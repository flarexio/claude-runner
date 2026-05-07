package runner

import "errors"

const (
	EventIssue       = "issue"
	EventPullRequest = "pull_request"

	IssueMarker = "<!-- agent-task:v1 -->"

	LabelTypeAgentTask     = "type:agent-task"
	LabelAgentClaudeCode   = "agent:claude-code"
	LabelAgentReady        = "agent-ready"
	LabelAgentApproved     = "agent-approved"
	LabelClaimedByClaude   = "claimed-by-claude"
	LabelAgentBlocked      = "agent-blocked"
	LabelSecuritySensitive = "security-sensitive"
	LabelAgentFailed       = "agent-failed"
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
)

var (
	ErrTaskNotFound       = errors.New("task not found")
	ErrInvalidPrompt      = errors.New("invalid prompt")
	ErrExecFailed         = errors.New("claude execution failed")
	ErrInvalidRepo        = errors.New("invalid repo")
	ErrInvalidIssueNumber = errors.New("invalid issue number")
	ErrIssueNotOpen       = errors.New("issue is not open")
	ErrIssueMarkerMissing = errors.New("issue body missing agent task marker")
	ErrIssueLabelMissing  = errors.New("issue missing required label")
	ErrIssueLabelExcluded = errors.New("issue has excluded label")
	ErrGitHubUnavailable  = errors.New("github client not configured")
)

type Config struct {
	WorkDir      string       `yaml:"workDir"`
	AllowedTools []string     `yaml:"allowedTools"`
	MaxTurns     int          `yaml:"maxTurns"`
	GitHub       GitHubConfig `yaml:"github,omitempty"`
}

type GitHubConfig struct {
	Token   string `yaml:"token,omitempty"`
	BaseURL string `yaml:"baseURL,omitempty"`
}
