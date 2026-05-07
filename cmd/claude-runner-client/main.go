package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/urfave/cli/v3"

	runner "github.com/flarexio/claude-runner"
)

func main() {
	cmd := &cli.Command{
		Name:  "claude-runner-client",
		Usage: "Client for Claude Code Runner",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "transport",
				Usage:   "Transport type (nats or http)",
				Value:   "nats",
				Sources: cli.EnvVars("TRANSPORT"),
			},
			&cli.StringFlag{
				Name:    "prompt",
				Usage:   "Prompt to send (required unless --event=issue)",
				Sources: cli.EnvVars("PROMPT"),
			},
			&cli.StringFlag{
				Name:    "repo",
				Usage:   "Repository URL",
				Sources: cli.EnvVars("REPO"),
			},
			&cli.StringFlag{
				Name:    "ref",
				Usage:   "Git ref (branch, tag)",
				Sources: cli.EnvVars("REF"),
			},
			&cli.StringFlag{
				Name:    "base-ref",
				Usage:   "Base git ref for pull request context",
				Sources: cli.EnvVars("BASE_REF"),
			},
			&cli.StringFlag{
				Name:    "event",
				Usage:   "Source event name, such as pull_request",
				Sources: cli.EnvVars("EVENT"),
			},
			&cli.IntFlag{
				Name:    "pr-number",
				Usage:   "Pull request number",
				Sources: cli.EnvVars("PR_NUMBER"),
			},
			&cli.IntFlag{
				Name:    "issue-number",
				Usage:   "Issue number (for event=issue)",
				Sources: cli.EnvVars("ISSUE_NUMBER"),
			},
			// NATS flags
			&cli.StringFlag{
				Name:    "nats-url",
				Usage:   "NATS server URL",
				Value:   "wss://nats.flarex.io",
				Sources: cli.EnvVars("NATS_URL"),
			},
			&cli.StringFlag{
				Name:    "nats-creds",
				Usage:   "Path to NATS credentials file",
				Sources: cli.EnvVars("NATS_CREDS"),
			},
			&cli.StringFlag{
				Name:    "edge-id",
				Usage:   "Edge node ID",
				Sources: cli.EnvVars("EDGE_ID"),
			},
			// HTTP flags
			&cli.StringFlag{
				Name:    "endpoint",
				Usage:   "HTTP endpoint URL",
				Sources: cli.EnvVars("ENDPOINT"),
			},
			// Output flags
			&cli.StringFlag{
				Name:    "output-file",
				Usage:   "Path to write Claude output to (relative paths are resolved under $GITHUB_WORKSPACE)",
				Sources: cli.EnvVars("OUTPUT_FILE"),
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
	event := cmd.String("event")

	var (
		payload any
		subject string
	)
	if event == runner.EventIssue {
		payload = runner.RunIssueRequest{
			Repo:        cmd.String("repo"),
			Ref:         cmd.String("ref"),
			IssueNumber: cmd.Int("issue-number"),
		}
		subject = "run-issue"
	} else {
		if cmd.String("prompt") == "" {
			return fmt.Errorf("--prompt is required unless --event=issue")
		}
		payload = runner.RunRequest{
			Prompt:   cmd.String("prompt"),
			Repo:     cmd.String("repo"),
			Ref:      cmd.String("ref"),
			BaseRef:  cmd.String("base-ref"),
			Event:    event,
			PRNumber: cmd.Int("pr-number"),
		}
		subject = "run"
	}

	transport := cmd.String("transport")
	outputFile := cmd.String("output-file")

	var (
		result *runner.Result
		err    error
	)

	switch transport {
	case "nats":
		result, err = requestNATS(cmd, payload, subject)
	case "http":
		result, err = requestHTTP(cmd, payload, subject)
	default:
		return fmt.Errorf("unsupported transport: %s", transport)
	}
	if err != nil {
		return err
	}

	return handleResult(result, outputFile)
}

func handleResult(result *runner.Result, outputFile string) error {
	if result.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", result.Error)
	}

	if result.Output != "" {
		fmt.Print(result.Output)
	}

	if outputFile != "" {
		if err := writeOutputFile(outputFile, result.Output); err != nil {
			fmt.Fprintf(os.Stderr, "write output file: %s\n", err)
		}
	}

	if result.Error != "" {
		return fmt.Errorf("remote claude execution failed")
	}

	return nil
}

func writeOutputFile(path, output string) error {
	resolved := resolveOutputPath(path)

	if dir := filepath.Dir(resolved); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create parent dir: %w", err)
		}
	}

	if err := os.WriteFile(resolved, []byte(output), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", resolved, err)
	}

	return nil
}

func resolveOutputPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}

	if workspace := os.Getenv("GITHUB_WORKSPACE"); workspace != "" {
		return filepath.Join(workspace, path)
	}

	return path
}

func requestNATS(cmd *cli.Command, payload any, subjectSuffix string) (*runner.Result, error) {
	natsURL := cmd.String("nats-url")
	natsCreds := cmd.String("nats-creds")
	edgeID := cmd.String("edge-id")

	if edgeID == "" {
		return nil, fmt.Errorf("--edge-id is required for NATS transport")
	}

	opts := []nats.Option{
		nats.Name("Claude Runner Client"),
	}

	if natsCreds != "" {
		opts = append(opts, nats.UserCredentials(natsCreds))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Drain()

	topic := "edges." + edgeID + ".claude-runner"

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	subject := topic + "." + subjectSuffix
	resp, err := nc.Request(subject, data, 10*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	var result runner.Result
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &result, nil
}

func requestHTTP(cmd *cli.Command, payload any, pathSuffix string) (*runner.Result, error) {
	endpoint := cmd.String("endpoint")
	if endpoint == "" {
		return nil, fmt.Errorf("--endpoint is required for HTTP transport")
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(endpoint, "/") + "/api/" + pathSuffix

	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result runner.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}
