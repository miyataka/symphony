# Hot Reload for Symphony Go

This document defines what "hot reload" means for the Go implementation, why
the current build is non-conformant, and the recommended path to conformance
with [`SPEC.md` §6.2 Dynamic Reload Semantics][spec-reload].

[spec-reload]: ../../SPEC.md

## Summary

- Go is currently non-conformant with SPEC §6.2: `orchestrator.Service` reads
  `WORKFLOW.md` once at startup and never re-reads it.
- The recommended first iteration is a poll-based `WorkflowStore` that mirrors
  `SymphonyElixir.WorkflowStore` (1s stamp poll, atomic swap, last-known-good
  fallback). This is a small, dependency-free change.
- In-flight agent runs are intentionally not interrupted on reload; new
  configuration applies to dispatch, retry scheduling, reconciliation, and
  workspace hooks executed after the swap.
- Go plugins, embedded scripting, and RPC workers are explicitly out of scope:
  the issue's own framing rejects them, and they are not required to satisfy
  SPEC §6.2.

## Scope of "Hot Reload" for Go

Symphony Go does not promise BEAM/Phoenix-style runtime code swapping. The Go
runtime cannot replace function bodies of a running process, and Go plugins
(`plugin.Open`) are platform-restricted and brittle in practice. "Hot reload"
in this document means exactly the SPEC §6.2 contract:

- Detect changes to the resolved `WORKFLOW.md`.
- Re-read and re-validate it.
- Apply the new effective configuration to *future* orchestrator decisions
  without restarting the process or interrupting in-flight runs.
- On invalid reload, keep operating with the last known good configuration and
  emit an operator-visible error.

Anything beyond this — replacing tracker implementations live, swapping the
`agent.command` binary while a turn is mid-flight, replacing the orchestrator
state machine — is a non-goal for the first iteration and would only be
revisited if a concrete user need appears.

## Current Behavior (as of `issue-27-symphony` branch)

- `cmd/symphony/main.go:42` calls `loadWorkflow(workflowPath)` once.
- `orchestrator.New` (`internal/orchestrator/orchestrator.go:48`) captures the
  resolved `workflow.Config` and prompt template into the `Service` struct.
- `Service.Run` (`internal/orchestrator/orchestrator.go:66`) reads
  `s.cfg.PollInterval()`, hooks, agent command, etc. from that snapshot for
  the lifetime of the process.
- There is no file watcher, no signal handler for reload (`SIGHUP` etc.), and
  no IPC surface that re-reads the file.

The Elixir reference holds the workflow in `SymphonyElixir.WorkflowStore`
(`elixir/lib/symphony_elixir/workflow_store.ex`): a `GenServer` that polls the
resolved path every 1s, computes a `(mtime, size, content hash)` stamp, and
atomically replaces its cached workflow when the stamp changes. Reads from
`Workflow.current/0` always observe the latest accepted workflow.

## Recommended Implementation Path (first iteration)

Mirror the Elixir design as closely as the Go runtime allows.

### Shape

1. Add a `workflow.Store` (or `workflow.Watcher`) type in
   `go/internal/workflow/`. It owns:
   - the resolved `path`,
   - the last accepted `Definition` and parsed `Config`,
   - the last accepted stamp `(mtime int64, size int64, sha256 [32]byte)`,
   - a `sync.RWMutex` (or `atomic.Pointer[snapshot]`) guarding atomic swap.
2. Expose:
   - `Current() (Definition, Config)` — non-blocking read of the latest
     accepted snapshot.
   - `ForceReload(ctx) error` — used by tests and possibly by a `SIGHUP`
     handler in a later iteration.
   - `Run(ctx) error` — polls on a `time.Ticker` (1s default, configurable for
     tests), calls `reload()`, returns when `ctx` is cancelled.
3. `reload()`:
   - stat the file; if `os.IsNotExist`, log warn and keep cache,
   - read content, compute stamp; if equal to last accepted, return,
   - parse + `ParseConfig`; on error, log error and keep last accepted
     snapshot,
   - on success, swap atomically and emit a structured info log.
4. Wire `Service` to read configuration through the store on every
   poll iteration and at every dispatch decision boundary, instead of reading
   `s.cfg` fields captured at construction time. Concretely the following
   call sites in `internal/orchestrator/orchestrator.go` need to read from
   the store rather than the captured snapshot:
   - `Run` ticker construction (`PollInterval`)
   - `poll` / `reconcileRunning` state list reads (`Tracker.*States`)
   - `canDispatch` concurrency limits (`Agent.MaxConcurrentAgents*`)
   - `runIssue` per-turn (`Agent.MaxTurns`, `HookTimeout`, hook scripts,
     `Agent.Command`, `TurnTimeout`)
   - `applyReviewStatePolicy` / `pullRequestReadyForMerge`
     (`PullRequest.*`, `Tracker.*State`)
   - `cleanupWorkspaces` (`Workspace.Cleanup*`)
5. The prompt template lives alongside `Config` in the `Definition`; the
   store returns both so prompt changes apply on the next `runAgentTurn` call.

### Why poll, not fsnotify

The issue lists `air` / `watchexec` / `reflex` as dev-only options; this
document is about the orchestrator process itself, not a dev rebuild loop.
Within the orchestrator, polling beats fsnotify here:

- SPEC §6.2 says implementations *SHOULD* re-validate defensively in case
  filesystem watch events are missed. Even with `fsnotify`, polling is still
  required, so adding `fsnotify` only adds dependency surface (CGO-free but
  another external package) and platform-specific edge cases (macOS APFS
  rename semantics, Docker bind-mount events, network-FS workspaces) without
  removing the poll path.
- Polling matches the Elixir reference exactly, keeping the two
  implementations easy to compare.
- A 1s poll on a small Markdown file is negligible cost.

### Trigger surface

For the first iteration, the only trigger is the 1s background poll. Two
follow-ups, deferred until a real need surfaces:

- `SIGHUP` handler that calls `Store.ForceReload`. Useful for ops who want to
  trigger reload deterministically (e.g. after `git pull`).
- An optional `symphony reload` admin subcommand or `gRPC`/HTTP endpoint.
  Out of scope until the orchestrator gains an admin surface for any other
  reason.

## Reloadable vs Restart-Required Fields

The cut below is conservative: anything that participates in a long-lived
resource (HTTP client, listener, signal channel, tracker auth) is
restart-required for the first iteration. Anything read by value on each
dispatch tick is reloadable.

### Reloadable on next tick

Read fresh from `Store.Current()` when the orchestrator next consults them.
New values apply only to dispatch decisions and agent turns started *after*
the swap.

- `polling.interval_ms` — applied on the next ticker reset (see
  Implementation Notes).
- `agent.command`, `agent.kind`-derived defaults that compose with `command`,
  `agent.max_turns`, `agent.max_retry_backoff_ms`, `agent.turn_timeout_ms`.
- `agent.max_concurrent_agents`, `agent.max_concurrent_agents_by_state` —
  shrinking the cap does not preempt running work; new dispatches are gated
  against the new cap.
- `hooks.after_create`, `hooks.before_run`, `hooks.after_run`,
  `hooks.before_remove`, `hooks.timeout_ms` — re-read at every hook
  invocation. Workspaces created before the swap will see the new
  `before_remove` script when they are eventually torn down.
- Prompt template body — re-read at every `runAgentTurn`.
- `tracker.start_state`, `handoff_state`, `rework_state`, `merging_state`,
  `done_state`, `active_states`, `monitor_states`, `terminal_states`,
  `workpad_marker`, `read_issue_dependencies`,
  `allowed_repositories` — re-read at every poll, dispatch, and review-state
  policy decision.
- `pull_request.auto_merge`, `merge_method`, `allow_draft`,
  `require_approval`, `require_passing_checks`, `required_check_names`.
- `workspace.cleanup_orphans`, `workspace.cleanup_stale_after_days` — read at
  the next `cleanupWorkspaces` call.
- `observability.log_level` — reloadable iff the slog handler is rebuilt
  with a `slog.LevelVar` on first iteration. Otherwise treat as
  restart-required.

### Restart-required

The reloader MUST detect a change to any of these fields, log a clear
operator-visible warning ("changing X requires a restart; the new value is
ignored"), and continue with the previous effective value.

- `tracker.kind` — selects implementation (only `github` today, but the
  store's tracker client is constructed once).
- `tracker.token` — the `githubtracker` client captures this in its HTTP
  client; reloading would need lifecycle plumbing through the tracker.
- `tracker.endpoint`, `tracker.rest_endpoint`, `tracker.owner`,
  `tracker.owner_type`, `tracker.project_number`, `tracker.status_field`,
  `tracker.priority_field`, `tracker.assignee` — captured by the tracker
  client at construction; relaxing this is feasible but is a separate piece
  of work because it requires the tracker to expose an `Update(cfg)` hook.
- `workspace.root` — workspaces already on disk hold absolute paths; in-flight
  runs would lose their workspace if the root moved. Treat as
  restart-required for safety. (Reloadable in principle for *future*
  workspaces, but only worth it if a user case appears.)
- `agent.kind` — its only runtime effect is to seed defaults for
  `agent.command` and `tracker.workpad_marker`. Once those defaults are
  resolved into `Config`, swapping `kind` mid-flight is confusing rather
  than useful. Document as restart-required.
- `observability.log_json` — rebuilding the slog handler with a different
  output format mid-process is doable but forces every concurrent goroutine
  to rebind. Restart-required for first iteration.

### Edge cases

- `polling.interval_ms`: changing this requires resetting `time.NewTicker`.
  Implementation should compare the new value against the last applied one
  inside the `Run` loop and call `ticker.Reset(newInterval)` when it
  changes. Do not allocate a new ticker every poll.
- `agent.max_concurrent_agents` decreasing below the current running set:
  `canDispatch` already uses `len(s.running) >= max`, so existing runs
  continue and no new ones start until count drops below the new cap. This is
  the intended behavior; document it.
- `tracker.terminal_states` shrinking: a workspace removed by a previous
  cleanup pass under the old set is gone. A state moving *out* of the
  terminal set will not retroactively recreate the workspace.

## In-Flight Agent Run Behavior

Per SPEC §6.2, implementations are not required to restart in-flight agent
sessions when config changes. Symphony Go follows that rule strictly:

- Agent commands launched via `runAgentTurn` (`orchestrator.go:318`) keep
  running with the `agent.command` and `agent.turn_timeout_ms` value that
  was effective at launch.
- Hooks (`workspace.RunBefore`, `workspace.RunAfter`) for an in-flight run
  observe the *current* hook scripts at the moment they execute. This is
  intentional: hooks run between turns, so a fix landed in `WORKFLOW.md`
  during a long retry loop becomes effective without forcing a restart, and
  hooks are short shell scripts where a fresh read per invocation is cheap
  and predictable.
- The `runHandle` in `s.running` continues to hold the launch-time issue
  metadata. Reload does not cancel the per-issue context.

This split (hooks re-read, agent.command frozen) is asymmetric on purpose:
hooks are per-turn boundary events where reload safety is easy to reason
about; agent commands are long-running child processes where mid-flight
swap would require killing the child and recovering its workspace state,
which is well beyond the scope of "config reload."

A `SIGTERM` / `Ctrl-C` to the orchestrator still cancels in-flight runs as
today; reload is strictly a non-destructive operation.

## Why Not Plugins / RPC

The issue lists this as a candidate; it should be ruled out for the first
iteration:

- Go plugins (`plugin.Open`) are limited to Linux/Darwin, require strict
  toolchain alignment between host and plugin, and do not support unloading.
- RPC workers (gRPC / Unix socket process boundary) would change the
  shape of `tracker.Tracker` and `workspace.Manager` from in-process
  interfaces to network calls, with new failure modes (transport timeouts,
  partial state) and no obvious user benefit at the current single-binary
  scale.
- Neither approach is required by SPEC §6.2. The SPEC's reload contract is
  satisfied by the data-only `WorkflowStore` design.

If a future requirement emerges (e.g. swapping the agent runtime live for a
long-lived multi-tenant deployment), it can be revisited as a separate
proposal with its own SPEC change.

## Implementation Notes for the Follow-Up PR

The actual code change is intentionally not in this PR. When it lands, the
following are the focused tests it should add:

- `workflow.Store` re-reads on stamp change (write file → wait → assert
  `Current()` returns the new content).
- Invalid YAML during reload keeps the last good `Config` and surfaces an
  error log; subsequent valid edits recover.
- File deleted then restored: store keeps last good and recovers on
  restore without crashing.
- `orchestrator.Service` honors a changed `polling.interval_ms` by
  resetting its ticker (assert via injectable clock).
- `orchestrator.Service` honors a changed `agent.max_concurrent_agents`
  on the next `canDispatch` call (no preemption of running work).
- Restart-required fields (e.g. `tracker.token`) emit the documented
  operator-visible warning and are otherwise ignored.

For dev-loop ergonomics — rebuild-and-restart on Go source changes — keep
that out of this design entirely. Use `air`/`watchexec`/`reflex` externally
if desired; it has nothing to do with SPEC §6.2 conformance.
