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
workDir: ./workspaces
allowedTools:
  - Read
  - Edit
  - Bash
maxTurns: 10
```

### id

A plain text file containing the edge node ID.

### user.creds

NATS credentials file for authentication.

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

## API

### POST /api/run

Request:

```json
{
  "prompt": "Review this code for bugs",
  "repo": "git@github.com:user/repo.git",
  "ref": "main",
  "allowed_tools": ["Read", "Edit"],
  "max_turns": 10
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
