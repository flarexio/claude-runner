package runner

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigUnmarshalYAMLDefaultsIssuePreserveOnFailureWhenIssueOmitted(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(`workDir: /tmp/runner`), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if !cfg.Issue.PreserveOnFailure {
		t.Fatal("omitted issue block left PreserveOnFailure false, want default true")
	}
	if cfg.WorkDir != "/tmp/runner" {
		t.Fatalf("WorkDir = %q", cfg.WorkDir)
	}
}

func TestConfigUnmarshalYAMLPreservesTopLevelFields(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(`
workDir: /tmp/workspaces
allowedTools:
- Read
- Grep
maxTurns: 11
model: claude-sonnet-4-6
github:
  token: token-from-file
  baseURL: https://example.test/api
`), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if cfg.WorkDir != "/tmp/workspaces" {
		t.Fatalf("WorkDir = %q", cfg.WorkDir)
	}
	if len(cfg.AllowedTools) != 2 || cfg.AllowedTools[0] != "Read" || cfg.AllowedTools[1] != "Grep" {
		t.Fatalf("AllowedTools = %#v", cfg.AllowedTools)
	}
	if cfg.MaxTurns != 11 {
		t.Fatalf("MaxTurns = %d", cfg.MaxTurns)
	}
	if cfg.Model != "claude-sonnet-4-6" {
		t.Fatalf("Model = %q", cfg.Model)
	}
	if cfg.GitHub.Token != "token-from-file" || cfg.GitHub.BaseURL != "https://example.test/api" {
		t.Fatalf("GitHub = %#v", cfg.GitHub)
	}
	if !cfg.Issue.PreserveOnFailure {
		t.Fatal("omitted issue block did not default PreserveOnFailure to true")
	}
}

func TestEventConfigUnmarshalYAMLDefaultsPreserveOnFailure(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(`issue: {}`), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if !cfg.Issue.PreserveOnFailure {
		t.Fatal("omitted issue.preserveOnFailure = false, want default true")
	}
}

func TestEventConfigUnmarshalYAMLAllowsExplicitPreserveOnFailureFalse(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("issue:\n  preserveOnFailure: false\n"), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if cfg.Issue.PreserveOnFailure {
		t.Fatal("explicit issue.preserveOnFailure=false decoded true")
	}
}

func TestEventConfigUnmarshalYAMLAllowsExplicitPreserveOnFailureTrue(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("issue:\n  preserveOnFailure: true\n"), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if !cfg.Issue.PreserveOnFailure {
		t.Fatal("explicit issue.preserveOnFailure=true decoded false")
	}
}

func TestEventConfigUnmarshalYAMLPreservesOtherFields(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte(`
issue:
  allowedTools:
  - Read
  - Bash
  maxTurns: 7
  model: claude-sonnet-4-6
  modelLabels:
    model:fast: claude-haiku-4-5
    model:balanced: claude-sonnet-4-6
  bypassPermissions: true
`), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	issue := cfg.Issue
	if !issue.PreserveOnFailure {
		t.Fatal("omitted preserveOnFailure did not default to true")
	}
	if len(issue.AllowedTools) != 2 || issue.AllowedTools[0] != "Read" || issue.AllowedTools[1] != "Bash" {
		t.Fatalf("AllowedTools = %#v", issue.AllowedTools)
	}
	if issue.MaxTurns != 7 {
		t.Fatalf("MaxTurns = %d, want 7", issue.MaxTurns)
	}
	if issue.Model != "claude-sonnet-4-6" {
		t.Fatalf("Model = %q", issue.Model)
	}
	if issue.ModelLabels[LabelModelFast] != "claude-haiku-4-5" || issue.ModelLabels[LabelModelBalanced] != "claude-sonnet-4-6" {
		t.Fatalf("ModelLabels = %#v", issue.ModelLabels)
	}
	if !issue.BypassPermissions {
		t.Fatal("BypassPermissions = false, want true")
	}
}
