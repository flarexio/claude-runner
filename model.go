package runner

import "errors"

const (
	EventIssue = "issue"

	IssueMarker = "<!-- agent-task:v1 -->"

	LabelTypeAgentTask     = "type:agent-task"
	LabelAgentClaudeCode   = "agent:claude-code"
	LabelAgentReady        = "agent-ready"
	LabelAgentApproved     = "agent-approved"
	LabelClaimedByClaude   = "claimed-by-claude"
	LabelAgentBlocked      = "agent-blocked"
	LabelSecuritySensitive = "security-sensitive"
	LabelAgentFailed       = "agent-failed"

	LabelModelFast     = "model:fast"
	LabelModelBalanced = "model:balanced"
	LabelModelStrong   = "model:strong"
)

var (
	RequiredIssueLabels = []string{
		LabelTypeAgentTask,
		LabelAgentClaudeCode,
		LabelAgentReady,
		LabelAgentApproved,
	}

	ExcludedIssueLabels = []string{
		LabelClaimedByClaude,
		LabelAgentBlocked,
		LabelSecuritySensitive,
	}

	KnownModelLabels = []string{
		LabelModelFast,
		LabelModelBalanced,
		LabelModelStrong,
	}
)

var (
	ErrTaskNotFound             = errors.New("task not found")
	ErrInvalidPrompt            = errors.New("invalid prompt")
	ErrExecFailed               = errors.New("claude execution failed")
	ErrInvalidRepo              = errors.New("invalid repo")
	ErrInvalidIssueNumber       = errors.New("invalid issue number")
	ErrIssueNotOpen             = errors.New("issue is not open")
	ErrIssueMarkerMissing       = errors.New("issue body missing agent task marker")
	ErrIssueLabelMissing        = errors.New("issue missing required label")
	ErrIssueLabelExcluded       = errors.New("issue has excluded label")
	ErrIssueMultipleModelLabels = errors.New("issue has multiple model recommendation labels")
	ErrGitHubUnavailable        = errors.New("github client not configured")
)

type Config struct {
	WorkDir      string       `yaml:"workDir"`
	AllowedTools []string     `yaml:"allowedTools"`
	MaxTurns     int          `yaml:"maxTurns"`
	Model        string       `yaml:"model,omitempty"`
	Issue        EventConfig  `yaml:"issue,omitempty"`
	GitHub       GitHubConfig `yaml:"github,omitempty"`
}

// EventConfig overrides the top-level Claude flags for a specific event.
// Empty fields fall back to the top-level Config values.
//
// BypassPermissions, when true, passes --dangerously-skip-permissions to
// claude and ignores AllowedTools. Use only for trusted unattended flows
// (issue mode behind label + author gates) where no human is watching tool
// calls.
//
// PreserveOnFailure (issue mode only) keeps the cloned workspace under
// WorkDir when claude exits non-zero, so an operator can inspect the run
// before re-triggering. Successful runs still clean up. CI / PR review
// runs ignore this flag and always clean up. The pointer type lets us
// distinguish an omitted setting (nil → defaults to true; the failed
// workspace is the best debugging artifact) from an explicit
// preserveOnFailure: false opt-out.
type EventConfig struct {
	AllowedTools      []string          `yaml:"allowedTools,omitempty"`
	MaxTurns          int               `yaml:"maxTurns,omitempty"`
	Model             string            `yaml:"model,omitempty"`
	ModelLabels       map[string]string `yaml:"modelLabels,omitempty"`
	BypassPermissions bool              `yaml:"bypassPermissions,omitempty"`
	PreserveOnFailure *bool             `yaml:"preserveOnFailure,omitempty"`
}

// PreserveOnFailureOrDefault resolves PreserveOnFailure to a concrete bool,
// applying the runtime default of true for issue mode when the field is
// omitted.
func (c EventConfig) PreserveOnFailureOrDefault() bool {
	if c.PreserveOnFailure == nil {
		return true
	}
	return *c.PreserveOnFailure
}

type GitHubConfig struct {
	Token   string `yaml:"token,omitempty"`
	BaseURL string `yaml:"baseURL,omitempty"`
}
