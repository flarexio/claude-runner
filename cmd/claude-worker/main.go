package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"
	"github.com/urfave/cli/v3"
	"go.uber.org/zap"

	runner "github.com/flarexio/claude-runner"
)

func main() {
	cmd := &cli.Command{
		Name:  "claude-worker",
		Usage: "Run Claude on a GitHub issue task (validate, claim, execute, post results)",
		Description: "claude-worker is a one-shot tool that performs the full GitHub issue " +
			"agent-task protocol: fetch + validate the issue, claim it, run claude in a " +
			"temporary clone, and post a success or failure comment back to the issue.\n\n" +
			"It is designed to be invoked directly by AI agents or by claude-runner; both " +
			"call the same runner.RunClaimedIssue primitive.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "repo",
				Usage:    "Repository (owner/repo or clone URL)",
				Required: true,
				Sources:  cli.EnvVars("REPO"),
			},
			&cli.IntFlag{
				Name:     "issue-number",
				Usage:    "Issue number",
				Required: true,
				Sources:  cli.EnvVars("ISSUE_NUMBER"),
			},
			&cli.StringFlag{
				Name:    "clone-url",
				Usage:   "git clone URL (defaults to https://github.com/<owner>/<repo>.git when --repo is in slug form)",
				Sources: cli.EnvVars("CLONE_URL"),
			},
			&cli.StringFlag{
				Name:    "ref",
				Usage:   "git branch to check out (optional)",
				Sources: cli.EnvVars("REF"),
			},
			&cli.StringFlag{
				Name:     "github-token",
				Usage:    "GitHub token with issues:write and contents:read",
				Sources:  cli.EnvVars("GITHUB_TOKEN"),
				Required: true,
			},
			&cli.StringFlag{
				Name:    "github-base-url",
				Usage:   "GitHub API base URL (defaults to https://api.github.com)",
				Sources: cli.EnvVars("GITHUB_API_URL"),
			},
			&cli.StringFlag{
				Name:    "workspace",
				Usage:   "Base directory for clones; a subdir is created per run",
				Value:   filepath.Join(os.TempDir(), "claude-worker"),
				Sources: cli.EnvVars("WORKSPACE"),
			},
			&cli.BoolFlag{
				Name:    "bypass-permissions",
				Usage:   "Pass --dangerously-skip-permissions to claude (no per-tool prompts)",
				Sources: cli.EnvVars("BYPASS_PERMISSIONS"),
			},
			&cli.StringSliceFlag{
				Name:    "allowed-tools",
				Usage:   "Allowed tool names (ignored when --bypass-permissions is set)",
				Sources: cli.EnvVars("ALLOWED_TOOLS"),
			},
			&cli.IntFlag{
				Name:    "max-turns",
				Usage:   "Maximum turns for claude",
				Value:   30,
				Sources: cli.EnvVars("MAX_TURNS"),
			},
		},
		Action: run,
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err.Error())
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	logger, err := zap.NewDevelopment()
	if err != nil {
		return err
	}
	defer logger.Sync()

	repoIn := cmd.String("repo")
	slug, err := runner.NormalizeRepo(repoIn)
	if err != nil {
		return fmt.Errorf("repo: %w", err)
	}

	cloneURL := cmd.String("clone-url")
	if cloneURL == "" {
		if strings.Contains(repoIn, "://") || strings.HasPrefix(repoIn, "git@") {
			cloneURL = repoIn
		} else {
			cloneURL = fmt.Sprintf("https://github.com/%s.git", slug)
		}
	}

	gh := runner.NewGitHubClient(runner.GitHubConfig{
		Token:   cmd.String("github-token"),
		BaseURL: cmd.String("github-base-url"),
	})

	issueNumber := cmd.Int("issue-number")

	logger.Info("claiming issue", zap.String("repo", slug), zap.Int("issue_number", issueNumber))
	issue, err := runner.ClaimIssue(ctx, gh, slug, issueNumber)
	if err != nil {
		return fmt.Errorf("claim issue: %w", err)
	}

	runID := ulid.Make().String()
	workDir := filepath.Join(cmd.String("workspace"), runID)

	if err := os.MkdirAll(cmd.String("workspace"), 0o755); err != nil {
		return fmt.Errorf("create workspace root: %w", err)
	}

	job := runner.IssueJob{
		Repo:     slug,
		Issue:    issue,
		CloneURL: cloneURL,
		Ref:      cmd.String("ref"),
		Cfg: runner.EventConfig{
			AllowedTools:      cmd.StringSlice("allowed-tools"),
			MaxTurns:          cmd.Int("max-turns"),
			BypassPermissions: cmd.Bool("bypass-permissions"),
		},
		WorkDir: workDir,
	}

	result, err := runner.RunClaimedIssue(ctx, gh, logger, job)
	if err != nil {
		return err
	}

	if result.Output != "" {
		fmt.Println(result.Output)
	}
	if result.Error != "" {
		return fmt.Errorf("claude failed: %s", result.Error)
	}
	return nil
}
