package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"
	"go.uber.org/zap"
)

// IssueJob describes a fully-claimed GitHub issue task ready for Claude.
// The Issue must already be fetched, validated, and claimed (the claim
// comment posted, agent-ready removed, claimed-by-claude added).
// ClaimIssue performs all of that and returns a populated Issue.
type IssueJob struct {
	Repo     string      // "owner/repo" slug for GitHub API calls
	Issue    *Issue      // already-claimed issue
	CloneURL string      // git URL to clone (e.g. https://github.com/owner/repo.git)
	Ref      string      // optional branch to check out
	Cfg      EventConfig // tool / turn / bypass settings for `claude -p`
	WorkDir  string      // exact directory to clone into; created and removed by RunClaimedIssue
}

// ClaimIssue fetches an issue, validates it against the agent-task protocol,
// removes agent-ready, adds claimed-by-claude, and posts a claim comment.
// Returns the issue so callers don't need to re-fetch it.
//
// Both the daemon (Service.Run for event=issue) and the standalone
// claude-worker CLI use this; once it succeeds, the caller is responsible
// for either running RunClaimedIssue or otherwise unwinding the claim.
func ClaimIssue(ctx context.Context, gh GitHubClient, repo string, issueNumber int) (*Issue, error) {
	if gh == nil {
		return nil, ErrGitHubUnavailable
	}
	if issueNumber <= 0 {
		return nil, ErrInvalidIssueNumber
	}

	issue, err := gh.GetIssue(ctx, repo, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch issue: %w", err)
	}
	if err := validateIssue(issue); err != nil {
		return nil, err
	}

	if err := gh.RemoveLabel(ctx, repo, issueNumber, LabelAgentReady); err != nil {
		return nil, fmt.Errorf("remove %s: %w", LabelAgentReady, err)
	}
	if err := gh.AddLabels(ctx, repo, issueNumber, []string{LabelClaimedByClaude}); err != nil {
		return nil, fmt.Errorf("add %s: %w", LabelClaimedByClaude, err)
	}
	if err := gh.CreateComment(ctx, repo, issueNumber, "claude-runner has claimed this issue and started working on it."); err != nil {
		return nil, fmt.Errorf("comment claim: %w", err)
	}
	return issue, nil
}

// RunClaimedIssue executes Claude against a pre-claimed issue and posts the
// success/failure outcome to GitHub. It clones into job.WorkDir, runs
// claude, removes the workdir, then comments on the issue.
//
// Returns the Claude result; the returned error is non-nil only for setup
// failures (e.g. clone failure) where Claude never got to run. Once Claude
// runs, its outcome — including a non-zero exit — is posted to GitHub and
// the function returns nil error with the result populated.
func RunClaimedIssue(ctx context.Context, gh GitHubClient, log *zap.Logger, job IssueJob) (*Result, error) {
	if log == nil {
		log = zap.NewNop()
	}
	log = log.With(
		zap.String("repo", job.Repo),
		zap.Int("issue_number", job.Issue.Number),
	)

	if job.WorkDir == "" {
		return nil, fmt.Errorf("issue job missing WorkDir")
	}

	if err := cloneRepo(ctx, job.CloneURL, job.Ref, job.WorkDir, true); err != nil {
		log.Error("clone failed", zap.Error(err))
		reportIssueFailure(ctx, gh, log, job.Repo, job.Issue.Number, fmt.Sprintf("clone failed: %v", err))
		return nil, err
	}
	defer func() {
		if err := os.RemoveAll(job.WorkDir); err != nil {
			log.Warn("remove workspace", zap.String("work_dir", job.WorkDir), zap.Error(err))
		}
	}()

	prompt := buildIssuePrompt(job.Repo, job.Issue)
	args := claudeArgs(prompt, job.Cfg)

	result := runClaude(ctx, job.WorkDir, args)
	result.ID = ulid.Make().String()

	if result.Error != "" {
		log.Warn("claude reported error", zap.String("error", result.Error))
		reportIssueFailure(ctx, gh, log, job.Repo, job.Issue.Number, result.Error)
	} else {
		log.Info("claude completed", zap.String("id", result.ID))
		reportIssueSuccess(ctx, gh, log, job.Repo, job.Issue.Number, result.Output)
	}
	return result, nil
}

// runIssue is the daemon-side dispatch: validate + claim synchronously,
// then run claude in the background. Returns immediately once the claim
// comment is posted.
func (svc *service) runIssue(ctx context.Context, req Request) (*Result, error) {
	if svc.github == nil {
		return nil, ErrGitHubUnavailable
	}

	slug, err := NormalizeRepo(req.Repo)
	if err != nil {
		return nil, err
	}

	issue, err := ClaimIssue(ctx, svc.github, slug, req.IssueNumber)
	if err != nil {
		return nil, err
	}

	runID := ulid.Make().String()
	job := IssueJob{
		Repo:     slug,
		Issue:    issue,
		CloneURL: req.Repo,
		Ref:      req.Ref,
		Cfg:      svc.eventConfig(EventIssue),
		WorkDir:  filepath.Join(svc.cfg.WorkDir, runID),
	}

	gh := svc.github
	log := svc.log
	svc.launchBg(func(bgCtx context.Context) {
		if _, err := RunClaimedIssue(bgCtx, gh, log, job); err != nil {
			log.Error("issue background failed",
				zap.String("repo", slug),
				zap.Int("issue_number", req.IssueNumber),
				zap.Error(err))
		}
	})

	return &Result{
		ID:     runID,
		Output: fmt.Sprintf("Issue %s#%d accepted; claude-runner is processing in the background.", slug, req.IssueNumber),
	}, nil
}

func validateIssue(issue *Issue) error {
	if !issue.IsOpen() {
		return ErrIssueNotOpen
	}
	if !strings.Contains(issue.Body, IssueMarker) {
		return ErrIssueMarkerMissing
	}
	for _, label := range RequiredIssueLabels {
		if !issue.HasLabel(label) {
			return fmt.Errorf("%w: %s", ErrIssueLabelMissing, label)
		}
	}
	for _, label := range ExcludedIssueLabels {
		if issue.HasLabel(label) {
			return fmt.Errorf("%w: %s", ErrIssueLabelExcluded, label)
		}
	}
	return nil
}

func reportIssueSuccess(ctx context.Context, gh GitHubClient, log *zap.Logger, repo string, number int, output string) {
	body := "claude-runner finished this task.\n\n" + summarize(output)
	if err := gh.CreateComment(ctx, repo, number, body); err != nil {
		log.Warn("comment success", zap.Error(err))
	}
}

func reportIssueFailure(ctx context.Context, gh GitHubClient, log *zap.Logger, repo string, number int, errSummary string) {
	if err := gh.AddLabels(ctx, repo, number, []string{LabelAgentFailed}); err != nil {
		log.Warn("add agent-failed", zap.Error(err))
	}
	body := "claude-runner failed to complete this task.\n\n" + summarize(errSummary)
	if err := gh.CreateComment(ctx, repo, number, body); err != nil {
		log.Warn("comment failure", zap.Error(err))
	}
}

func buildIssuePrompt(repo string, issue *Issue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are claude-runner working on GitHub issue #%d in %s.\n\n", issue.Number, repo)
	if issue.Title != "" {
		fmt.Fprintf(&b, "Issue title: %s\n", issue.Title)
	}
	if issue.HTMLURL != "" {
		fmt.Fprintf(&b, "Issue URL: %s\n", issue.HTMLURL)
	}
	b.WriteString("\nIssue body:\n")
	b.WriteString(issue.Body)
	b.WriteString("\n\nInstructions:\n")
	b.WriteString("- Implement the task described in the issue.\n")
	b.WriteString("- Do not merge any pull requests.\n")
	b.WriteString("- Run the relevant tests after your changes and report which commands you ran and the outcomes in your final summary.\n")
	b.WriteString("- Keep changes scoped to what the issue asks for.\n")
	return b.String()
}

const summaryLimit = 4000

func summarize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(no output)"
	}
	if len(s) <= summaryLimit {
		return s
	}
	return s[:summaryLimit] + "\n\n[truncated]"
}
