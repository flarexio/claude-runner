# claude-runner

A Go service that runs [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude -p`) remotely over NATS and HTTP transports. Built with [go-kit](https://gokit.io/) architecture.

## Architecture

```
Service → Middleware (logging) → Endpoint → Transport (NATS / HTTP)
```

## Features

- Execute Claude CLI prompts remotely via NATS or HTTP
- Auto-clone git repositories as working directories
- Configurable allowed tools and max turns
- NATS transport for edge nodes without public IPs
- HTTP transport with Gin framework

## Prerequisites

- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed and authenticated
- Go 1.25+

## Installation

```bash
# Server
go install github.com/flarexio/claude-runner/cmd/claude-runner@latest

# Client
go install github.com/flarexio/claude-runner/cmd/claude-runner-client@latest
```

## Configuration

Create config directory at `~/.flarex/claude-runner/` with the following files:

### config.yaml

```yaml
# workDir: ~/.flarexio/claude-runner/workspaces
allowedTools:
- Read
- Glob
- Grep
- Bash
maxTurns: 10
```

`workDir` is optional. Defaults to `~/.flarex/claude-runner/workspaces`.

### id

A plain text file containing the edge node ID.

### user.creds

NATS credentials file for authentication.

## Docker

### NATS

```bash
docker run -d \
  -v ~/.claude:/root/.claude \
  -v ~/.flarex/claude-runner:/root/.flarex/claude-runner \
  flarexio/claude-runner
```

### HTTP

```bash
docker run -d \
  -v ~/.claude:/root/.claude \
  -v ~/.flarex/claude-runner:/root/.flarex/claude-runner \
  -p 8080:8080 \
  flarexio/claude-runner --http
```

## Usage

### Server

```bash
# Start with NATS transport (default)
claude-runner

# Enable HTTP transport
claude-runner --http
```

### Client

#### HTTP

```bash
claude-runner-client \
  --transport http \
  --endpoint http://localhost:8080 \
  --prompt "Review this code for bugs"
```

#### NATS

```bash
claude-runner-client \
  --edge-id <edge-node-id> \
  --prompt "Review this code for bugs"
```

#### With git repository

```bash
claude-runner-client \
  --transport http \
  --endpoint http://localhost:8080 \
  --prompt "Review this code" \
  --repo git@github.com:user/repo.git
```

## GitHub Action

Use as a step in your workflow to send prompts to a running claude-runner instance:

```yaml
- uses: flarexio/claude-runner@v1.0.2
  with:
    prompt: "Review this code for bugs"
    repo: ${{ github.server_url }}/${{ github.repository }}.git
    ref: ${{ github.head_ref }}
    edge-id: <your-edge-id>
    nats-creds-content: ${{ secrets.NATS_CREDS }}
```

Add `NATS_CREDS` (content of `user.creds`) to your repository's **Settings → Secrets → Actions**.

For pull request reviews, generate the diff in the workflow and pass it to
claude-runner so the remote runner does not have to infer the base branch from a
shallow clone:

```yaml
- uses: actions/checkout@v4
  with:
    fetch-depth: 0

- name: Generate PR diff
  if: github.event_name == 'pull_request'
  run: |
    mkdir -p .claude-runner
    git fetch origin ${{ github.base_ref }} --depth=1
    git diff origin/${{ github.base_ref }}...HEAD > .claude-runner/pr.diff

- uses: flarexio/claude-runner@v1.0.2
  with:
    prompt: "Review changed files for bugs"
    repo: ${{ github.server_url }}/${{ github.repository }}.git
    ref: ${{ github.head_ref || github.ref_name }}
    base-ref: ${{ github.base_ref }}
    event: ${{ github.event_name }}
    pr-number: ${{ github.event.pull_request.number }}
    diff-file: .claude-runner/pr.diff
    edge-id: <your-edge-id>
    nats-creds-content: ${{ secrets.NATS_CREDS }}
```

## API

### POST /api/run

Request:

```json
{
  "prompt": "Review this code for bugs",
  "repo": "git@github.com:user/repo.git",
  "ref": "feature/my-change",
  "base_ref": "main",
  "event": "pull_request",
  "pr_number": 2,
  "diff": "diff --git a/example.go b/example.go\n..."
}
```

Response:

```json
{
  "id": "01JNXYZ...",
  "output": "...",
  "error": ""
}
```

## License

MIT
