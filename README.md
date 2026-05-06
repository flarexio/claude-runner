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
# workDir: ~/.flarex/claude-runner/workspaces
allowedTools:
- Read
- Glob
- Grep
- Bash
maxTurns: 10
```

`workDir` is optional. Defaults to `~/.flarex/claude-runner/workspaces`.
It is a server-side setting and cannot be overridden by client requests.

## Execution Modes

claude-runner supports two workspace modes.

### Existing Workspace Mode

When `repo` is omitted, claude-runner runs `claude -p` directly in the
configured `workDir`. Use this when the runner is tied to an existing local
checkout on the server.

### CI Mode

When `repo` is provided, claude-runner treats the request as a CI job. It clones
the requested repository into `workDir/<run-id>`, optionally checks out `ref`,
generates pull request diff context when `base-ref` is provided, runs
`claude -p`, then removes the temporary clone.

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

#### Persisting output to a file

```bash
claude-runner-client \
  --edge-id <edge-node-id> \
  --prompt "Review this code" \
  --output-file claude-output.md
```

`--output-file` writes Claude's output to disk in addition to stdout. Relative
paths are resolved under `$GITHUB_WORKSPACE` when set (otherwise the current
directory), and parent directories are created automatically. The file is
written even if the remote returns an error, and the client still exits
non-zero in that case.

## GitHub Action

Use as a step in your workflow to send prompts to a running claude-runner instance:

```yaml
- uses: flarexio/claude-runner@v1
  with:
    prompt: "Review this code for bugs"
    repo: ${{ github.server_url }}/${{ github.repository }}.git
    ref: ${{ github.head_ref || github.ref_name }}
    base-ref: ${{ github.base_ref }}
    event: ${{ github.event_name }}
    pr-number: ${{ github.event.pull_request.number || '' }}
    edge-id: <your-edge-id>
    nats-creds-content: ${{ secrets.NATS_CREDS }}
    output-file: claude-output.md
```

Add `NATS_CREDS` (content of `user.creds`) to your repository's **Settings → Secrets → Actions**.

When `base-ref` is present, claude-runner generates a PR diff in the remote
workspace and uses that diff as Claude's review scope. When `base-ref` is empty,
claude-runner runs the prompt against the cloned `ref` without diff context.

`output-file` is optional. When set, the client writes Claude's output to that
path in addition to stdout. Relative paths are resolved under
`$GITHUB_WORKSPACE`, and parent directories are created automatically. Use this
to forward the result to `$GITHUB_STEP_SUMMARY`, attach it as an artifact, or
post it as a PR comment in a follow-up step:

```yaml
- uses: flarexio/claude-runner@v1
  with:
    prompt: "Review this code"
    output-file: claude-output.md
    # ...
- run: cat claude-output.md >> "$GITHUB_STEP_SUMMARY"
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
  "pr_number": 2
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
