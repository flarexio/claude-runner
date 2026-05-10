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

// RunIssue validates and claims an issue synchronously, then runs Claude in
// the background. Final success/failure is posted as a comment on the issue.
// Callers must Service.Close to wait for the background goroutine to finish.
func (svc *service) RunIssue(ctx context.Context, req RunIssueRequest) (*Result, error) {
	return svc.runIssueWorkflow(ctx, req)
}

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

	// Event=EventIssue routes buildArgs to the issue overrides and tells
	// preparePrompt to skip the PR trailer.
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

		result, ws, runErr := svc.runIssueExecution(bgCtx, exec, slug, req.IssueNumber)
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

// runIssueExecution owns the issue lifecycle: stable taskRoot/repo layout,
// preserve-on-failure when configured, and a clean removal otherwise. The
// task root is reserved for sibling runner metadata (.claude-runner/, future).
func (svc *service) runIssueExecution(ctx context.Context, req RunRequest, slug string, issueNumber int) (*Result, workspaceOutcome, error) {
	runID := ulid.Make().String()
	taskRoot := filepath.Join(svc.cfg.WorkDir, issueTaskID(slug, issueNumber))
	workDir := filepath.Join(taskRoot, "repo")

	if err := svc.prepareWorkDir(ctx, req, workDir); err != nil {
		return nil, workspaceOutcome{}, err
	}

	ws := workspaceOutcome{dir: taskRoot}
	defer func() {
		if ws.preserved {
			svc.log.Info("preserving failed issue workspace",
				zap.String("task_root", taskRoot),
				zap.String("id", runID))
			return
		}
		if err := os.RemoveAll(taskRoot); err != nil {
			svc.log.Warn("remove workspace", zap.String("task_root", taskRoot), zap.Error(err))
		}
	}()

	result, claudeFailed, err := svc.execClaude(ctx, req, workDir, runID)
	if err != nil {
		return nil, ws, err
	}
	if claudeFailed && svc.cfg.Issue.PreserveOnFailure {
		ws.preserved = true
	}
	return result, ws, nil
}

// issueTaskID returns the stable workspace key for an issue task.
// slug must be the normalized "owner/repo" form.
func issueTaskID(slug string, number int) string {
	parts := strings.SplitN(slug, "/", 2)
	return fmt.Sprintf("gh-issue-%s-%s-%d", parts[0], parts[1], number)
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

type issueFailureReport struct {
	runID  string // included in the comment for log correlation
	detail string // raw stderr; sanitized before posting
	ws     workspaceOutcome
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

// sanitizeFailureDetail is best-effort redaction (not a security boundary):
// strip workspace paths and the runner's home dir before posting publicly.
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
