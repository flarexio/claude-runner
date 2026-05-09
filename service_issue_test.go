package runner

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
)

// fakeGitHub captures calls and returns canned responses for tests.
type fakeGitHub struct {
	mu       sync.Mutex
	issue    *Issue
	getErr   error
	added    []string
	removed  []string
	comments []string
}

func (f *fakeGitHub) GetIssue(_ context.Context, _ string, _ int) (*Issue, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.issue, nil
}

func (f *fakeGitHub) AddLabels(_ context.Context, _ string, _ int, labels []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, labels...)
	return nil
}

func (f *fakeGitHub) RemoveLabel(_ context.Context, _ string, _ int, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, label)
	return nil
}

func (f *fakeGitHub) CreateComment(_ context.Context, _ string, _ int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, body)
	return nil
}

func validIssue() *Issue {
	return &Issue{
		Number:  42,
		Title:   "Add feature X",
		Body:    "Please implement feature X.\n" + IssueMarker + "\n",
		State:   "open",
		HTMLURL: "https://github.com/o/r/issues/42",
		Labels: []Label{
			{Name: LabelTypeAgentTask},
			{Name: LabelAgentClaudeCode},
			{Name: LabelAgentReady},
			{Name: LabelAgentApproved},
		},
	}
}

func TestValidateIssueAccepts(t *testing.T) {
	if err := validateIssue(validIssue()); err != nil {
		t.Fatalf("validateIssue() = %v, want nil", err)
	}
}

func TestValidateIssueRejectsClosed(t *testing.T) {
	issue := validIssue()
	issue.State = "closed"
	if err := validateIssue(issue); !errors.Is(err, ErrIssueNotOpen) {
		t.Fatalf("err = %v, want ErrIssueNotOpen", err)
	}
}

func TestValidateIssueRequiresMarker(t *testing.T) {
	issue := validIssue()
	issue.Body = "no marker here"
	if err := validateIssue(issue); !errors.Is(err, ErrIssueMarkerMissing) {
		t.Fatalf("err = %v, want ErrIssueMarkerMissing", err)
	}
}

func TestValidateIssueRequiresAllLabels(t *testing.T) {
	for _, missing := range RequiredIssueLabels {
		t.Run(missing, func(t *testing.T) {
			issue := validIssue()
			kept := issue.Labels[:0]
			for _, l := range issue.Labels {
				if l.Name != missing {
					kept = append(kept, l)
				}
			}
			issue.Labels = kept
			err := validateIssue(issue)
			if !errors.Is(err, ErrIssueLabelMissing) {
				t.Fatalf("err = %v, want ErrIssueLabelMissing", err)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Fatalf("err = %v, want it to mention %q", err, missing)
			}
		})
	}
}

func TestValidateIssueRejectsExcludedLabels(t *testing.T) {
	for _, bad := range ExcludedIssueLabels {
		t.Run(bad, func(t *testing.T) {
			issue := validIssue()
			issue.Labels = append(issue.Labels, Label{Name: bad})
			err := validateIssue(issue)
			if !errors.Is(err, ErrIssueLabelExcluded) {
				t.Fatalf("err = %v, want ErrIssueLabelExcluded", err)
			}
			if !strings.Contains(err.Error(), bad) {
				t.Fatalf("err = %v, want it to mention %q", err, bad)
			}
		})
	}
}

func TestValidateIssueRejectsMultipleModelLabels(t *testing.T) {
	issue := validIssue()
	issue.Labels = append(issue.Labels, Label{Name: LabelModelFast}, Label{Name: LabelModelStrong})

	err := validateIssue(issue)
	if !errors.Is(err, ErrIssueMultipleModelLabels) {
		t.Fatalf("err = %v, want ErrIssueMultipleModelLabels", err)
	}
	if !strings.Contains(err.Error(), LabelModelFast) || !strings.Contains(err.Error(), LabelModelStrong) {
		t.Fatalf("err = %v, want both model labels mentioned", err)
	}
}

func TestSelectIssueModelUsesModelLabelMapping(t *testing.T) {
	svc := &service{cfg: Config{
		Model: "claude-sonnet-4-6",
		Issue: EventConfig{
			Model: "claude-opus-4-7",
			ModelLabels: map[string]string{
				LabelModelFast:   "claude-haiku-4-5",
				LabelModelStrong: "claude-opus-4-7",
			},
		},
	}}

	if got, want := svc.selectIssueModel(LabelModelFast), "claude-haiku-4-5"; got != want {
		t.Fatalf("selectIssueModel(fast) = %q, want %q", got, want)
	}
	if got, want := svc.selectIssueModel(LabelModelBalanced), "claude-opus-4-7"; got != want {
		t.Fatalf("selectIssueModel(unmapped balanced) = %q, want issue default %q", got, want)
	}
}

func TestRunIssuePassesModelSelectedFromLabel(t *testing.T) {
	issue := validIssue()
	issue.Labels = append(issue.Labels, Label{Name: LabelModelFast})
	gh := &fakeGitHub{issue: issue}
	workspaces := t.TempDir()
	svc := &service{
		cfg: Config{
			WorkDir: workspaces,
			Issue: EventConfig{
				Model: "claude-sonnet-4-6",
				ModelLabels: map[string]string{
					LabelModelFast: "claude-haiku-4-5",
				},
			},
		},
		log:    zap.NewNop(),
		github: gh,
	}
	argsPath := prependFakeClaudeRecording(t, 0)

	if _, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo:        newRemoteRepo(t),
		IssueNumber: 42,
	}); err != nil {
		t.Fatalf("RunIssue() error = %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	args := readClaudeArgs(t, argsPath)
	if !sliceContainsPair(args, "--model", "claude-haiku-4-5") {
		t.Fatalf("claude args = %v, want selected model", args)
	}
	claim := gh.comments[0]
	if !strings.Contains(claim, LabelModelFast) || !strings.Contains(claim, "claude-haiku-4-5") {
		t.Fatalf("claim comment = %q, want model selection details", claim)
	}
}

func TestRunIssueWorkflowAcceptsSyncAndCompletesInBackground(t *testing.T) {
	gh := &fakeGitHub{issue: validIssue()}
	workspaces := t.TempDir()
	svc := &service{cfg: Config{WorkDir: workspaces}, log: zap.NewNop(), github: gh}

	prependFakeClaude(t, 0)

	result, err := svc.runIssueWorkflow(context.Background(), RunIssueRequest{
		Repo:        newRemoteRepo(t),
		IssueNumber: 42,
	})
	if err != nil {
		t.Fatalf("runIssueWorkflow() error = %v", err)
	}
	if result.ID == "" {
		t.Fatal("result.ID is empty, want non-empty for accepted issue")
	}
	if !strings.Contains(result.Output, "accepted") {
		t.Fatalf("result.Output = %q, want accepted message", result.Output)
	}

	// Claim happened synchronously, before the background work is drained.
	if got, want := gh.removed, []string{LabelAgentReady}; !sliceEqual(got, want) {
		t.Fatalf("removed = %v, want %v", got, want)
	}
	if !slices.Contains(gh.added, LabelClaimedByClaude) {
		t.Fatalf("added does not include claimed-by-claude: %v", gh.added)
	}
	if len(gh.comments) < 1 || !strings.Contains(gh.comments[0], "claimed") {
		t.Fatalf("first comment must be the claim message: %v", gh.comments)
	}

	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if len(gh.comments) < 2 || !strings.Contains(gh.comments[len(gh.comments)-1], "finished") {
		t.Fatalf("expected final success comment, got %v", gh.comments)
	}
}

func TestRunIssueRequiresGitHubClient(t *testing.T) {
	svc := &service{cfg: Config{}, log: zap.NewNop()}
	_, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo:        "owner/repo",
		IssueNumber: 1,
	})
	if !errors.Is(err, ErrGitHubUnavailable) {
		t.Fatalf("err = %v, want ErrGitHubUnavailable", err)
	}
}

func TestRunIssueRequiresIssueNumber(t *testing.T) {
	svc := &service{cfg: Config{}, log: zap.NewNop(), github: &fakeGitHub{}}
	_, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo: "owner/repo",
	})
	if !errors.Is(err, ErrInvalidIssueNumber) {
		t.Fatalf("err = %v, want ErrInvalidIssueNumber", err)
	}
}

func TestRunIssueRejectsBadRepo(t *testing.T) {
	svc := &service{cfg: Config{}, log: zap.NewNop(), github: &fakeGitHub{}}
	_, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo:        "no-slash",
		IssueNumber: 1,
	})
	if !errors.Is(err, ErrInvalidRepo) {
		t.Fatalf("err = %v, want ErrInvalidRepo", err)
	}
}

func TestRunIssueValidationFailureSkipsClaim(t *testing.T) {
	gh := &fakeGitHub{issue: &Issue{State: "closed", Body: IssueMarker}}
	svc := &service{cfg: Config{}, log: zap.NewNop(), github: gh}

	_, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo:        "owner/repo",
		IssueNumber: 1,
	})
	if !errors.Is(err, ErrIssueNotOpen) {
		t.Fatalf("err = %v, want ErrIssueNotOpen", err)
	}
	if len(gh.removed) != 0 || len(gh.added) != 0 || len(gh.comments) != 0 {
		t.Fatalf("expected no GitHub mutations on validation failure: removed=%v added=%v comments=%v",
			gh.removed, gh.added, gh.comments)
	}
}

func TestRunIssueAcceptsSyncAndCompletesInBackground(t *testing.T) {
	gh := &fakeGitHub{issue: validIssue()}
	workspaces := t.TempDir()
	svc := &service{cfg: Config{WorkDir: workspaces}, log: zap.NewNop(), github: gh}

	prependFakeClaude(t, 0)

	result, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo:        newRemoteRepo(t),
		IssueNumber: 42,
	})
	if err != nil {
		t.Fatalf("RunIssue() error = %v", err)
	}
	if result.ID == "" {
		t.Fatal("result.ID is empty, want non-empty for accepted issue")
	}
	if !strings.Contains(result.Output, "accepted") {
		t.Fatalf("result.Output = %q, want accepted message", result.Output)
	}

	// Claim happened synchronously, before RunIssue returned.
	if got, want := gh.removed, []string{LabelAgentReady}; !sliceEqual(got, want) {
		t.Fatalf("removed = %v, want %v", got, want)
	}
	if !slices.Contains(gh.added, LabelClaimedByClaude) {
		t.Fatalf("added does not include claimed-by-claude: %v", gh.added)
	}
	if len(gh.comments) < 1 || !strings.Contains(gh.comments[0], "claimed") {
		t.Fatalf("first comment must be the claim message: %v", gh.comments)
	}

	// Drain the background goroutine before checking the success comment.
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if slices.Contains(gh.added, LabelAgentFailed) {
		t.Fatalf("added unexpectedly includes agent-failed: %v", gh.added)
	}
	if len(gh.comments) < 2 {
		t.Fatalf("expected claim + result comments, got %d: %v", len(gh.comments), gh.comments)
	}
	if !strings.Contains(gh.comments[len(gh.comments)-1], "finished") {
		t.Fatalf("last comment = %q, want success summary", gh.comments[len(gh.comments)-1])
	}
}

func TestRunIssueReportsFailure(t *testing.T) {
	gh := &fakeGitHub{issue: validIssue()}
	workspaces := t.TempDir()
	svc := &service{cfg: Config{WorkDir: workspaces}, log: zap.NewNop(), github: gh}

	prependFakeClaude(t, 1)

	if _, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo:        newRemoteRepo(t),
		IssueNumber: 42,
	}); err != nil {
		t.Fatalf("RunIssue() error = %v", err)
	}

	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if !slices.Contains(gh.added, LabelAgentFailed) {
		t.Fatalf("added does not include agent-failed: %v", gh.added)
	}
	failure := gh.comments[len(gh.comments)-1]
	if !strings.Contains(failure, "failed") {
		t.Fatalf("last comment = %q, want failure summary", failure)
	}
	if !strings.Contains(failure, "Run ID:") {
		t.Fatalf("failure comment = %q, want Run ID context", failure)
	}
}

func TestRunIssuePreservesWorkspaceOnFailureByDefault(t *testing.T) {
	gh := &fakeGitHub{issue: validIssue()}
	workspaces := t.TempDir()
	// Issue config left zero-valued so PreserveOnFailure is nil; the
	// runtime default should preserve the failed workspace.
	svc := &service{cfg: Config{WorkDir: workspaces}, log: zap.NewNop(), github: gh}

	prependFakeClaude(t, 1)

	if _, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo:        newRemoteRepo(t),
		IssueNumber: 42,
	}); err != nil {
		t.Fatalf("RunIssue() error = %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := os.ReadDir(workspaces)
	if err != nil {
		t.Fatalf("read workspaces: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected preserved workspace under default config, found none")
	}

	failure := gh.comments[len(gh.comments)-1]
	if !strings.Contains(failure, "Workspace preserved") {
		t.Fatalf("failure comment = %q, want preservation hint by default", failure)
	}
	for _, entry := range entries {
		if strings.Contains(failure, entry.Name()) {
			t.Fatalf("failure comment %q exposes workspace dir name %q", failure, entry.Name())
		}
	}
}

func TestRunIssueRemovesWorkspaceOnFailureWhenDisabled(t *testing.T) {
	gh := &fakeGitHub{issue: validIssue()}
	workspaces := t.TempDir()
	disabled := false
	svc := &service{
		cfg: Config{
			WorkDir: workspaces,
			Issue:   EventConfig{PreserveOnFailure: &disabled},
		},
		log:    zap.NewNop(),
		github: gh,
	}

	prependFakeClaude(t, 1)

	if _, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo:        newRemoteRepo(t),
		IssueNumber: 42,
	}); err != nil {
		t.Fatalf("RunIssue() error = %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := os.ReadDir(workspaces)
	if err != nil {
		t.Fatalf("read workspaces: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected workspace to be cleaned up when preserveOnFailure=false, got %d entries: %v",
			len(entries), entries)
	}

	failure := gh.comments[len(gh.comments)-1]
	if strings.Contains(failure, "Workspace preserved") {
		t.Fatalf("failure comment unexpectedly mentions preservation: %q", failure)
	}
}

func TestRunIssuePreservesWorkspaceOnFailureWhenExplicitlyEnabled(t *testing.T) {
	gh := &fakeGitHub{issue: validIssue()}
	workspaces := t.TempDir()
	enabled := true
	svc := &service{
		cfg: Config{
			WorkDir: workspaces,
			Issue:   EventConfig{PreserveOnFailure: &enabled},
		},
		log:    zap.NewNop(),
		github: gh,
	}

	prependFakeClaude(t, 1)

	if _, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo:        newRemoteRepo(t),
		IssueNumber: 42,
	}); err != nil {
		t.Fatalf("RunIssue() error = %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := os.ReadDir(workspaces)
	if err != nil {
		t.Fatalf("read workspaces: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected preserved workspace, found none")
	}

	failure := gh.comments[len(gh.comments)-1]
	if !strings.Contains(failure, "Workspace preserved") {
		t.Fatalf("failure comment = %q, want preservation hint", failure)
	}
}

func TestRunIssueRemovesWorkspaceOnSuccessByDefault(t *testing.T) {
	gh := &fakeGitHub{issue: validIssue()}
	workspaces := t.TempDir()
	// Default config (PreserveOnFailure nil → preserve on failure).
	// Successful runs must still clean up.
	svc := &service{cfg: Config{WorkDir: workspaces}, log: zap.NewNop(), github: gh}

	prependFakeClaude(t, 0)

	if _, err := svc.RunIssue(context.Background(), RunIssueRequest{
		Repo:        newRemoteRepo(t),
		IssueNumber: 42,
	}); err != nil {
		t.Fatalf("RunIssue() error = %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := os.ReadDir(workspaces)
	if err != nil {
		t.Fatalf("read workspaces: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected successful run to clean up workspace, got %d entries: %v",
			len(entries), entries)
	}
}

func TestPreserveOnFailureOrDefault(t *testing.T) {
	if got := (EventConfig{}).PreserveOnFailureOrDefault(); got != true {
		t.Fatalf("PreserveOnFailureOrDefault() with nil = %v, want true", got)
	}
	yes := true
	if got := (EventConfig{PreserveOnFailure: &yes}).PreserveOnFailureOrDefault(); got != true {
		t.Fatalf("PreserveOnFailureOrDefault() with *true = %v, want true", got)
	}
	no := false
	if got := (EventConfig{PreserveOnFailure: &no}).PreserveOnFailureOrDefault(); got != false {
		t.Fatalf("PreserveOnFailureOrDefault() with *false = %v, want false", got)
	}
}

func TestBuildIssueFailureCommentSanitizesPaths(t *testing.T) {
	report := issueFailureReport{
		runID: "01ABC",
		detail: "panic at /var/runner/workspaces/01ABC/main.go:10\n" +
			"hint: rerun with --workdir=/var/runner/workspaces",
		ws: workspaceOutcome{
			dir:       "/var/runner/workspaces/01ABC",
			preserved: true,
		},
	}

	body := buildIssueFailureComment(report, "/var/runner/workspaces")

	if strings.Contains(body, "/var/runner/workspaces/01ABC") {
		t.Fatalf("body still contains workspace path:\n%s", body)
	}
	if strings.Contains(body, "/var/runner/workspaces") {
		t.Fatalf("body still contains workspace root:\n%s", body)
	}
	for _, want := range []string{
		"failed to complete",
		"Run ID: `01ABC`",
		"Workspace preserved",
		"<workspace>",
		"<workspace-root>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestBuildIssueFailureCommentOmitsPreservationWhenNotPreserved(t *testing.T) {
	body := buildIssueFailureComment(issueFailureReport{
		runID:  "01ABC",
		detail: "boom",
	}, "")

	if strings.Contains(body, "Workspace preserved") {
		t.Fatalf("body unexpectedly mentions preservation:\n%s", body)
	}
	if !strings.Contains(body, "Run ID: `01ABC`") {
		t.Fatalf("body missing run id:\n%s", body)
	}
}

func TestRunPromptDoesNotTouchGitHub(t *testing.T) {
	workspaces := t.TempDir()
	gh := &fakeGitHub{}
	svc := &service{cfg: Config{WorkDir: workspaces}, log: zap.NewNop(), github: gh}

	prependFakeClaude(t, 0)

	result, err := svc.Run(context.Background(), RunRequest{
		Prompt: "Run tests",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Error != "" {
		t.Fatalf("result.Error = %q, want empty", result.Error)
	}
	if len(gh.added) != 0 || len(gh.removed) != 0 || len(gh.comments) != 0 {
		t.Fatalf("expected no GitHub calls for prompt event: %+v", gh)
	}
}

func TestRunRequiresPrompt(t *testing.T) {
	svc := &service{cfg: Config{}, log: zap.NewNop()}
	_, err := svc.Run(context.Background(), RunRequest{})
	if !errors.Is(err, ErrInvalidPrompt) {
		t.Fatalf("err = %v, want ErrInvalidPrompt", err)
	}
}

func TestBuildIssuePromptIncludesContext(t *testing.T) {
	prompt := buildIssuePrompt("owner/repo", validIssue())
	for _, want := range []string{
		"owner/repo",
		"#42",
		"Add feature X",
		"Please implement feature X",
		"open a pull request",
		"Do not merge any pull requests",
		"Run the relevant tests",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
