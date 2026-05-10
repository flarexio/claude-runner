# AGENTS.md

This file provides project-wide development standards, build commands, and architectural rules for AI coding agents (Claude Code, Codex, Cursor, etc.) working in this repository.

## Common commands

```bash
# Build server and client
go build cmd/claude-runner/main.go
go build cmd/claude-runner-client/main.go

# Run all tests (CI runs this on Linux)
go test -v ./...

# Run a single test
go test -run TestBuildArgsUsesIssueOverride -v ./...

# Run the daemon (loads ~/.flarex/claude-runner/config.yaml)
go run cmd/claude-runner/main.go            # NATS only
go run cmd/claude-runner/main.go --http     # also enable HTTP on :8080

# One-shot subcommand: full GitHub issue agent-task protocol in foreground
go run cmd/claude-runner/main.go run-issue --repo owner/repo --issue-number 42
```

Tests that need to invoke `claude` shell out to a fake binary that the helpers in `service_test.go` (`prependFakeClaude`, `prependFakeClaudeRecording`) drop on `$PATH`. The recording variant inspects the actual argv that would be passed to `claude` and is **POSIX-only** — it self-skips on Windows. Develop on Linux/macOS to exercise the full suite, or rely on CI.

## Architecture

The service is a single Go module (`github.com/flarexio/claude-runner`) implementing the layering described in the README: `Service → Middleware (logging) → Endpoint → Transport (NATS / HTTP)`. The pattern follows the project-wide convention: endpoints map 1:1 to `Service` methods, no event dispatch happens at the endpoint or transport layer, async work lives inside the `Service`, and the CLI subcommand goes through `Service` (not a separate code path).

### Two operations, two endpoints

The `Service` interface (`service.go`) exposes exactly two methods, and `EndpointSet` (`endpoint.go`) wires each to its own transport route:

| Service method | HTTP route        | NATS subject suffix | Behavior                                                  |
| -------------- | ----------------- | ------------------- | --------------------------------------------------------- |
| `Run`          | `POST /api/run`   | `run`               | Synchronous prompt / PR review                            |
| `RunIssue`     | `POST /api/run-issue` | `run-issue`     | Sync validate+claim → goroutine for execute → `accepted`  |

Request types are distinct (`RunRequest` vs `RunIssueRequest`); clients pick the endpoint, the server never branches on `event` to choose an endpoint. When adding a new operation, add a new `Service` method + `Endpoint` + transport route — do **not** overload an existing endpoint with event dispatch.

### Run vs RunIssue: sync-claim, async-execute

`Run` is fully synchronous and delegates to `runStateless` (a thin wrapper over the shared workspace helper — see Workspace lifecycle below). `RunIssue` (`service_issue.go`) delegates to `runIssueWorkflow`, which is the only place that splits a request into a sync phase and a background phase:

1. **Sync (returned to caller):** fetch issue, `validateIssue` (open + `IssueMarker` + `RequiredIssueLabels` + no `ExcludedIssueLabels` + at most one of `KnownModelLabels`), pick model via `selectModelLabel` + `selectIssueModel`, run `claimIssue` (remove `agent-ready`, add `claimed-by-claude`, post claim comment).
2. **Async (`svc.launchBg`):** call `runIssueExecution` (which invokes the shared workspace helper with `preserveOnFailure` from issue config), then post a success or failure comment back to the issue. The goroutine runs on a fresh `context.Background()` so it survives request cancellation. `service.bgWg` tracks it; `Service.Close()` blocks on the WaitGroup.

Failure comments are built by `buildIssueFailureComment` from an `issueFailureReport{runID, detail, ws}` and run through `sanitizeFailureDetail`, which strips workspace paths and the runner's home dir before posting. The comment includes the run ID for log correlation and a "workspace preserved on the runner" hint when `ws.preserved` is set, but **never** posts the host path. Extend that struct rather than calling `CreateComment` directly when adding new failure context.

`run-issue` (the CLI subcommand in `cmd/claude-runner/main.go`) reuses the same `Service` and explicitly calls `svc.Close()` after `RunIssue` returns so the foreground process waits for the background goroutine before exiting. The HTTP/NATS daemons call `svc.Close()` from `defer` for graceful shutdown.

Issue mode prompts are **always built server-side** by `buildIssuePrompt` from the issue body — `RunRequest.Prompt` is overwritten before reaching the workspace helper. Don't try to surface the client's `prompt` for issue events.

### Event-aware config resolution

`buildArgs` calls `eventConfig(req.Event)` (`service.go`). For `EventIssue`, fields fall through `cfg.Issue` → top-level `Config`; for everything else, only top-level values are used. `BypassPermissions: true` passes `--dangerously-skip-permissions` and **suppresses `allowedTools` entirely** (it is not merged with the bypass flag). `RunRequest.Model` (set internally from issue model-label mapping) overrides the resolved config model. Read both `eventConfig` and `selectIssueModel` together when changing how flags are derived — they are the single source of truth and changes anywhere else will desync them from tests.

### Workspace lifecycle

Both `Run` and `RunIssue` execute Claude through the shared helper `runClaudeInTemporaryWorkspace(ctx, req, opts)` (`service.go`), which resolves `(taskRoot, workDir)` via `resolveWorkspacePaths`:

- **Stateless / CI / PR review** (`runStateless`, `runOptions{}`): per-run ULID, flat — `taskRoot == workDir == <cfg.WorkDir>/<ulid>`. Always cleaned up.
- **Issue execution** (`runIssueExecution`, `runOptions{issueTaskID: gh-issue-<owner>-<repo>-<n>, preserveOnFailure: cfg.Issue.PreserveOnFailure}`): stable layout — `taskRoot = <cfg.WorkDir>/<issueTaskID>`, `workDir = <taskRoot>/repo`. The git checkout lives under `repo/` so the task root can hold sibling runner metadata (e.g. `.claude-runner/`) outside the worktree. Future state-persistence work hangs off the task root, not the checkout.
- **Existing-workspace mode** (`req.Repo == ""`): both paths equal `cfg.WorkDir`; the helper never removes it.

Cleanup operates on `taskRoot`, not `workDir`. When `preserveOnFailure` is true and claude exits non-zero, `ws.preserved` is set and the deferred `RemoveAll` is skipped, leaving the whole task root for an operator to inspect. `prepareWorkDir` itself runs `RemoveAll(workDir)` before cloning so a leftover `repo/` from a previous preserved-on-failure run does not block re-cloning. Resume is out of scope; the runner re-clones each time and any future `.claude-runner/` content has its own lifecycle.

`Issue.PreserveOnFailure` defaults to `true` via custom `UnmarshalYAML` on both `Config` and `EventConfig` (`model.go`) — set `issue.preserveOnFailure: false` in `config.yaml` to opt out. Don't change the default in struct literals; tests construct configs directly and rely on the zero value, while YAML-loaded daemons rely on the unmarshaler.

`git clone` is invoked with `-c core.longpaths=true` so deep `.git/objects/...` paths work on Windows even when the test name + workspace prefix push paths past `MAX_PATH` (260 chars). When `BaseRef` is provided, `generateDiff` writes `claude-runner.diff` into the workspace and `preparePrompt` appends a "Pull request context" trailer; issue events skip this trailer (the prompt is already authoritative).

### Issue protocol constants

All label/marker strings live in `model.go` (`IssueMarker`, `Label*`, `RequiredIssueLabels`, `ExcludedIssueLabels`, `KnownModelLabels`). The validation order in `validateIssue` matches the README's documented order — keep the README and that function aligned when adding/removing gates.

### GitHub client

`github.go` defines a small `GitHubClient` interface (`GetIssue`, `AddLabels`, `RemoveLabel`, `CreateComment`) with an HTTP-backed implementation. Tests inject a fake (`fakeGitHub` in `service_issue_test.go`) — extend the interface if you need new GitHub operations rather than calling the HTTP client directly from `service_issue.go`.

### Transports

Both transports are thin: they decode the body into the right request type and dispatch to the matching endpoint. They do not branch on `event`. The HTTP transport uses Gin and a generic `endpointHandler[T]`; the NATS transport uses `nats.go/micro` with `EndpointHandler[T]` and the topic format `edges.<edge-id>.claude-runner.<run|run-issue>`. The edge-id is read from a plain text file at `<config-path>/id`; if that file is absent the daemon silently runs without NATS.
