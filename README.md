# claude-runner

A Go service that runs [Claude Code](https://docs.anthropic.com/en/docs/claude-code) (`claude -p`) remotely over NATS and HTTP transports.

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
- Event-aware routing: plain prompts, pull request reviews, and GitHub issue tasks

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
issue:
  bypassPermissions: true
  maxTurns: 30
```

`workDir` is optional. Defaults to `~/.flarex/claude-runner/workspaces`.
It is a server-side setting and cannot be overridden by client requests.

`issue.allowedTools`, `issue.maxTurns`, and `issue.bypassPermissions` only
apply when `event: issue`. Empty fields fall back to the top-level values.

`issue.bypassPermissions: true` (shown above) passes
`--dangerously-skip-permissions` to `claude` and ignores `allowedTools`,
mirroring how Claude Code is normally used in interactive development. In
issue mode no human is on the call path, so only enable bypass when the
trigger is gated to trusted members (label gate plus the
`author_association` check in the [Issue Mode action example](#issue-mode))
and the runner's GitHub token is restricted to non-destructive operations.

If you prefer a curated tool list, set `bypassPermissions: false` and
spell out `issue.allowedTools` (Issue mode needs `Edit` and `Write` to
implement tasks, which the top-level fallback list does not include).

## Execution Modes

claude-runner exposes two operations on its Service: `Run` (prompt / PR
review, synchronous) and `RunIssue` (GitHub issue task, async after claim).
Each is a 1:1 endpoint — no event dispatch happens at the endpoint or
transport layer; the client picks which endpoint to call.

| Mode | Endpoint | NATS subject | Behavior |
| --- | --- | --- | --- |
| Existing workspace / CI / PR review | `POST /api/run` | `<topic>.run` | Synchronous |
| GitHub issue | `POST /api/run-issue` | `<topic>.run-issue` | Sync claim → goroutine for execute → returns `accepted` |

### Existing Workspace Mode

When `repo` is omitted, claude-runner runs `claude -p` directly in the
configured `workDir`. Use this when the runner is tied to an existing local
checkout on the server.

### CI Mode

When `repo` is provided, claude-runner treats the request as a CI job. It
clones the requested repository into `workDir/<run-id>`, optionally checks
out `ref`, generates pull request diff context when `base-ref` is provided,
runs `claude -p`, then removes the temporary clone.

### Issue Mode

For issue tasks the daemon splits the work into a synchronous claim phase
and an asynchronous execution phase, so callers (NATS clients, GitHub
Actions, AI agents) get an `accepted` response in seconds instead of
holding the request open for the full Claude run.

Synchronous (returns to the caller as soon as it finishes):

1. Fetches the issue and verifies it is **open**
2. Verifies the body contains `<!-- agent-task:v1 -->`
3. Verifies the labels include `type:agent-task`, `agent:claude-code`,
   `agent-ready`, and `agent-approved`
4. Rejects the issue if it is labelled `claimed-by-claude`, `agent-blocked`,
   or `security-sensitive`
5. Removes `agent-ready`, adds `claimed-by-claude`, and posts a claim comment

The API call returns here with `{id, status: accepted}`. Background:

6. Clones the repo into `workDir/<run-id>`
7. Builds the prompt from the issue body and runs `claude -p` (Claude is
   instructed to implement the task, not merge PRs, and report the tests it
   ran)
8. On success: posts a summary comment. On failure: adds `agent-failed` and
   posts a failure comment. The clone is removed either way.

The runner never auto-closes the issue and never auto-merges anything.
`prompt` is ignored for issue events; the prompt is built server-side from
the issue body. Validation/claim failures are returned synchronously to the
caller; once the claim succeeds, downstream failures are reported on the
issue itself, not in the API response.

The same flow is also exposed as a one-shot subcommand for AI agents — see
[claude-runner run-issue](#claude-runner-run-issue) below.

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

#### Issue task

```bash
claude-runner-client \
  --transport http \
  --endpoint http://localhost:8080 \
  --event issue \
  --repo https://github.com/user/repo.git \
  --issue-number 42
```

`--prompt` is ignored when `--event=issue`; the runner builds the prompt from
the issue body. The server must be configured with a GitHub token (see
[config.yaml](#configyaml)).

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

Use as a step in your workflow to send prompts to a running claude-runner
instance. Add `NATS_CREDS` (content of `user.creds`) to your repository's
**Settings → Secrets → Actions** for any of the examples below.

### CI Mode (Pull Request and non-PR)

The same step works for `pull_request`, `push`, and `workflow_dispatch`. On a
PR, `base-ref` and `pr-number` are populated and claude-runner generates a PR
diff for Claude to review against. On non-PR triggers those fields are empty
and the prompt runs against the cloned `ref` without diff context. Add an
`if: github.event_name == 'pull_request'` gate if you want to limit the step
to PRs only.

```yaml
on:
  pull_request:
  push:
    branches: [main]
  workflow_dispatch:

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - name: Code Review with Claude
        uses: flarexio/claude-runner@v1
        with:
          prompt: |
            Review all changed files in this repository.
            Before reviewing, search for all REVIEW.md files. If found, apply the root
            REVIEW.md as global review guidelines and each subdirectory's REVIEW.md as
            additional guidelines scoped to that directory.
            Provide a concise summary of findings. If the code looks good, say so briefly.
          repo: ${{ github.server_url }}/${{ github.repository }}.git
          ref: ${{ github.head_ref || github.ref_name }}
          base-ref: ${{ github.base_ref }}
          event: ${{ github.event_name }}
          pr-number: ${{ github.event.pull_request.number || '' }}
          nats-creds-content: ${{ secrets.NATS_CREDS }}
          edge-id: ${{ secrets.EDGE_ID }}
          output-file: claude-output.md

      - name: Add Claude review to summary
        if: always()
        run: |
          if [ -f claude-output.md ]; then
            echo "# Claude Review" >> "$GITHUB_STEP_SUMMARY"
            echo "" >> "$GITHUB_STEP_SUMMARY"
            cat claude-output.md >> "$GITHUB_STEP_SUMMARY"
          fi
```

### Issue Mode

Triggered on `issues` events (typically `labeled`). `prompt` is omitted —
claude-runner builds the prompt from the issue body. The runner only acts on
issues that pass validation (open, marker present, required labels including
`agent-ready` and `agent-approved`, no excluded labels). The server must have
a GitHub token configured (see [config.yaml](#configyaml)). Results are
posted back as a comment on the issue; `output-file` lets you also attach the
output to the workflow run summary.

The job `if:` below combines the label gate with an `author_association`
check so the workflow only runs for issues opened by a trusted member of the
repo (`OWNER`, `MEMBER`, or `COLLABORATOR`). This closes the gap where the
issue author edits the body after a maintainer has labelled it: now the
person who can edit the body is restricted to the same trusted group.

```yaml
on:
  issues:
    types: [labeled]

jobs:
  agent:
    if: |
      github.event.label.name == 'agent-ready' &&
      contains(fromJson('["OWNER", "MEMBER", "COLLABORATOR"]'),
               github.event.issue.author_association)
    runs-on: ubuntu-latest
    steps:
      - name: Run Claude on issue
        uses: flarexio/claude-runner@v1
        with:
          event: issue
          repo: ${{ github.server_url }}/${{ github.repository }}.git
          issue-number: ${{ github.event.issue.number }}
          nats-creds-content: ${{ secrets.NATS_CREDS }}
          edge-id: ${{ secrets.EDGE_ID }}
          output-file: claude-output.md

      - name: Add Claude output to summary
        run: |
          if [ -f claude-output.md ]; then
            echo "# Claude Issue Run" >> "$GITHUB_STEP_SUMMARY"
            echo "" >> "$GITHUB_STEP_SUMMARY"
            cat claude-output.md >> "$GITHUB_STEP_SUMMARY"
          fi
```

`output-file` is optional. When set, the client writes Claude's output to that
path in addition to stdout. Relative paths are resolved under
`$GITHUB_WORKSPACE`, and parent directories are created automatically.

## API

Two operations, two endpoints. Clients choose based on what they want to
do; the server does no event-based dispatching.

### POST /api/run

Synchronous prompt / PR review. Request:

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
  "output": "Claude's full review",
  "error": ""
}
```

### POST /api/run-issue

GitHub issue task. Validates and claims synchronously, then runs Claude in
the background. Request:

```json
{
  "repo": "owner/repo",
  "issue_number": 42
}
```

Response (returns after the claim phase, before Claude runs):

```json
{
  "id": "01JNXYZ...",
  "output": "Issue owner/repo#42 accepted; claude-runner is processing in the background.",
  "error": ""
}
```

The two endpoints have distinct request types — `run` does not accept
`issue_number`, and `run-issue` does not accept `prompt`/`base_ref`/
`pr_number`. Final issue success/failure is posted as a comment on the
GitHub issue, not in the response.

NATS callers use the same endpoint names as subjects:
`<topic>.run` and `<topic>.run-issue`.

## claude-runner run-issue

`claude-runner run-issue` is a one-shot subcommand that performs the full
GitHub issue agent-task protocol — claim, run Claude, post results — in a
single foreground invocation. It builds the same Service the daemon uses,
calls `Service.RunIssue`, then drains the background goroutine via
`Service.Close` before exiting. Designed to be invocable directly by AI
agents as a skill.

```bash
claude-runner run-issue \
  --repo owner/repo \
  --issue-number 42
```

Required: `--repo`, `--issue-number`. Useful flags:

- `--path` — config directory (defaults to `~/.flarex/claude-runner`); the
  GitHub token, allowed-tools, max-turns, and bypass-permissions for issue
  events are all read from `config.yaml`.
- `--ref` — branch to check out.
- `--github-token` — overrides the config token (env: `GITHUB_TOKEN`).

Validation/claim failures cause a non-zero exit before Claude runs. Once
Claude runs, the result (including a non-zero exit) is posted to the issue
and the subcommand exits 0. The runner never auto-closes the issue and
never auto-merges anything.

## License

MIT
