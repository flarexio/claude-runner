package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestBuildArgsUsesIssueOverride(t *testing.T) {
	svc := &service{cfg: Config{
		AllowedTools: []string{"Read", "Glob"},
		MaxTurns:     10,
		Issue: EventConfig{
			AllowedTools: []string{"Read", "Edit", "Write", "Bash"},
			MaxTurns:     30,
		},
	}}

	args := claudeArgs("p", svc.eventConfig(EventIssue))

	want := []string{"-p", "p", "--allowedTools", "Read,Edit,Write,Bash", "--max-turns", "30"}
	if !sliceEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestBuildArgsIssueFallsBackToDefault(t *testing.T) {
	svc := &service{cfg: Config{
		AllowedTools: []string{"Read", "Glob"},
		MaxTurns:     10,
	}}

	args := claudeArgs("p", svc.eventConfig(EventIssue))

	want := []string{"-p", "p", "--allowedTools", "Read,Glob", "--max-turns", "10"}
	if !sliceEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestBuildArgsIssueBypassPermissions(t *testing.T) {
	svc := &service{cfg: Config{
		AllowedTools: []string{"Read", "Glob"},
		MaxTurns:     10,
		Issue: EventConfig{
			AllowedTools:      []string{"Edit", "Write"},
			MaxTurns:          30,
			BypassPermissions: true,
		},
	}}

	args := claudeArgs("p", svc.eventConfig(EventIssue))

	want := []string{"-p", "p", "--dangerously-skip-permissions", "--max-turns", "30"}
	if !sliceEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestBuildArgsNonIssueIgnoresIssueOverride(t *testing.T) {
	svc := &service{cfg: Config{
		AllowedTools: []string{"Read", "Glob"},
		MaxTurns:     10,
		Issue: EventConfig{
			AllowedTools: []string{"Edit", "Write"},
			MaxTurns:     30,
		},
	}}

	args := claudeArgs("p", svc.eventConfig("pull_request"))

	want := []string{"-p", "p", "--allowedTools", "Read,Glob", "--max-turns", "10"}
	if !sliceEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPreparePromptWritesDiffContext(t *testing.T) {
	workDir := t.TempDir()
	svc := &service{}

	prompt, err := svc.preparePrompt(Request{
		Prompt:   "Review changed files",
		Ref:      "feature/review",
		BaseRef:  "main",
		Event:    "pull_request",
		PRNumber: 2,
	}, workDir, "diff --git a/a.go b/a.go\n")
	if err != nil {
		t.Fatalf("preparePrompt() error = %v", err)
	}

	diff, err := os.ReadFile(filepath.Join(workDir, "claude-runner.diff"))
	if err != nil {
		t.Fatalf("read diff file: %v", err)
	}
	if string(diff) != "diff --git a/a.go b/a.go\n" {
		t.Fatalf("diff file = %q", string(diff))
	}

	for _, want := range []string{
		"Review changed files",
		"- Event: pull_request",
		"- PR number: 2",
		"- Base ref: main",
		"- Head ref: feature/review",
		"Use claude-runner.diff as the authoritative review scope.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not contain %q:\n%s", want, prompt)
		}
	}
}

func TestPreparePromptLeavesPlainPromptUnchanged(t *testing.T) {
	svc := &service{}

	prompt, err := svc.preparePrompt(Request{Prompt: "Run tests"}, t.TempDir(), "")
	if err != nil {
		t.Fatalf("preparePrompt() error = %v", err)
	}
	if prompt != "Run tests" {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestGenerateDiffFromBaseRef(t *testing.T) {
	remote := t.TempDir()
	runGit(t, "", "init", "--bare", remote)

	src := t.TempDir()
	runGit(t, "", "clone", remote, src)
	runGit(t, src, "config", "user.email", "test@example.com")
	runGit(t, src, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(src, "app.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	runGit(t, src, "add", "app.txt")
	runGit(t, src, "commit", "-m", "base")
	runGit(t, src, "branch", "-M", "main")
	runGit(t, src, "push", "origin", "main")

	runGit(t, src, "checkout", "-b", "feature/review")
	if err := os.WriteFile(filepath.Join(src, "app.txt"), []byte("base\nhead\n"), 0o644); err != nil {
		t.Fatalf("write head file: %v", err)
	}
	runGit(t, src, "commit", "-am", "head")
	runGit(t, src, "push", "origin", "feature/review")

	workspaces := t.TempDir()
	svc := &service{cfg: Config{WorkDir: workspaces}}

	workDir, err := svc.prepareWorkDir(context.Background(), Request{
		Repo:    remote,
		Ref:     "feature/review",
		BaseRef: "main",
	}, "run")
	if err != nil {
		t.Fatalf("prepareWorkDir() error = %v", err)
	}

	diff, err := generateDiff(context.Background(), workDir, "main")
	if err != nil {
		t.Fatalf("generateDiff() error = %v", err)
	}
	if !strings.Contains(diff, "+head") {
		t.Fatalf("diff does not contain head change:\n%s", diff)
	}
}

func TestRunRemovesClonedWorkDirAfterClaudeSuccess(t *testing.T) {
	remote := newRemoteRepo(t)
	workspaces := t.TempDir()
	svc := &service{cfg: Config{WorkDir: workspaces}}
	prependFakeClaude(t, 0)

	result, err := svc.Run(context.Background(), Request{
		Prompt: "Run tests",
		Repo:   remote,
		Ref:    "main",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(workspaces, result.ID)); !os.IsNotExist(err) {
		t.Fatalf("workspace was not removed, stat err = %v", err)
	}
}

func TestRunRemovesClonedWorkDirAfterClaudeFailure(t *testing.T) {
	remote := newRemoteRepo(t)
	workspaces := t.TempDir()
	svc := &service{cfg: Config{WorkDir: workspaces}}
	prependFakeClaude(t, 1)

	result, err := svc.Run(context.Background(), Request{
		Prompt: "Run tests",
		Repo:   remote,
		Ref:    "main",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Error == "" {
		t.Fatal("Run() result error is empty")
	}

	if _, err := os.Stat(filepath.Join(workspaces, result.ID)); !os.IsNotExist(err) {
		t.Fatalf("workspace was not removed, stat err = %v", err)
	}
}

func TestPrepareWorkDirRemovesFailedClone(t *testing.T) {
	workspaces := t.TempDir()
	svc := &service{cfg: Config{WorkDir: workspaces}}

	workDir, err := svc.prepareWorkDir(context.Background(), Request{
		Repo: filepath.Join(t.TempDir(), "missing.git"),
	}, "run")
	if err == nil {
		t.Fatal("prepareWorkDir() error = nil")
	}
	if workDir != "" {
		t.Fatalf("workDir = %q, want empty", workDir)
	}
	if _, err := os.Stat(filepath.Join(workspaces, "run")); !os.IsNotExist(err) {
		t.Fatalf("failed clone workspace was not removed, stat err = %v", err)
	}
}

func newRemoteRepo(t *testing.T) string {
	t.Helper()

	remote := t.TempDir()
	runGit(t, "", "init", "--bare", remote)

	src := t.TempDir()
	runGit(t, "", "clone", remote, src)
	runGit(t, src, "config", "user.email", "test@example.com")
	runGit(t, src, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(src, "app.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	runGit(t, src, "add", "app.txt")
	runGit(t, src, "commit", "-m", "base")
	runGit(t, src, "branch", "-M", "main")
	runGit(t, src, "push", "origin", "main")

	return remote
}

func prependFakeClaude(t *testing.T, exitCode int) {
	t.Helper()

	bin := t.TempDir()
	name := "claude"
	exitCodeString := strconv.Itoa(exitCode)
	content := "#!/bin/sh\necho fake output\nexit " + exitCodeString + "\n"
	if runtime.GOOS == "windows" {
		name = "claude.bat"
		content = "@echo off\r\necho fake output\r\necho fake error 1>&2\r\nexit /b " + exitCodeString + "\r\n"
	}

	path := filepath.Join(bin, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %s: %v", strings.Join(args, " "), out, err)
	}
}
