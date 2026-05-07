package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/oklog/ulid/v2"
	"go.uber.org/zap"
)

type Service interface {
	Close() error
	Run(ctx context.Context, req Request) (*Result, error)
}

type ServiceMiddleware func(Service) Service

func NewService(cfg Config) (Service, error) {
	log := zap.L().With(
		zap.String("service", "claude-runner"),
	)

	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, err
	}

	svc := &service{
		cfg: cfg,
		log: log,
	}
	if cfg.GitHub.Token != "" {
		svc.github = NewGitHubClient(cfg.GitHub)
	}

	return svc, nil
}

type service struct {
	cfg    Config
	log    *zap.Logger
	github GitHubClient

	bgWg sync.WaitGroup
}

// Close blocks until all background goroutines finish. Background work
// (e.g. async issue execution) inherits a fresh context, so Close does not
// cancel in-flight work — it only waits for it.
func (svc *service) Close() error {
	svc.bgWg.Wait()
	return nil
}

// launchBg runs fn in a tracked goroutine. The goroutine receives a fresh
// context so its lifetime is decoupled from any caller's request context.
func (svc *service) launchBg(fn func(context.Context)) {
	svc.bgWg.Add(1)
	go func() {
		defer svc.bgWg.Done()
		fn(context.Background())
	}()
}

func (svc *service) Run(ctx context.Context, req Request) (*Result, error) {
	if req.Event == EventIssue {
		return svc.runIssue(ctx, req)
	}
	return svc.runPrompt(ctx, req)
}

func (svc *service) runPrompt(ctx context.Context, req Request) (*Result, error) {
	if req.Prompt == "" {
		return nil, ErrInvalidPrompt
	}
	return svc.execute(ctx, req)
}

// execute runs a synchronous Claude invocation for non-issue events. It
// clones (when needed), generates a diff (when applicable), composes the
// final prompt, and runs claude.
func (svc *service) execute(ctx context.Context, req Request) (*Result, error) {
	id := ulid.Make().String()

	workDir, err := svc.prepareWorkDir(ctx, req, id)
	if err != nil {
		return nil, err
	}
	if req.Repo != "" {
		defer func() {
			if err := os.RemoveAll(workDir); err != nil {
				svc.log.Warn("remove workspace", zap.String("work_dir", workDir), zap.Error(err))
			}
		}()
	}

	var diff string
	if req.BaseRef != "" {
		diff, err = generateDiff(ctx, workDir, req.BaseRef)
		if err != nil {
			return nil, err
		}
	}

	prompt, err := svc.preparePrompt(req, workDir, diff)
	if err != nil {
		return nil, err
	}

	args := claudeArgs(prompt, svc.eventConfig(req.Event))
	result := runClaude(ctx, workDir, args)
	result.ID = id
	return result, nil
}

// eventConfig resolves the effective Claude flags for an event. Issue events
// read from cfg.Issue first and fall through to the top-level values; other
// events always use the top-level values.
func (svc *service) eventConfig(event string) EventConfig {
	if event != EventIssue {
		return EventConfig{
			AllowedTools: svc.cfg.AllowedTools,
			MaxTurns:     svc.cfg.MaxTurns,
		}
	}

	resolved := svc.cfg.Issue
	if len(resolved.AllowedTools) == 0 {
		resolved.AllowedTools = svc.cfg.AllowedTools
	}
	if resolved.MaxTurns == 0 {
		resolved.MaxTurns = svc.cfg.MaxTurns
	}
	return resolved
}

func (svc *service) preparePrompt(req Request, workDir string, diff string) (string, error) {
	// Issue events build their prompt in runIssue; no PR trailer.
	if req.Event == EventIssue {
		return req.Prompt, nil
	}
	if diff == "" && req.BaseRef == "" && req.Event == "" && req.PRNumber == 0 {
		return req.Prompt, nil
	}

	var diffPath string
	if diff != "" {
		diffPath = filepath.Join(workDir, "claude-runner.diff")
		if err := os.WriteFile(diffPath, []byte(diff), 0o600); err != nil {
			return "", fmt.Errorf("write diff: %w", err)
		}
	}

	var b strings.Builder
	b.WriteString(req.Prompt)
	b.WriteString("\n\n")
	b.WriteString("Pull request context supplied by claude-runner:\n")

	if req.Event != "" {
		fmt.Fprintf(&b, "- Event: %s\n", req.Event)
	}
	if req.PRNumber != 0 {
		fmt.Fprintf(&b, "- PR number: %d\n", req.PRNumber)
	}
	if req.BaseRef != "" {
		fmt.Fprintf(&b, "- Base ref: %s\n", req.BaseRef)
	}
	if req.Ref != "" {
		fmt.Fprintf(&b, "- Head ref: %s\n", req.Ref)
	}
	if diffPath != "" {
		b.WriteString("- Diff file: claude-runner.diff\n")
		b.WriteString("\nUse claude-runner.diff as the authoritative review scope. Review only changes shown in that diff unless the prompt explicitly asks otherwise.\n")
	}

	return b.String(), nil
}

func (svc *service) prepareWorkDir(ctx context.Context, req Request, id string) (string, error) {
	if req.Repo == "" {
		return svc.cfg.WorkDir, nil
	}

	workDir := filepath.Join(svc.cfg.WorkDir, id)
	shallow := req.BaseRef == ""
	if err := cloneRepo(ctx, req.Repo, req.Ref, workDir, shallow); err != nil {
		return "", err
	}
	return workDir, nil
}

// cloneRepo clones cloneURL into workDir. If ref is non-empty, checks out
// that branch. Use shallow=true for one-shot runs (issue mode, simple
// prompts) and false when downstream code needs to fetch a base ref for
// diffing.
func cloneRepo(ctx context.Context, cloneURL, ref, workDir string, shallow bool) error {
	args := []string{"clone"}
	if shallow {
		args = append(args, "--depth", "1")
	}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, cloneURL, workDir)

	cmd := exec.CommandContext(ctx, "git", args...)
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(workDir)
		return fmt.Errorf("git clone failed: %w", err)
	}
	return nil
}

// generateDiff fetches baseRef into workDir and returns the diff between
// FETCH_HEAD and HEAD.
func generateDiff(ctx context.Context, workDir, baseRef string) (string, error) {
	fetch := exec.CommandContext(ctx, "git", "fetch", "origin", baseRef)
	fetch.Dir = workDir

	var fetchStderr bytes.Buffer
	fetch.Stderr = &fetchStderr
	if err := fetch.Run(); err != nil {
		return "", fmt.Errorf("git fetch base ref %q failed: %s: %w", baseRef, fetchStderr.String(), err)
	}

	diff := exec.CommandContext(ctx, "git", "diff", "--no-ext-diff", "--binary", "FETCH_HEAD...HEAD")
	diff.Dir = workDir

	var stdout, stderr bytes.Buffer
	diff.Stdout = &stdout
	diff.Stderr = &stderr
	if err := diff.Run(); err != nil {
		return "", fmt.Errorf("git diff base ref %q failed: %s: %w", baseRef, stderr.String(), err)
	}
	return stdout.String(), nil
}

// claudeArgs builds the argument list for `claude -p` from a prompt and a
// resolved EventConfig.
func claudeArgs(prompt string, cfg EventConfig) []string {
	args := []string{"-p", prompt}

	if cfg.BypassPermissions {
		args = append(args, "--dangerously-skip-permissions")
	} else if len(cfg.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(cfg.AllowedTools, ","))
	}
	if cfg.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", cfg.MaxTurns))
	}
	return args
}

// runClaude invokes the claude binary in workDir with the given args. The
// returned Result captures stdout/stderr; a non-zero exit is reported via
// Result.Error rather than a returned error (mirrors existing semantics).
// Result.ID is left empty for the caller to populate.
func runClaude(ctx context.Context, workDir string, args []string) *Result {
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	result := &Result{}
	if err := cmd.Run(); err != nil {
		result.Output = stdout.String()
		result.Error = stderr.String()
		if result.Error == "" {
			result.Error = err.Error()
		}
		return result
	}
	result.Output = stdout.String()
	return result
}
