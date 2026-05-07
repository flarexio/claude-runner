package runner

import (
	"context"
	"errors"
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

func TestRunIssueRequiresGitHubClient(t *testing.T) {
	svc := &service{cfg: Config{}, log: zap.NewNop()}
	_, err := svc.RunIssue(context.Background(), Request{
		Repo:        "owner/repo",
		IssueNumber: 1,
	})
	if !errors.Is(err, ErrGitHubUnavailable) {
		t.Fatalf("err = %v, want ErrGitHubUnavailable", err)
	}
}

func TestRunIssueRequiresIssueNumber(t *testing.T) {
	svc := &service{cfg: Config{}, log: zap.NewNop(), github: &fakeGitHub{}}
	_, err := svc.RunIssue(context.Background(), Request{
		Repo: "owner/repo",
	})
	if !errors.Is(err, ErrInvalidIssueNumber) {
		t.Fatalf("err = %v, want ErrInvalidIssueNumber", err)
	}
}

func TestRunIssueRejectsBadRepo(t *testing.T) {
	svc := &service{cfg: Config{}, log: zap.NewNop(), github: &fakeGitHub{}}
	_, err := svc.RunIssue(context.Background(), Request{
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

	_, err := svc.RunIssue(context.Background(), Request{
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

	result, err := svc.RunIssue(context.Background(), Request{
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

	if _, err := svc.RunIssue(context.Background(), Request{
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
}

func TestRunPromptDoesNotTouchGitHub(t *testing.T) {
	workspaces := t.TempDir()
	gh := &fakeGitHub{}
	svc := &service{cfg: Config{WorkDir: workspaces}, log: zap.NewNop(), github: gh}

	prependFakeClaude(t, 0)

	result, err := svc.Run(context.Background(), Request{
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
	_, err := svc.Run(context.Background(), Request{})
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
		"Do not merge any pull requests",
		"Run the relevant tests",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}


