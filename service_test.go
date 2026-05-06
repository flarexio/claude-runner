package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

	diff, err := svc.generateDiff(context.Background(), Request{BaseRef: "main"}, workDir)
	if err != nil {
		t.Fatalf("generateDiff() error = %v", err)
	}
	if !strings.Contains(diff, "+head") {
		t.Fatalf("diff does not contain head change:\n%s", diff)
	}
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
