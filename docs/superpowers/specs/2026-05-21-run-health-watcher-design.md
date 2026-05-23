# Run Health Watcher Design

## Goal

Reduce operator anxiety during long-running Symphony issue sessions.

The target moment is when an issue is actively assigned to an agent, but a human cannot tell whether
the agent is making progress, quietly working, or stuck. Symphony should expose enough live signal to
answer:

- Is this run still healthy?
- When did meaningful progress last happen?
- Why does Symphony think this run is or is not suspicious?
- What will Symphony do next?

The first implementation focuses on the Elixir orchestrator.

## Non-Goals

- Do not make Linear the primary live status surface.
- Do not update Linear on every heartbeat or event.
- Do not require the dashboard to become part of core orchestration correctness.
- Do not infer detailed task completion percentages.
- Do not interrupt a currently running Codex turn unless the app-server protocol later exposes a
  safe supported mechanism for that.

## User Experience

Dashboard and terminal status show a compact health summary for each running issue:

- `Health`: `Active`, `Quiet`, `Suspect`, or `Stalled`
- `Idle`: elapsed time since the last meaningful progress signal
- `Why`: short reason such as `same event x12`, `tokens only`, `no turn progress`, or
  `self-report missing`
- `Next`: `watching`, `asking agent`, `retrying soon`, or `human attention`

Linear stays quiet during healthy runs. Symphony writes to the persistent Workpad only when a run
becomes suspicious or fails to self-report after being asked.

## Health Model

Add a small `SymphonyElixir.RunHealth` module that evaluates one running entry at a time:

```elixir
RunHealth.evaluate(running_entry, now, config)
```

It returns a map with:

- `status`: `:active | :quiet | :suspect | :stalled`
- `reason`: atom or compact string explaining the classification
- `next_action`: `:watching | :requesting_self_report | :retrying_soon | :human_attention`
- `last_meaningful_progress_at`: timestamp
- `details`: optional counters used by the dashboard and logs

The orchestrator remains the source of truth for running state. `RunHealth` is pure classification
logic, so it can be unit-tested without starting Codex, Linear, or Phoenix.

## Running Entry Fields

Extend orchestrator running entries with health metadata:

- `health_status`
- `health_reason`
- `health_next_action`
- `last_meaningful_progress_at`
- `last_progress_signature`
- `repeated_event_count`
- `self_report_requested_at`
- `self_report_deadline_at`
- `self_report_attempts`
- `self_report_state`

These fields are observability and control metadata. Existing fields such as `last_codex_event`,
`last_codex_timestamp`, `turn_count`, and token counts remain intact.

## Meaningful Progress

The initial classifier should be conservative. A signal counts as meaningful progress when it is
likely to correspond to visible work:

- a new Codex session starts
- a turn completes
- `turn_count` increases
- a dynamic tool call completes
- command execution or file-change related events occur
- total tokens increase by at least a configurable minimum since the last progress point
- the event signature changes after a long repeated run of the same event

Suspicion grows when activity exists but does not look useful:

- no Codex events since start
- the same event signature repeats many times
- tokens increase without turn progress for too long
- only non-action notifications arrive
- a self-report was requested and no useful report arrived before the deadline

## State Transitions

Health status is derived from elapsed time and event patterns:

1. `active`
   - recent meaningful progress exists
   - next action is `watching`
2. `quiet`
   - no recent meaningful progress, but still inside the normal quiet window
   - next action is `watching`
3. `suspect`
   - the run crossed the suspect threshold or matched a suspicious pattern
   - next action is `requesting_self_report`
4. `stalled`
   - the self-report deadline passed without useful signal, or existing hard stall timeout fired
   - next action is `retrying_soon`

The existing `codex.stall_timeout_ms` remains the hard safety net. Run health adds earlier warnings
and a faster retry path after self-report failure.

## Self-Report Request

When a run first becomes `suspect`, the orchestrator requests a self-report from the agent.

The preferred implementation is a control message from Orchestrator to AgentRunner:

```elixir
{:symphony_control, :request_self_report, payload}
```

The payload includes the issue id, reason, last meaningful progress time, and a short instruction:

> Briefly report current status, what has changed since the last visible progress, what is taking
> time, and what you will do next. Do not create extra Linear comments; Symphony owns the run-health
> Workpad warning.

Because the current app-server client runs one turn synchronously, the first version should avoid
unsafe mid-turn interruption. If no supported app-server method exists to append input to an active
turn, AgentRunner stores the pending self-report request and injects it into the next continuation
prompt. The dashboard and terminal still show `asking agent` immediately.

If a future Codex app-server protocol exposes a safe method for in-turn control input, the
AgentRunner control path can use that without changing the RunHealth classifier or dashboard API.

## Self-Report Failure

If the self-report deadline passes without useful signal:

1. Orchestrator marks the run `stalled`.
2. Orchestrator writes one Workpad warning through the tracker layer.
3. Orchestrator terminates the worker.
4. Orchestrator schedules retry earlier than the hard stall timeout.

The retry error should include a concise reason such as:

```text
self-report missing after suspect run health: repeated_same_event
```

## Linear Workpad Behavior

Linear should be updated only for abnormal health transitions.

Write or update the persistent `## Codex Workpad` comment when:

- a run first becomes `suspect`
- a self-report request is issued
- self-report fails and Symphony is about to retry

The Workpad entry should be compact:

```md
### Run Health Warning

- Status: Suspect
- Reason: repeated_same_event
- Last meaningful progress: 2026-05-21T10:20:00Z
- Action: Requested agent self-report
- Retry policy: Will retry early if no useful report arrives by 2026-05-21T10:22:00Z
```

Add first-class Workpad helpers to the tracker layer so the orchestrator can upsert a named section
inside the persistent Workpad comment. The Linear adapter can implement this with the existing Linear
GraphQL client, but callers should not build Linear-specific GraphQL directly.

## API Shape

Extend `GET /api/v1/state` running rows:

```json
{
  "issue_identifier": "SYM-35",
  "state": "Rework",
  "health": {
    "status": "suspect",
    "reason": "repeated_same_event",
    "next_action": "requesting_self_report",
    "last_meaningful_progress_at": "2026-05-21T10:20:00Z",
    "idle_ms": 620000,
    "details": {
      "repeated_event_count": 12,
      "self_report_deadline_at": "2026-05-21T10:32:00Z"
    }
  }
}
```

Extend `GET /api/v1/<issue_identifier>` with the same `health` block under both the top-level issue
payload and the `running` block when available.

Existing fields stay backward-compatible.

## Dashboard UI

Add a compact health display to the existing Running sessions table.

The preferred layout is one new `Health` column and slightly richer text in the existing Codex update
cell:

- Health badge: `Active`, `Quiet`, `Suspect`, or `Stalled`
- Idle text: `idle 7m 12s`
- Tooltip or secondary text for reason and next action

Use restrained colors:

- active: green/neutral
- quiet: gray
- suspect: amber
- stalled: red

Avoid adding a large new dashboard section in the first version.

## Terminal Dashboard

Extend the existing running row formatting with a short health segment. The terminal dashboard has
limited width, so prefer compact strings:

```text
SYM-35  Rework  18m/2  14k  Suspect  idle 10m same-event
```

When terminal width is tight, health should be more important than session id. Session id can remain
available through JSON details and logs.

## Configuration

Add `observability.run_health` configuration:

```yaml
observability:
  run_health:
    enabled: true
    quiet_after_ms: 300000
    suspect_after_ms: 600000
    self_report_timeout_ms: 120000
    early_retry_on_self_report_failure: true
    min_token_progress_delta: 500
    repeated_event_suspect_count: 10
```

Defaults should keep the feature enabled but conservative. If the config block is omitted, Symphony
should behave like today except for richer snapshot health fields derived from existing state.

If `enabled: false`, dashboard/API may still report basic idle time, but no self-report requests,
Linear Workpad writes, or early retries should occur.

## Error Handling

- If health evaluation fails, log the error and keep the existing running entry.
- If Workpad update fails, log and continue with retry behavior.
- If self-report request delivery fails, mark `self_report_state` as failed and proceed according to
  the configured timeout/retry policy.
- If a run leaves active states while suspicious, normal reconciliation wins.
- If the issue becomes terminal, normal cleanup wins.

## Testing

Unit tests:

- `RunHealth` classifies active, quiet, suspect, and stalled runs.
- repeated event signatures increment suspicion.
- meaningful progress resets idle time and repeated counters.
- token-only progress only counts above `min_token_progress_delta`.
- disabled config suppresses self-report actions.

Orchestrator tests:

- snapshot running rows include health blocks.
- suspect transition records self-report request metadata.
- self-report timeout schedules retry earlier than hard stall timeout.
- terminal or non-active issue reconciliation overrides run-health retry behavior.

Presenter/API tests:

- state payload includes health without removing existing fields.
- issue payload includes health for running issues.

Dashboard/terminal tests:

- running rows display health status and reason.
- narrow terminal output remains readable.

Linear tests:

- Workpad is updated only for suspect/self-report-failed transitions.
- repeated suspect evaluations do not append duplicate warning comments.

## Implementation Order

1. Add config schema fields for `observability.run_health`.
2. Add `RunHealth` pure module and unit tests.
3. Store health metadata in orchestrator running entries.
4. Add health to snapshots and presenter payloads.
5. Render health in web dashboard and terminal dashboard.
6. Add self-report request metadata and timeout handling.
7. Add AgentRunner control-message plumbing for deferred self-report prompts.
8. Add Linear Workpad warning helper.
9. Add integration tests for early retry and Workpad update behavior.

## Implementation Constraints

- The first version defers self-report to the next continuation prompt instead of interrupting an
  active Codex turn. In-turn interruption is out of scope until the app-server protocol exposes a
  supported method for it.
- The tracker layer should expose a generic helper such as
  `upsert_workpad_section(issue_id, section_key, markdown)`. Linear-specific GraphQL stays inside
  the Linear adapter.
