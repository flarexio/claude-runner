package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
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
				Name:     "transport",
				Usage:    "Transport type (nats or http)",
				Value:    "nats",
				Sources:  cli.EnvVars("TRANSPORT"),
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
		return runNATS(ctx, cmd, req)
	case "http":
		return runHTTP(ctx, cmd, req)
	default:
		return fmt.Errorf("unsupported transport: %s", transport)
	}
}

func runNATS(ctx context.Context, cmd *cli.Command, req runner.Request) error {
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

	// Subscribe for result before submitting
	resultCh := make(chan *runner.Result, 1)
	sub, err := nc.Subscribe(topic+".results.*", func(msg *nats.Msg) {
		var result runner.Result
		if err := json.Unmarshal(msg.Data, &result); err != nil {
			return
		}
		resultCh <- &result
	})
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	defer sub.Unsubscribe()

	// Submit async run
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}

	resp, err := nc.Request(topic+".async-run", data, 30*time.Second)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}

	var asyncResp runner.AsyncRunResponse
	if err := json.Unmarshal(resp.Data, &asyncResp); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Submitted: %s\n", asyncResp.ID)

	// Wait for result
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case result := <-resultCh:
		if result.Error != "" {
			fmt.Fprintf(os.Stderr, "Error: %s\n", result.Error)
		}
		fmt.Print(result.Output)
	case sign := <-quit:
		return fmt.Errorf("interrupted: %s", sign)
	}

	return nil
}

func runHTTP(ctx context.Context, cmd *cli.Command, req runner.Request) error {
	endpoint := cmd.String("endpoint")
	if endpoint == "" {
		return fmt.Errorf("--endpoint is required for HTTP transport")
	}

	data, err := json.Marshal(req)
	if err != nil {
		return err
	}

	url := strings.TrimRight(endpoint, "/") + "/api/async-run"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
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

	// Parse SSE stream
	scanner := bufio.NewScanner(resp.Body)
	var event string

	for scanner.Scan() {
		line := scanner.Text()

		if val, ok := strings.CutPrefix(line, "event: "); ok {
			event = val
			continue
		}

		if data, ok := strings.CutPrefix(line, "data: "); ok {

			switch event {
			case "submitted":
				var asyncResp runner.AsyncRunResponse
				if err := json.Unmarshal([]byte(data), &asyncResp); err == nil {
					fmt.Fprintf(os.Stderr, "Submitted: %s\n", asyncResp.ID)
				}
			case "result":
				var result runner.Result
				if err := json.Unmarshal([]byte(data), &result); err == nil {
					if result.Error != "" {
						fmt.Fprintf(os.Stderr, "Error: %s\n", result.Error)
					}
					fmt.Print(result.Output)
				}
			case "error":
				return fmt.Errorf("server error: %s", data)
			}

			event = ""
		}
	}

	return scanner.Err()
}
