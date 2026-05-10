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
	Run(ctx context.Context, req RunRequest) (*Result, error)
	RunIssue(ctx context.Context, req RunIssueRequest) (*Result, error)
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

// Close blocks until in-flight background work finishes. It does not cancel;
// background goroutines run on a fresh context independent of any request.
func (svc *service) Close() error {
	svc.bgWg.Wait()
	return nil
}

func (svc *service) launchBg(fn func(context.Context)) {
	svc.bgWg.Add(1)
	go func() {
		defer svc.bgWg.Done()
		fn(context.Background())
	}()
}

func (svc *service) Run(ctx context.Context, req RunRequest) (*Result, error) {
	if req.Prompt == "" {
		return nil, ErrInvalidPrompt
	}
	return svc.runStateless(ctx, req)
}

func (svc *service) runStateless(ctx context.Context, req RunRequest) (*Result, error) {
	result, _, err := svc.runClaudeInTemporaryWorkspace(ctx, req, runOptions{})
	return result, err
}

type runOptions struct {
	// preserveOnFailure: keep the cloned workspace when claude exits non-zero
	// so an operator can inspect. Only applies when req.Repo != "".
	preserveOnFailure bool
	// issueTaskID, when set, switches the workspace to the stable issue
	// layout: <WorkDir>/<issueTaskID>/repo/ for the git checkout, with the
	// task root reserved for sibling runner metadata (e.g. .claude-runner/).
	// Empty value keeps the per-run ULID layout used by CI / PR review.
	issueTaskID string
}

type workspaceOutcome struct {
	dir       string
	preserved bool
}

func (svc *service) runClaudeInTemporaryWorkspace(ctx context.Context, req RunRequest, opts runOptions) (*Result, workspaceOutcome, error) {
	id := ulid.Make().String()

	taskRoot, workDir := svc.resolveWorkspacePaths(req, opts, id)

	if err := svc.prepareWorkDir(ctx, req, workDir); err != nil {
		return nil, workspaceOutcome{}, err
	}

	ws := workspaceOutcome{dir: taskRoot}
	if req.Repo != "" {
		defer func() {
			if ws.preserved {
				svc.log.Info("preserving failed issue workspace",
					zap.String("task_root", taskRoot),
					zap.String("id", id))
				return
			}
			if err := os.RemoveAll(taskRoot); err != nil {
				svc.log.Warn("remove workspace", zap.String("task_root", taskRoot), zap.Error(err))
			}
		}()
	}

	var diff string
	if req.BaseRef != "" {
		var err error
		diff, err = svc.generateDiff(ctx, req, workDir)
		if err != nil {
			return nil, ws, err
		}
	}

	prompt, err := svc.preparePrompt(req, workDir, diff)
	if err != nil {
		return nil, ws, err
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
		if opts.preserveOnFailure && req.Repo != "" {
			ws.preserved = true
		}
		return result, ws, nil
	}

	result.Output = stdout.String()

	return result, ws, nil
}

func (svc *service) buildArgs(req RunRequest) []string {
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
	model := cfg.Model
	if req.Model != "" {
		model = req.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	return args
}

// eventConfig resolves Claude flags for an event. Issue events fall through
// cfg.Issue → top-level; other events use the top-level values directly.
func (svc *service) eventConfig(event string) EventConfig {
	if event != EventIssue {
		return EventConfig{
			AllowedTools: svc.cfg.AllowedTools,
			MaxTurns:     svc.cfg.MaxTurns,
			Model:        svc.cfg.Model,
		}
	}

	resolved := svc.cfg.Issue
	if len(resolved.AllowedTools) == 0 {
		resolved.AllowedTools = svc.cfg.AllowedTools
	}
	if resolved.MaxTurns == 0 {
		resolved.MaxTurns = svc.cfg.MaxTurns
	}
	if resolved.Model == "" {
		resolved.Model = svc.cfg.Model
	}
	return resolved
}

func (svc *service) preparePrompt(req RunRequest, workDir string, diff string) (string, error) {
	// Issue prompts are built upstream by buildIssuePrompt; skip the PR trailer.
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

func (svc *service) generateDiff(ctx context.Context, req RunRequest, workDir string) (string, error) {
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

// resolveWorkspacePaths returns (taskRoot, workDir) for a run.
//   - existing-workspace mode (req.Repo == ""): both are svc.cfg.WorkDir.
//   - issue mode (opts.issueTaskID set): <WorkDir>/<issueTaskID>/ and
//     <WorkDir>/<issueTaskID>/repo/. The runner reserves the task root for
//     sibling metadata (.claude-runner/), so the git checkout lives under repo/.
//   - CI / PR review mode: per-run ULID; workDir == taskRoot.
func (svc *service) resolveWorkspacePaths(req RunRequest, opts runOptions, runID string) (string, string) {
	if req.Repo == "" {
		return svc.cfg.WorkDir, svc.cfg.WorkDir
	}
	if opts.issueTaskID != "" {
		taskRoot := filepath.Join(svc.cfg.WorkDir, opts.issueTaskID)
		return taskRoot, filepath.Join(taskRoot, "repo")
	}
	root := filepath.Join(svc.cfg.WorkDir, runID)
	return root, root
}

func (svc *service) prepareWorkDir(ctx context.Context, req RunRequest, workDir string) error {
	if req.Repo == "" {
		return nil
	}

	// Stable issue layout may already have a leftover repo/ from a previous
	// preserved-on-failure run; git clone refuses non-empty targets.
	if err := os.RemoveAll(workDir); err != nil {
		return fmt.Errorf("clear stale workspace: %w", err)
	}
	if parent := filepath.Dir(workDir); parent != svc.cfg.WorkDir {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("create workspace parent: %w", err)
		}
	}

	// core.longpaths lets git create paths > MAX_PATH on Windows; harmless elsewhere.
	args := []string{"-c", "core.longpaths=true", "clone"}
	if req.BaseRef == "" {
		args = append(args, "--depth", "1")
	}
	if req.Ref != "" {
		args = append(args, "--branch", req.Ref)
	}

	args = append(args, req.Repo, workDir)

	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if removeErr := os.RemoveAll(workDir); removeErr != nil {
			svc.log.Warn("remove failed clone", zap.String("work_dir", workDir), zap.Error(removeErr))
		}
		return fmt.Errorf("git clone failed: %s: %w", strings.TrimSpace(stderr.String()), err)
	}

	return nil
}
