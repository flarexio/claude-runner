package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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
				Name:     "prompt",
				Usage:    "Prompt to send",
				Required: true,
				Sources:  cli.EnvVars("PROMPT"),
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
		},
		Action: run,
	}

	err := cmd.Run(context.Background(), os.Args)
	if err != nil {
		log.Fatal(err.Error())
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	req := runner.Request{
		Prompt: cmd.String("prompt"),
		Repo:   cmd.String("repo"),
		Ref:    cmd.String("ref"),
	}

	transport := cmd.String("transport")

	switch transport {
	case "nats":
		return runNATS(cmd, req)
	case "http":
		return runHTTP(cmd, req)
	default:
		return fmt.Errorf("unsupported transport: %s", transport)
	}
}

func runNATS(cmd *cli.Command, req runner.Request) error {
	natsURL := cmd.String("nats-url")
	natsCreds := cmd.String("nats-creds")
	edgeID := cmd.String("edge-id")

	if edgeID == "" {
		return fmt.Errorf("--edge-id is required for NATS transport")
	}

	opts := []nats.Option{
		nats.Name("Claude Runner Client"),
	}

	if natsCreds != "" {
		opts = append(opts, nats.UserCredentials(natsCreds))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Drain()

	topic := "edges." + edgeID + ".claude-runner"

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}

	resp, err := nc.Request(topic+".run", data, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}

	var result runner.Result
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	if result.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", result.Error)
	}
	fmt.Print(result.Output)

	return nil
}

func runHTTP(cmd *cli.Command, req runner.Request) error {
	endpoint := cmd.String("endpoint")
	if endpoint == "" {
		return fmt.Errorf("--endpoint is required for HTTP transport")
	}

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}

	url := strings.TrimRight(endpoint, "/") + "/api/run"

	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result runner.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if result.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", result.Error)
	}
	fmt.Print(result.Output)

	return nil
}
