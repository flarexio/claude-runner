package runner

import (
	"os"
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
		Diff:     "diff --git a/a.go b/a.go\n",
	}, workDir)
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

	prompt, err := svc.preparePrompt(Request{Prompt: "Run tests"}, t.TempDir())
	if err != nil {
		t.Fatalf("preparePrompt() error = %v", err)
	}
	if prompt != "Run tests" {
		t.Fatalf("prompt = %q", prompt)
	}
}
