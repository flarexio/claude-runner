package runner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/oklog/ulid/v2"
	"go.uber.org/zap"
)

// RunIssue handles a GitHub issue task. It performs validation and the
// claim protocol synchronously (so the caller learns immediately if the
// request is malformed or the issue cannot be claimed), then kicks off
// Claude execution in a tracked background goroutine and returns an
// "accepted" Result. Final success/failure is reported as a comment on
// the issue itself.
//
// Use Service.Close to wait for in-flight background work — required for
// one-shot CLI invocations and graceful daemon shutdown.
func (svc *service) RunIssue(ctx context.Context, req RunIssueRequest) (*Result, error) {
	return svc.runIssueWorkflow(ctx, req)
}

// runIssueWorkflow is the high-level lifecycle for stateful issue tasks. It
// owns issue validation and claim behavior before launching the lower-level
// Claude execution step in the background.
func (svc *service) runIssueWorkflow(ctx context.Context, req RunIssueRequest) (*Result, error) {
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

	issue, err := svc.github.GetIssue(ctx, slug, req.IssueNumber)
	if err != nil {
		return nil, fmt.Errorf("fetch issue: %w", err)
	}
	if err := validateIssue(issue); err != nil {
		return nil, err
	}
	modelLabel := selectModelLabel(issue)
	selectedModel := svc.selectIssueModel(modelLabel)
	if err := svc.claimIssue(ctx, slug, req.IssueNumber, modelLabel, selectedModel); err != nil {
		return nil, fmt.Errorf("claim issue: %w", err)
	}

	runID := ulid.Make().String()

	// Translate to the internal RunRequest that execute consumes. Event is
	// set so claudeArgs picks up the issue-specific overrides and so
	// preparePrompt skips the PR trailer.
	exec := RunRequest{
		Prompt: buildIssuePrompt(slug, issue),
		Repo:   req.Repo,
		Ref:    req.Ref,
		Event:  EventIssue,
		Model:  selectedModel,
	}

	svc.launchBg(func(bgCtx context.Context) {
		log := svc.log.With(
			zap.String("repo", slug),
			zap.Int("issue_number", req.IssueNumber),
			zap.String("id", runID),
			zap.String("model_label", modelLabel),
			zap.String("model", selectedModel),
		)

		result, ws, runErr := svc.runIssueExecution(bgCtx, exec)
		if runErr != nil {
			log.Error("issue execution failed", zap.Error(runErr))
			svc.reportIssueFailure(bgCtx, slug, req.IssueNumber, issueFailureReport{
				runID:  runID,
				detail: runErr.Error(),
				ws:     ws,
			})
			return
		}
		if result.Error != "" {
			log.Warn("claude reported error", zap.String("error", result.Error))
			svc.reportIssueFailure(bgCtx, slug, req.IssueNumber, issueFailureReport{
				runID:  runID,
				detail: result.Error,
				ws:     ws,
			})
			return
		}
		log.Info("issue completed")
		svc.reportIssueSuccess(bgCtx, slug, req.IssueNumber, result.Output)
	})

	return &Result{
		ID:     runID,
		Output: fmt.Sprintf("Issue %s#%d accepted; claude-runner is processing in the background.", slug, req.IssueNumber),
	}, nil
}

func (svc *service) runIssueExecution(ctx context.Context, req RunRequest) (*Result, workspaceOutcome, error) {
	return svc.runClaudeInTemporaryWorkspace(ctx, req, runOptions{
		preserveOnFailure: svc.cfg.Issue.PreserveOnFailure,
	})
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
	var modelLabels []string
	for _, label := range KnownModelLabels {
		if issue.HasLabel(label) {
			modelLabels = append(modelLabels, label)
		}
	}
	if len(modelLabels) > 1 {
		return fmt.Errorf("%w: %s", ErrIssueMultipleModelLabels, strings.Join(modelLabels, ", "))
	}
	return nil
}

func selectModelLabel(issue *Issue) string {
	for _, label := range KnownModelLabels {
		if issue.HasLabel(label) {
			return label
		}
	}
	return ""
}

func (svc *service) selectIssueModel(modelLabel string) string {
	if modelLabel != "" && svc.cfg.Issue.ModelLabels != nil {
		if model := svc.cfg.Issue.ModelLabels[modelLabel]; model != "" {
			return model
		}
	}
	cfg := svc.eventConfig(EventIssue)
	return cfg.Model
}

func (svc *service) claimIssue(ctx context.Context, repo string, number int, modelLabel string, model string) error {
	if err := svc.github.RemoveLabel(ctx, repo, number, LabelAgentReady); err != nil {
		return fmt.Errorf("remove %s: %w", LabelAgentReady, err)
	}
	if err := svc.github.AddLabels(ctx, repo, number, []string{LabelClaimedByClaude}); err != nil {
		return fmt.Errorf("add %s: %w", LabelClaimedByClaude, err)
	}
	body := "claude-runner has claimed this issue and started working on it."
	if modelLabel != "" || model != "" {
		body += "\n\nModel selection:"
		if modelLabel != "" {
			body += fmt.Sprintf("\n- Label: `%s`", modelLabel)
		}
		if model != "" {
			body += fmt.Sprintf("\n- Model: `%s`", model)
		}
	}
	if err := svc.github.CreateComment(ctx, repo, number, body); err != nil {
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

// issueFailureReport carries the context reportIssueFailure needs to write
// an actionable, sanitized failure comment.
type issueFailureReport struct {
	// runID is the workflow-level run id used in server logs. Including
	// it in the comment lets an operator correlate the failure with the
	// runner's logs without exposing host paths.
	runID string
	// detail is the raw error / stderr from claude. It is sanitized
	// before being posted.
	detail string
	// ws describes the cloned workspace; ws.preserved is mentioned in
	// the comment when true so reporters know an inspectable copy lives
	// on the runner. The path itself is never posted publicly.
	ws workspaceOutcome
}

func (svc *service) reportIssueFailure(ctx context.Context, repo string, number int, report issueFailureReport) {
	if err := svc.github.AddLabels(ctx, repo, number, []string{LabelAgentFailed}); err != nil {
		svc.log.Warn("add agent-failed", zap.Error(err),
			zap.String("repo", repo), zap.Int("issue_number", number))
	}
	body := buildIssueFailureComment(report, svc.cfg.WorkDir)
	if err := svc.github.CreateComment(ctx, repo, number, body); err != nil {
		svc.log.Warn("comment failure", zap.Error(err),
			zap.String("repo", repo), zap.Int("issue_number", number))
	}
}

func buildIssueFailureComment(report issueFailureReport, workRoot string) string {
	var b strings.Builder
	b.WriteString("claude-runner failed to complete this task.\n\n")
	if report.runID != "" {
		fmt.Fprintf(&b, "- Run ID: `%s`\n", report.runID)
	}
	if report.ws.preserved {
		b.WriteString("- Workspace preserved on the runner; ask an operator to inspect with the run ID above.\n")
	}
	if report.runID != "" || report.ws.preserved {
		b.WriteString("\n")
	}
	b.WriteString(summarize(sanitizeFailureDetail(report.detail, workRoot, report.ws.dir)))
	return b.String()
}

// sanitizeFailureDetail strips host-local details we don't want to expose
// when posting failure comments on public issues. It is best-effort
// redaction (not a security boundary) and intentionally narrow: the
// workspace clone path and the runner's home directory.
func sanitizeFailureDetail(s, workRoot, workDir string) string {
	if workDir != "" {
		s = strings.ReplaceAll(s, workDir, "<workspace>")
	}
	if workRoot != "" {
		s = strings.ReplaceAll(s, workRoot, "<workspace-root>")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, home, "~")
	}
	return s
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
	b.WriteString("- Implement the task described in the issue and open a pull request for review.\n")
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
