package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"
	"github.com/urfave/cli/v3"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	runner "github.com/flarexio/claude-runner"
	httpT "github.com/flarexio/claude-runner/transport/http"
	natsT "github.com/flarexio/claude-runner/transport/nats"
)

func main() {
	cmd := &cli.Command{
		Name:  "claude-runner",
		Usage: "Claude Code Runner over HTTP/NATS",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "path",
				Usage: "Path to the config directory",
			},
			&cli.StringFlag{
				Name:    "nats",
				Usage:   "NATS server URL",
				Value:   "wss://nats.flarex.io",
				Sources: cli.EnvVars("NATS_URL"),
			},
			&cli.BoolFlag{
				Name:  "http",
				Usage: "Enable HTTP transport",
				Value: false,
			},
			&cli.StringFlag{
				Name:  "http-addr",
				Usage: "HTTP server address",
				Value: ":8080",
			},
		},
		Action: serve,
		Commands: []*cli.Command{
			{
				Name:  "run-issue",
				Usage: "Run Claude on a GitHub issue task (validate, claim, execute, post results)",
				Description: "One-shot subcommand that performs the full GitHub issue agent-task " +
					"protocol: claim the issue, run claude in a temporary clone, post a success " +
					"or failure comment back to the issue. Reuses the same Service the daemon " +
					"uses; intended to be invocable directly by AI agents as a skill.",
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
						Name:    "ref",
						Usage:   "Optional git branch to check out",
						Sources: cli.EnvVars("REF"),
					},
					&cli.StringFlag{
						Name:    "github-token",
						Usage:   "GitHub token (overrides config; env: GITHUB_TOKEN)",
						Sources: cli.EnvVars("GITHUB_TOKEN"),
					},
				},
				Action: runIssue,
			},
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err.Error())
	}
}

// loadConfig reads config.yaml from the resolved config dir, applies defaults,
// and overrides cfg.GitHub.Token with $GITHUB_TOKEN when set.
func loadConfig(cmd *cli.Command) (runner.Config, string, error) {
	path := cmd.String("path")
	if path == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return runner.Config{}, "", err
		}
		path = filepath.Join(homeDir, ".flarex", "claude-runner")
	}

	f, err := os.Open(filepath.Join(path, "config.yaml"))
	if err != nil {
		return runner.Config{}, "", err
	}
	defer f.Close()

	var cfg runner.Config
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return runner.Config{}, "", err
	}

	if cfg.WorkDir == "" {
		cfg.WorkDir = filepath.Join(path, "workspaces")
	}
	if envToken := os.Getenv("GITHUB_TOKEN"); envToken != "" && cfg.GitHub.Token == "" {
		cfg.GitHub.Token = envToken
	}

	return cfg, path, nil
}

func serve(ctx context.Context, cmd *cli.Command) error {
	logger, err := zap.NewDevelopment()
	if err != nil {
		return err
	}
	defer logger.Sync()
	zap.ReplaceGlobals(logger)

	cfg, path, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	svc, err := runner.NewService(cfg)
	if err != nil {
		return err
	}
	defer svc.Close()

	svc = runner.LoggingMiddleware(logger)(svc)

	endpoints := runner.EndpointSet{
		Run:      runner.RunEndpoint(svc),
		RunIssue: runner.RunIssueEndpoint(svc),
	}

	// NATS Transport
	idBytes, err := os.ReadFile(filepath.Join(path, "id"))
	if err == nil {
		edgeID := strings.TrimSpace(string(idBytes))
		natsURL := cmd.String("nats")
		natsCreds := filepath.Join(path, "user.creds")

		nc, err := nats.Connect(natsURL,
			nats.Name("Claude Runner - "+edgeID),
			nats.UserCredentials(natsCreds),
		)
		if err != nil {
			return err
		}
		defer nc.Drain()

		srv, err := micro.AddService(nc, micro.Config{
			Name:    "claude-runner",
			Version: "1.0.0",
		})
		if err != nil {
			return err
		}
		defer srv.Stop()

		topic := "edges." + edgeID + ".claude-runner"

		root := srv.AddGroup(topic)
		natsT.AddEndpoints(root, endpoints)

		logger.Info("NATS transport enabled", zap.String("edge_id", edgeID))
	}

	// HTTP Transport
	httpEnabled := cmd.Bool("http")
	if httpEnabled {
		r := gin.Default()
		httpT.AddRouters(r, endpoints)

		httpAddr := cmd.String("http-addr")
		go r.Run(httpAddr)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	sign := <-quit

	logger.Info("graceful shutdown", zap.String("signal", sign.String()))
	return nil
}

func runIssue(ctx context.Context, cmd *cli.Command) error {
	logger, err := zap.NewDevelopment()
	if err != nil {
		return err
	}
	defer logger.Sync()
	zap.ReplaceGlobals(logger)

	cfg, _, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	if token := cmd.String("github-token"); token != "" {
		cfg.GitHub.Token = token
	}
	if cfg.GitHub.Token == "" {
		return fmt.Errorf("github token is required (config github.token, GITHUB_TOKEN env, or --github-token)")
	}

	svc, err := runner.NewService(cfg)
	if err != nil {
		return err
	}
	svc = runner.LoggingMiddleware(logger)(svc)

	req := runner.RunIssueRequest{
		Repo:        cmd.String("repo"),
		Ref:         cmd.String("ref"),
		IssueNumber: cmd.Int("issue-number"),
	}

	result, err := svc.RunIssue(ctx, req)
	if err != nil {
		// Close still drains in case anything got launched, but for an error
		// before launchBg the WaitGroup should be empty.
		_ = svc.Close()
		return err
	}

	logger.Info("issue accepted; waiting for background completion", zap.String("id", result.ID))

	// Block until the background goroutine kicked off by RunIssue finishes.
	// Final outcome is posted as a comment on the issue itself.
	if err := svc.Close(); err != nil {
		return err
	}

	if result.Output != "" {
		fmt.Println(result.Output)
	}
	return nil
}
