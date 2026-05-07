package runner

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

func (svc *service) runIssue(ctx context.Context, req Request) (*Result, error) {
	if svc.github == nil {
		return nil, ErrGitHubUnavailable
	}

	slug, err := NormalizeRepo(req.Repo)
	if err != nil {
		return nil, err
	}
	if req.IssueNumber <= 0 {
		return nil, ErrInvalidIssueNumber
	}

	log := svc.log.With(
		zap.String("repo", slug),
		zap.Int("issue_number", req.IssueNumber),
	)

	issue, err := svc.github.GetIssue(ctx, slug, req.IssueNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch issue: %w", err)
	}

	if err := validateIssue(issue); err != nil {
		return nil, err
	}

	if err := svc.claimIssue(ctx, slug, req.IssueNumber); err != nil {
		return nil, fmt.Errorf("claim issue: %w", err)
	}

	exec := req
	exec.Prompt = buildIssuePrompt(slug, issue)

	result, runErr := svc.execute(ctx, exec)
	if runErr != nil {
		log.Error("execute failed", zap.Error(runErr))
		svc.reportIssueFailure(ctx, slug, req.IssueNumber, runErr.Error())
		return nil, runErr
	}

	if result.Error != "" {
		svc.reportIssueFailure(ctx, slug, req.IssueNumber, result.Error)
	} else {
		svc.reportIssueSuccess(ctx, slug, req.IssueNumber, result.Output)
	}

	return result, nil
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

func (svc *service) claimIssue(ctx context.Context, repo string, number int) error {
	if err := svc.github.RemoveLabel(ctx, repo, number, LabelAgentReady); err != nil {
		return fmt.Errorf("remove %s: %w", LabelAgentReady, err)
	}
	if err := svc.github.AddLabels(ctx, repo, number, []string{LabelClaimedByClaude}); err != nil {
		return fmt.Errorf("add %s: %w", LabelClaimedByClaude, err)
	}
	if err := svc.github.CreateComment(ctx, repo, number, "claude-runner has claimed this issue and started working on it."); err != nil {
		return fmt.Errorf("comment claim: %w", err)
	}
	return nil
}

func (svc *service) reportIssueSuccess(ctx context.Context, repo string, number int, output string) {
	body := "claude-runner finished this task.\n\n" + summarize(output)
	if err := svc.github.CreateComment(ctx, repo, number, body); err != nil {
		svc.log.Warn("comment success", zap.Error(err),
			zap.String("repo", repo), zap.Int("issue_number", number))
	}
}

func (svc *service) reportIssueFailure(ctx context.Context, repo string, number int, errSummary string) {
	if err := svc.github.AddLabels(ctx, repo, number, []string{LabelAgentFailed}); err != nil {
		svc.log.Warn("add agent-failed", zap.Error(err),
			zap.String("repo", repo), zap.Int("issue_number", number))
	}
	body := "claude-runner failed to complete this task.\n\n" + summarize(errSummary)
	if err := svc.github.CreateComment(ctx, repo, number, body); err != nil {
		svc.log.Warn("comment failure", zap.Error(err),
			zap.String("repo", repo), zap.Int("issue_number", number))
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
