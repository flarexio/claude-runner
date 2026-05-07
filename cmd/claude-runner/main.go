package main

import (
	"context"
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
		Action: run,
	}

	err := cmd.Run(context.Background(), os.Args)
	if err != nil {
		log.Fatal(err.Error())
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	path := cmd.String("path")
	if path == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return err
		}

		path = filepath.Join(homeDir, ".flarex", "claude-runner")
	}

	logger, err := zap.NewDevelopment()
	if err != nil {
		return err
	}
	defer logger.Sync()

	zap.ReplaceGlobals(logger)

	f, err := os.Open(filepath.Join(path, "config.yaml"))
	if err != nil {
		return err
	}
	defer f.Close()

	var cfg runner.Config
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return err
	}

	if cfg.WorkDir == "" {
		cfg.WorkDir = filepath.Join(path, "workspaces")
	}

	if envToken := os.Getenv("GITHUB_TOKEN"); envToken != "" && cfg.GitHub.Token == "" {
		cfg.GitHub.Token = envToken
	}

	svc, err := runner.NewService(cfg)
	if err != nil {
		return err
	}
	defer svc.Close()

	svc = runner.LoggingMiddleware(logger)(svc)

	endpoints := runner.EndpointSet{
		Run: runner.RunEndpoint(svc),
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
