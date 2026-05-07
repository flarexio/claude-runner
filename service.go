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
	RunIssue(ctx context.Context, req Request) (*Result, error)
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

// Close blocks until any background work (e.g. async issue execution) has
// finished. Close does not cancel in-flight work; the goroutines run on a
// fresh context independent of any caller's request lifetime.
func (svc *service) Close() error {
	svc.bgWg.Wait()
	return nil
}

// launchBg runs fn in a tracked goroutine with a fresh context so its
// lifetime is decoupled from the caller's request context.
func (svc *service) launchBg(fn func(context.Context)) {
	svc.bgWg.Add(1)
	go func() {
		defer svc.bgWg.Done()
		fn(context.Background())
	}()
}

// Run handles prompt-only and pull-request review jobs synchronously.
// Issue jobs go through RunIssue.
func (svc *service) Run(ctx context.Context, req Request) (*Result, error) {
	if req.Prompt == "" {
		return nil, ErrInvalidPrompt
	}
	return svc.execute(ctx, req)
}

// execute clones (when needed), generates a diff (when applicable), composes
// the final prompt, and runs claude. Used by Run and by the background
// portion of RunIssue.
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
		diff, err = svc.generateDiff(ctx, req, workDir)
		if err != nil {
			return nil, err
		}
	}

	prompt, err := svc.preparePrompt(req, workDir, diff)
	if err != nil {
		return nil, err
	}

	req.Prompt = prompt
	args := svc.buildArgs(req)

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	result := &Result{ID: id}

	if err := cmd.Run(); err != nil {
		result.Output = stdout.String()
		result.Error = stderr.String()
		if result.Error == "" {
			result.Error = err.Error()
		}

		return result, nil
	}

	result.Output = stdout.String()

	return result, nil
}

func (svc *service) buildArgs(req Request) []string {
	args := []string{"-p", req.Prompt}

	cfg := svc.eventConfig(req.Event)
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
	// Issue events build their prompt before reaching execute; no PR trailer.
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

func (svc *service) generateDiff(ctx context.Context, req Request, workDir string) (string, error) {
	fetch := exec.CommandContext(ctx, "git", "fetch", "origin", req.BaseRef)
	fetch.Dir = workDir

	var fetchStderr bytes.Buffer
	fetch.Stderr = &fetchStderr
	if err := fetch.Run(); err != nil {
		return "", fmt.Errorf("git fetch base ref %q failed: %s: %w", req.BaseRef, fetchStderr.String(), err)
	}

	diff := exec.CommandContext(ctx, "git", "diff", "--no-ext-diff", "--binary", "FETCH_HEAD...HEAD")
	diff.Dir = workDir

	var stdout, stderr bytes.Buffer
	diff.Stdout = &stdout
	diff.Stderr = &stderr
	if err := diff.Run(); err != nil {
		return "", fmt.Errorf("git diff base ref %q failed: %s: %w", req.BaseRef, stderr.String(), err)
	}

	return stdout.String(), nil
}

func (svc *service) prepareWorkDir(ctx context.Context, req Request, id string) (string, error) {
	if req.Repo == "" {
		return svc.cfg.WorkDir, nil
	}

	workDir := filepath.Join(svc.cfg.WorkDir, id)

	args := []string{"clone"}
	if req.BaseRef == "" {
		args = append(args, "--depth", "1")
	}
	if req.Ref != "" {
		args = append(args, "--branch", req.Ref)
	}

	args = append(args, req.Repo, workDir)

	cmd := exec.CommandContext(ctx, "git", args...)
	if err := cmd.Run(); err != nil {
		if removeErr := os.RemoveAll(workDir); removeErr != nil {
			svc.log.Warn("remove failed clone", zap.String("work_dir", workDir), zap.Error(removeErr))
		}
		return "", fmt.Errorf("git clone failed: %w", err)
	}

	return workDir, nil
}
