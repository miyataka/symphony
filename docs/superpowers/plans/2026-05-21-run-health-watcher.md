# Run Health Watcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a run-health watcher that shows whether active Symphony agents are progressing, quiet, suspicious, or stalled, and retries early after failed self-report.

**Architecture:** Add a pure `RunHealth` classifier, store its output on orchestrator running entries, expose it through snapshots/API, and render it in the dashboard and terminal status. Orchestrator owns side effects: self-report request metadata, early retry, and Workpad warning updates.

**Tech Stack:** Elixir/OTP GenServer, Ecto embedded config schema, Phoenix LiveView dashboard, ExUnit.

---

## File Structure

- Create `elixir/lib/symphony_elixir/run_health.ex`
  - Pure classifier for running-entry health.
  - No tracker, Phoenix, filesystem, or process side effects.
- Create `elixir/test/symphony_elixir/run_health_test.exs`
  - Unit tests for active, quiet, suspect, stalled, repeated-event, and token-progress classification.
- Modify `elixir/lib/symphony_elixir/config/schema.ex`
  - Add `observability.run_health` embedded config with conservative defaults.
- Modify `elixir/test/support/test_support.exs`
  - Add workflow writer overrides for `observability.run_health`.
- Modify `elixir/test/symphony_elixir/workspace_and_config_test.exs`
  - Add config parsing and validation coverage.
- Modify `elixir/lib/symphony_elixir/orchestrator.ex`
  - Initialize health metadata.
  - Evaluate health after Codex updates and during reconciliation.
  - Add health to snapshots.
  - Add self-report deadline and early retry path.
- Modify `elixir/test/symphony_elixir/orchestrator_status_test.exs`
  - Add snapshot, suspect transition, and early retry tests.
- Modify `elixir/lib/symphony_elixir_web/presenter.ex`
  - Include health in state and issue payloads.
- Modify `elixir/test/symphony_elixir/extensions_test.exs`
  - Update observability API expectations.
- Modify `elixir/lib/symphony_elixir_web/live/dashboard_live.ex`
  - Add a compact Health column for running sessions.
- Modify `elixir/priv/static/dashboard.css`
  - Add restrained health badge styles.
- Modify `elixir/lib/symphony_elixir/status_dashboard.ex`
  - Render compact health text in terminal running rows.
- Modify `elixir/test/symphony_elixir/status_dashboard_snapshot_test.exs`
  - Update snapshots when terminal output changes.
- Modify `elixir/lib/symphony_elixir/tracker.ex`
  - Add a generic `upsert_workpad_section/3` callback and facade.
- Modify `elixir/lib/symphony_elixir/tracker/memory.ex`
  - Implement no-op/signal-preserving memory behavior for tests.
- Modify `elixir/lib/symphony_elixir/linear/adapter.ex`
  - Implement Workpad section upsert through Linear GraphQL.
- Modify `elixir/test/symphony_elixir/extensions_test.exs`
  - Add adapter delegation tests for Workpad upsert.
- Modify `elixir/lib/symphony_elixir/agent_runner.ex`
  - Accept deferred self-report requests and inject them into the next continuation prompt.
- Modify `elixir/test/symphony_elixir/core_test.exs`
  - Add AgentRunner prompt/control-message coverage for deferred self-report guidance.

---

### Task 1: Add Run-Health Config

**Files:**
- Modify: `elixir/lib/symphony_elixir/config/schema.ex`
- Modify: `elixir/test/support/test_support.exs`
- Modify: `elixir/test/symphony_elixir/workspace_and_config_test.exs`

- [ ] **Step 1: Write failing config tests**

Add these tests near the existing config parsing tests in `elixir/test/symphony_elixir/workspace_and_config_test.exs`:

```elixir
test "run health observability config uses conservative defaults" do
  write_workflow_file!(Workflow.workflow_file_path())

  settings = Config.settings!()

  assert settings.observability.run_health.enabled == true
  assert settings.observability.run_health.quiet_after_ms == 300_000
  assert settings.observability.run_health.suspect_after_ms == 600_000
  assert settings.observability.run_health.self_report_timeout_ms == 120_000
  assert settings.observability.run_health.early_retry_on_self_report_failure == true
  assert settings.observability.run_health.min_token_progress_delta == 500
  assert settings.observability.run_health.repeated_event_suspect_count == 10
end

test "run health observability config parses explicit values" do
  write_workflow_file!(Workflow.workflow_file_path(),
    observability_run_health_enabled: false,
    observability_run_health_quiet_after_ms: 10_000,
    observability_run_health_suspect_after_ms: 20_000,
    observability_run_health_self_report_timeout_ms: 30_000,
    observability_run_health_early_retry_on_self_report_failure: false,
    observability_run_health_min_token_progress_delta: 42,
    observability_run_health_repeated_event_suspect_count: 3
  )

  settings = Config.settings!()

  assert settings.observability.run_health.enabled == false
  assert settings.observability.run_health.quiet_after_ms == 10_000
  assert settings.observability.run_health.suspect_after_ms == 20_000
  assert settings.observability.run_health.self_report_timeout_ms == 30_000
  assert settings.observability.run_health.early_retry_on_self_report_failure == false
  assert settings.observability.run_health.min_token_progress_delta == 42
  assert settings.observability.run_health.repeated_event_suspect_count == 3
end

test "run health observability config validates positive thresholds" do
  write_workflow_file!(Workflow.workflow_file_path(),
    observability_run_health_quiet_after_ms: 0,
    observability_run_health_suspect_after_ms: -1,
    observability_run_health_self_report_timeout_ms: 0,
    observability_run_health_min_token_progress_delta: -1,
    observability_run_health_repeated_event_suspect_count: 0
  )

  assert {:error, {:invalid_workflow_config, message}} = Config.settings()
  assert message =~ "observability.run_health.quiet_after_ms"
  assert message =~ "observability.run_health.suspect_after_ms"
  assert message =~ "observability.run_health.self_report_timeout_ms"
  assert message =~ "observability.run_health.min_token_progress_delta"
  assert message =~ "observability.run_health.repeated_event_suspect_count"
end
```

- [ ] **Step 2: Run the config tests and verify they fail**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/workspace_and_config_test.exs
```

Expected: tests fail because `observability.run_health` and test-support overrides do not exist.

- [ ] **Step 3: Add schema support**

In `elixir/lib/symphony_elixir/config/schema.ex`, add this embedded schema before `Observability`:

```elixir
defmodule RunHealth do
  @moduledoc false
  use Ecto.Schema
  import Ecto.Changeset

  @primary_key false
  embedded_schema do
    field(:enabled, :boolean, default: true)
    field(:quiet_after_ms, :integer, default: 300_000)
    field(:suspect_after_ms, :integer, default: 600_000)
    field(:self_report_timeout_ms, :integer, default: 120_000)
    field(:early_retry_on_self_report_failure, :boolean, default: true)
    field(:min_token_progress_delta, :integer, default: 500)
    field(:repeated_event_suspect_count, :integer, default: 10)
  end

  @spec changeset(%__MODULE__{}, map()) :: Ecto.Changeset.t()
  def changeset(schema, attrs) do
    schema
    |> cast(
      attrs,
      [
        :enabled,
        :quiet_after_ms,
        :suspect_after_ms,
        :self_report_timeout_ms,
        :early_retry_on_self_report_failure,
        :min_token_progress_delta,
        :repeated_event_suspect_count
      ],
      empty_values: []
    )
    |> validate_number(:quiet_after_ms, greater_than: 0)
    |> validate_number(:suspect_after_ms, greater_than: 0)
    |> validate_number(:self_report_timeout_ms, greater_than: 0)
    |> validate_number(:min_token_progress_delta, greater_than_or_equal_to: 0)
    |> validate_number(:repeated_event_suspect_count, greater_than: 0)
  end
end
```

Then update `Observability`:

```elixir
embedded_schema do
  field(:dashboard_enabled, :boolean, default: true)
  field(:refresh_ms, :integer, default: 1_000)
  field(:render_interval_ms, :integer, default: 16)
  embeds_one(:run_health, RunHealth, on_replace: :update, defaults_to_struct: true)
end
```

Update its changeset:

```elixir
schema
|> cast(attrs, [:dashboard_enabled, :refresh_ms, :render_interval_ms], empty_values: [])
|> cast_embed(:run_health, with: &RunHealth.changeset/2)
|> validate_number(:refresh_ms, greater_than: 0)
|> validate_number(:render_interval_ms, greater_than: 0)
```

- [ ] **Step 4: Add test-support workflow overrides**

In `elixir/test/support/test_support.exs`, add defaults to the `Keyword.merge` block:

```elixir
observability_run_health_enabled: true,
observability_run_health_quiet_after_ms: 300_000,
observability_run_health_suspect_after_ms: 600_000,
observability_run_health_self_report_timeout_ms: 120_000,
observability_run_health_early_retry_on_self_report_failure: true,
observability_run_health_min_token_progress_delta: 500,
observability_run_health_repeated_event_suspect_count: 10,
```

Fetch them next to the other observability variables:

```elixir
observability_run_health_enabled = Keyword.get(config, :observability_run_health_enabled)
observability_run_health_quiet_after_ms = Keyword.get(config, :observability_run_health_quiet_after_ms)
observability_run_health_suspect_after_ms = Keyword.get(config, :observability_run_health_suspect_after_ms)
observability_run_health_self_report_timeout_ms = Keyword.get(config, :observability_run_health_self_report_timeout_ms)
observability_run_health_early_retry_on_self_report_failure = Keyword.get(config, :observability_run_health_early_retry_on_self_report_failure)
observability_run_health_min_token_progress_delta = Keyword.get(config, :observability_run_health_min_token_progress_delta)
observability_run_health_repeated_event_suspect_count = Keyword.get(config, :observability_run_health_repeated_event_suspect_count)
```

Replace the `observability_yaml(...)` call with:

```elixir
observability_yaml(
  observability_enabled,
  observability_refresh_ms,
  observability_render_interval_ms,
  %{
    enabled: observability_run_health_enabled,
    quiet_after_ms: observability_run_health_quiet_after_ms,
    suspect_after_ms: observability_run_health_suspect_after_ms,
    self_report_timeout_ms: observability_run_health_self_report_timeout_ms,
    early_retry_on_self_report_failure: observability_run_health_early_retry_on_self_report_failure,
    min_token_progress_delta: observability_run_health_min_token_progress_delta,
    repeated_event_suspect_count: observability_run_health_repeated_event_suspect_count
  }
),
```

Replace `observability_yaml/3` with:

```elixir
defp observability_yaml(enabled, refresh_ms, render_interval_ms, run_health) do
  [
    "observability:",
    "  dashboard_enabled: #{yaml_value(enabled)}",
    "  refresh_ms: #{yaml_value(refresh_ms)}",
    "  render_interval_ms: #{yaml_value(render_interval_ms)}",
    "  run_health:",
    "    enabled: #{yaml_value(run_health.enabled)}",
    "    quiet_after_ms: #{yaml_value(run_health.quiet_after_ms)}",
    "    suspect_after_ms: #{yaml_value(run_health.suspect_after_ms)}",
    "    self_report_timeout_ms: #{yaml_value(run_health.self_report_timeout_ms)}",
    "    early_retry_on_self_report_failure: #{yaml_value(run_health.early_retry_on_self_report_failure)}",
    "    min_token_progress_delta: #{yaml_value(run_health.min_token_progress_delta)}",
    "    repeated_event_suspect_count: #{yaml_value(run_health.repeated_event_suspect_count)}"
  ]
  |> Enum.join("\n")
end
```

- [ ] **Step 5: Run tests**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/workspace_and_config_test.exs
```

Expected: all tests in the file pass.

- [ ] **Step 6: Commit**

```bash
git add elixir/lib/symphony_elixir/config/schema.ex elixir/test/support/test_support.exs elixir/test/symphony_elixir/workspace_and_config_test.exs
git commit -m "feat: add run health config"
```

---

### Task 2: Add Pure RunHealth Classifier

**Files:**
- Create: `elixir/lib/symphony_elixir/run_health.ex`
- Create: `elixir/test/symphony_elixir/run_health_test.exs`

- [ ] **Step 1: Write failing unit tests**

Create `elixir/test/symphony_elixir/run_health_test.exs`:

```elixir
defmodule SymphonyElixir.RunHealthTest do
  use ExUnit.Case, async: true

  alias SymphonyElixir.RunHealth

  defp config(overrides \\ %{}) do
    Map.merge(
      %{
        enabled: true,
        quiet_after_ms: 300_000,
        suspect_after_ms: 600_000,
        self_report_timeout_ms: 120_000,
        early_retry_on_self_report_failure: true,
        min_token_progress_delta: 500,
        repeated_event_suspect_count: 3
      },
      overrides
    )
  end

  defp entry(overrides \\ %{}) do
    now = DateTime.utc_now()

    Map.merge(
      %{
        started_at: now,
        last_codex_timestamp: now,
        last_codex_event: :session_started,
        last_codex_message: %{event: :session_started, message: %{}, timestamp: now},
        last_meaningful_progress_at: now,
        last_progress_signature: "session_started",
        repeated_event_count: 0,
        codex_total_tokens: 0,
        health_last_progress_total_tokens: 0,
        turn_count: 1,
        health_last_progress_turn_count: 1,
        self_report_requested_at: nil,
        self_report_deadline_at: nil,
        self_report_attempts: 0,
        self_report_state: nil
      },
      overrides
    )
  end

  test "active when meaningful progress is recent" do
    now = DateTime.utc_now()

    health = RunHealth.evaluate(entry(last_meaningful_progress_at: now), now, config())

    assert health.status == :active
    assert health.reason == :recent_progress
    assert health.next_action == :watching
    assert health.idle_ms == 0
  end

  test "quiet when progress is older than quiet threshold but below suspect threshold" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -360, :second)

    health = RunHealth.evaluate(entry(last_meaningful_progress_at: progress_at), now, config())

    assert health.status == :quiet
    assert health.reason == :quiet
    assert health.next_action == :watching
    assert health.idle_ms >= 360_000
  end

  test "suspect when progress exceeds suspect threshold" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)

    health = RunHealth.evaluate(entry(last_meaningful_progress_at: progress_at), now, config())

    assert health.status == :suspect
    assert health.reason == :no_meaningful_progress
    assert health.next_action == :requesting_self_report
  end

  test "suspect when the same event repeats too many times" do
    now = DateTime.utc_now()

    health =
      RunHealth.evaluate(
        entry(repeated_event_count: 3, last_meaningful_progress_at: DateTime.add(now, -60, :second)),
        now,
        config(repeated_event_suspect_count: 3)
      )

    assert health.status == :suspect
    assert health.reason == :repeated_same_event
  end

  test "stalled when self-report deadline has passed" do
    now = DateTime.utc_now()

    health =
      RunHealth.evaluate(
        entry(
          self_report_requested_at: DateTime.add(now, -180, :second),
          self_report_deadline_at: DateTime.add(now, -60, :second),
          self_report_state: :requested
        ),
        now,
        config()
      )

    assert health.status == :stalled
    assert health.reason == :self_report_missing
    assert health.next_action == :retrying_soon
  end

  test "large token increase counts as meaningful progress" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)

    health =
      RunHealth.evaluate(
        entry(
          last_meaningful_progress_at: progress_at,
          codex_total_tokens: 1_000,
          health_last_progress_total_tokens: 100
        ),
        now,
        config(min_token_progress_delta: 500)
      )

    assert health.status == :active
    assert health.reason == :token_progress
    assert health.last_meaningful_progress_at == now
  end

  test "disabled config returns basic active health without actions" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -1_000, :second)

    health =
      RunHealth.evaluate(
        entry(last_meaningful_progress_at: progress_at, repeated_event_count: 99),
        now,
        config(enabled: false)
      )

    assert health.status == :active
    assert health.reason == :disabled
    assert health.next_action == :watching
  end
end
```

- [ ] **Step 2: Run the new test and verify it fails**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/run_health_test.exs
```

Expected: compile failure because `SymphonyElixir.RunHealth` does not exist.

- [ ] **Step 3: Implement `RunHealth`**

Create `elixir/lib/symphony_elixir/run_health.ex`:

```elixir
defmodule SymphonyElixir.RunHealth do
  @moduledoc """
  Pure run-health classifier for orchestrator running entries.
  """

  @type status :: :active | :quiet | :suspect | :stalled
  @type next_action :: :watching | :requesting_self_report | :retrying_soon | :human_attention

  @type health :: %{
          status: status(),
          reason: atom(),
          next_action: next_action(),
          last_meaningful_progress_at: DateTime.t() | nil,
          idle_ms: non_neg_integer() | nil,
          details: map()
        }

  @spec evaluate(map(), DateTime.t(), map() | struct()) :: health()
  def evaluate(running_entry, %DateTime{} = now, config) when is_map(running_entry) do
    config = normalize_config(config)

    if config.enabled == false do
      build_health(:active, :disabled, :watching, running_entry, now, %{})
    else
      do_evaluate(running_entry, now, config)
    end
  end

  defp do_evaluate(running_entry, now, config) do
    cond do
      self_report_deadline_passed?(running_entry, now) ->
        build_health(:stalled, :self_report_missing, :retrying_soon, running_entry, now, details(running_entry))

      token_progress?(running_entry, config) ->
        build_health(:active, :token_progress, :watching, running_entry, now, details(running_entry))

      turn_progress?(running_entry) ->
        build_health(:active, :turn_progress, :watching, running_entry, now, details(running_entry))

      repeated_event_suspect?(running_entry, config) ->
        build_health(:suspect, :repeated_same_event, :requesting_self_report, running_entry, now, details(running_entry))

      suspect_idle?(running_entry, now, config) ->
        build_health(:suspect, :no_meaningful_progress, :requesting_self_report, running_entry, now, details(running_entry))

      quiet_idle?(running_entry, now, config) ->
        build_health(:quiet, :quiet, :watching, running_entry, now, details(running_entry))

      true ->
        build_health(:active, :recent_progress, :watching, running_entry, now, details(running_entry))
    end
  end

  @spec event_signature(map()) :: String.t()
  def event_signature(%{last_codex_event: event, last_codex_message: message}) do
    [to_string(event || "none"), message_signature(message)]
    |> Enum.join(":")
  end

  def event_signature(_entry), do: "none"

  @spec meaningful_progress?(map(), map() | struct()) :: boolean()
  def meaningful_progress?(entry, config) when is_map(entry) do
    config = normalize_config(config)
    token_progress?(entry, config) or turn_progress?(entry) or progress_event?(Map.get(entry, :last_codex_event))
  end

  defp build_health(status, reason, next_action, running_entry, now, details) do
    last_progress = last_progress_at(running_entry) || last_activity_at(running_entry)
    idle_ms = elapsed_ms(last_progress, now)

    %{
      status: status,
      reason: reason,
      next_action: next_action,
      last_meaningful_progress_at: if(reason in [:token_progress, :turn_progress], do: now, else: last_progress),
      idle_ms: idle_ms,
      details: details
    }
  end

  defp details(entry) do
    %{
      repeated_event_count: Map.get(entry, :repeated_event_count, 0),
      self_report_deadline_at: Map.get(entry, :self_report_deadline_at)
    }
  end

  defp normalize_config(config) do
    %{
      enabled: Map.get(config, :enabled, true),
      quiet_after_ms: Map.get(config, :quiet_after_ms, 300_000),
      suspect_after_ms: Map.get(config, :suspect_after_ms, 600_000),
      self_report_timeout_ms: Map.get(config, :self_report_timeout_ms, 120_000),
      early_retry_on_self_report_failure: Map.get(config, :early_retry_on_self_report_failure, true),
      min_token_progress_delta: Map.get(config, :min_token_progress_delta, 500),
      repeated_event_suspect_count: Map.get(config, :repeated_event_suspect_count, 10)
    }
  end

  defp self_report_deadline_passed?(entry, now) do
    case Map.get(entry, :self_report_deadline_at) do
      %DateTime{} = deadline ->
        DateTime.compare(now, deadline) in [:gt, :eq] and Map.get(entry, :self_report_state) == :requested

      _ ->
        false
    end
  end

  defp token_progress?(entry, config) do
    current = Map.get(entry, :codex_total_tokens, 0) || 0
    previous = Map.get(entry, :health_last_progress_total_tokens, 0) || 0
    current - previous >= config.min_token_progress_delta
  end

  defp turn_progress?(entry) do
    current = Map.get(entry, :turn_count, 0) || 0
    previous = Map.get(entry, :health_last_progress_turn_count, current) || 0
    current > previous
  end

  defp repeated_event_suspect?(entry, config) do
    (Map.get(entry, :repeated_event_count, 0) || 0) >= config.repeated_event_suspect_count
  end

  defp suspect_idle?(entry, now, config) do
    elapsed_at_least?(last_progress_at(entry) || last_activity_at(entry), now, config.suspect_after_ms)
  end

  defp quiet_idle?(entry, now, config) do
    elapsed_at_least?(last_progress_at(entry) || last_activity_at(entry), now, config.quiet_after_ms)
  end

  defp elapsed_at_least?(nil, _now, _threshold_ms), do: false

  defp elapsed_at_least?(%DateTime{} = timestamp, now, threshold_ms) do
    DateTime.diff(now, timestamp, :millisecond) >= threshold_ms
  end

  defp elapsed_ms(nil, _now), do: nil

  defp elapsed_ms(%DateTime{} = timestamp, now) do
    max(DateTime.diff(now, timestamp, :millisecond), 0)
  end

  defp last_progress_at(entry), do: Map.get(entry, :last_meaningful_progress_at)
  defp last_activity_at(entry), do: Map.get(entry, :last_codex_timestamp) || Map.get(entry, :started_at)

  defp progress_event?(event) do
    event in [
      :session_started,
      :turn_completed,
      :tool_call_completed,
      :approval_auto_approved,
      :tool_input_auto_answered
    ]
  end

  defp message_signature(nil), do: "nil"
  defp message_signature(message) when is_binary(message), do: String.slice(message, 0, 80)

  defp message_signature(message) when is_map(message) do
    message
    |> Map.take([:event, :message, "event", "method", "type"])
    |> inspect(limit: 5, printable_limit: 80)
  end

  defp message_signature(message), do: inspect(message, limit: 5, printable_limit: 80)
end
```

- [ ] **Step 4: Run the classifier tests**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/run_health_test.exs
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add elixir/lib/symphony_elixir/run_health.ex elixir/test/symphony_elixir/run_health_test.exs
git commit -m "feat: add run health classifier"
```

---

### Task 3: Integrate Health Into Orchestrator Snapshots

**Files:**
- Modify: `elixir/lib/symphony_elixir/orchestrator.ex`
- Modify: `elixir/test/symphony_elixir/orchestrator_status_test.exs`

- [ ] **Step 1: Write failing snapshot tests**

Add tests near the other snapshot tests in `elixir/test/symphony_elixir/orchestrator_status_test.exs`:

```elixir
test "orchestrator snapshot includes run health" do
  issue_id = "issue-health-snapshot"
  issue = %Issue{id: issue_id, identifier: "MT-HEALTH", title: "Health", state: "In Progress"}
  orchestrator_name = Module.concat(__MODULE__, :HealthSnapshotOrchestrator)
  {:ok, pid} = Orchestrator.start_link(name: orchestrator_name)

  on_exit(fn -> if Process.alive?(pid), do: Process.exit(pid, :normal) end)

  now = DateTime.utc_now()
  stale_progress = DateTime.add(now, -700, :second)
  initial_state = :sys.get_state(pid)

  running_entry = %{
    pid: self(),
    ref: make_ref(),
    identifier: issue.identifier,
    issue: issue,
    session_id: "thread-health-turn-health",
    turn_count: 1,
    last_codex_message: %{event: :notification, message: %{}, timestamp: stale_progress},
    last_codex_timestamp: stale_progress,
    last_codex_event: :notification,
    last_meaningful_progress_at: stale_progress,
    last_progress_signature: "notification:%{}",
    repeated_event_count: 0,
    codex_input_tokens: 0,
    codex_output_tokens: 0,
    codex_total_tokens: 0,
    health_last_progress_total_tokens: 0,
    health_last_progress_turn_count: 1,
    started_at: stale_progress
  }

  :sys.replace_state(pid, fn _ ->
    initial_state
    |> Map.put(:running, %{issue_id => running_entry})
    |> Map.put(:claimed, MapSet.put(initial_state.claimed, issue_id))
  end)

  snapshot = GenServer.call(pid, :snapshot)
  assert %{running: [entry]} = snapshot
  assert entry.health.status == :suspect
  assert entry.health.reason == :no_meaningful_progress
  assert entry.health.next_action == :requesting_self_report
  assert is_integer(entry.health.idle_ms)
end

test "orchestrator codex updates refresh health progress metadata" do
  issue_id = "issue-health-update"
  issue = %Issue{id: issue_id, identifier: "MT-HUP", title: "Health update", state: "In Progress"}
  orchestrator_name = Module.concat(__MODULE__, :HealthUpdateOrchestrator)
  {:ok, pid} = Orchestrator.start_link(name: orchestrator_name)

  on_exit(fn -> if Process.alive?(pid), do: Process.exit(pid, :normal) end)

  started_at = DateTime.utc_now()
  initial_state = :sys.get_state(pid)

  running_entry = %{
    pid: self(),
    ref: make_ref(),
    identifier: issue.identifier,
    issue: issue,
    session_id: nil,
    turn_count: 0,
    last_codex_message: nil,
    last_codex_timestamp: nil,
    last_codex_event: nil,
    last_meaningful_progress_at: started_at,
    last_progress_signature: nil,
    repeated_event_count: 0,
    codex_input_tokens: 0,
    codex_output_tokens: 0,
    codex_total_tokens: 0,
    health_last_progress_total_tokens: 0,
    health_last_progress_turn_count: 0,
    started_at: started_at
  }

  :sys.replace_state(pid, fn _ ->
    initial_state
    |> Map.put(:running, %{issue_id => running_entry})
    |> Map.put(:claimed, MapSet.put(initial_state.claimed, issue_id))
  end)

  now = DateTime.utc_now()

  send(pid, {:codex_worker_update, issue_id, %{event: :session_started, session_id: "thread-hup-turn-hup", timestamp: now}})
  Process.sleep(25)

  snapshot = GenServer.call(pid, :snapshot)
  assert %{running: [entry]} = snapshot
  assert entry.health.status == :active
  assert entry.health.reason in [:recent_progress, :turn_progress]
  assert entry.last_meaningful_progress_at == now
  assert entry.repeated_event_count == 0
end
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/orchestrator_status_test.exs
```

Expected: new assertions fail because snapshots do not include `health`.

- [ ] **Step 3: Wire `RunHealth` into orchestrator**

In `elixir/lib/symphony_elixir/orchestrator.ex`, update aliases:

```elixir
alias SymphonyElixir.{AgentRunner, Config, RunHealth, StatusDashboard, Tracker, Workspace}
```

When creating a running entry in `spawn_issue_on_worker_host/5`, add:

```elixir
last_meaningful_progress_at: DateTime.utc_now(),
last_progress_signature: nil,
repeated_event_count: 0,
health_status: :active,
health_reason: :recent_progress,
health_next_action: :watching,
health_last_progress_total_tokens: 0,
health_last_progress_turn_count: 0,
self_report_requested_at: nil,
self_report_deadline_at: nil,
self_report_attempts: 0,
self_report_state: nil,
health_workpad_warning_written?: false
```

Inside `integrate_codex_update/2`, build the merged entry first and then apply run-health progress
helpers before returning:

```elixir
merged_running_entry =
  Map.merge(running_entry, %{
    last_codex_timestamp: timestamp,
    last_codex_message: summarize_codex_update(update),
    session_id: session_id_for_update(running_entry.session_id, update),
    last_codex_event: event,
    codex_app_server_pid: codex_app_server_pid_for_update(codex_app_server_pid, update),
    codex_input_tokens: codex_input_tokens + token_delta.input_tokens,
    codex_output_tokens: codex_output_tokens + token_delta.output_tokens,
    codex_total_tokens: codex_total_tokens + token_delta.total_tokens,
    codex_last_reported_input_tokens: max(last_reported_input, token_delta.input_reported),
    codex_last_reported_output_tokens: max(last_reported_output, token_delta.output_reported),
    codex_last_reported_total_tokens: max(last_reported_total, token_delta.total_reported),
    turn_count: turn_count_for_update(turn_count, running_entry.session_id, update)
  })

updated_running_entry =
  merged_running_entry
  |> update_run_health_progress(update)
  |> evaluate_run_health(DateTime.utc_now())
```

Keep the existing `{updated_running_entry, token_delta}` return shape.

Add helpers:

```elixir
defp update_run_health_progress(running_entry, update) do
  signature = RunHealth.event_signature(running_entry)
  previous_signature = Map.get(running_entry, :last_progress_signature)
  repeated_count = if signature == previous_signature, do: Map.get(running_entry, :repeated_event_count, 0) + 1, else: 0
  progress? = RunHealth.meaningful_progress?(running_entry, Config.settings!().observability.run_health)

  running_entry =
    running_entry
    |> Map.put(:last_progress_signature, signature)
    |> Map.put(:repeated_event_count, repeated_count)

  if progress? do
    %{
      running_entry
      | last_meaningful_progress_at: update.timestamp,
        health_last_progress_total_tokens: Map.get(running_entry, :codex_total_tokens, 0),
        health_last_progress_turn_count: Map.get(running_entry, :turn_count, 0),
        repeated_event_count: 0
    }
  else
    running_entry
  end
end

defp evaluate_run_health(running_entry, now) do
  health = RunHealth.evaluate(running_entry, now, Config.settings!().observability.run_health)

  running_entry
  |> Map.put(:health_status, health.status)
  |> Map.put(:health_reason, health.reason)
  |> Map.put(:health_next_action, health.next_action)
  |> Map.put(:last_meaningful_progress_at, health.last_meaningful_progress_at)
end
```

Add health to snapshot running rows:

```elixir
health = RunHealth.evaluate(metadata, now, Config.settings!().observability.run_health)

%{
  issue_id: issue_id,
  ...
  last_meaningful_progress_at: Map.get(metadata, :last_meaningful_progress_at),
  repeated_event_count: Map.get(metadata, :repeated_event_count, 0),
  health: health,
  runtime_seconds: running_seconds(metadata.started_at, now)
}
```

- [ ] **Step 4: Run orchestrator tests**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/orchestrator_status_test.exs
```

Expected: orchestrator status tests pass.

- [ ] **Step 5: Commit**

```bash
git add elixir/lib/symphony_elixir/orchestrator.ex elixir/test/symphony_elixir/orchestrator_status_test.exs
git commit -m "feat: expose run health in orchestrator snapshots"
```

---

### Task 4: Expose Health Through API and UI

**Files:**
- Modify: `elixir/lib/symphony_elixir_web/presenter.ex`
- Modify: `elixir/test/symphony_elixir/extensions_test.exs`
- Modify: `elixir/lib/symphony_elixir_web/live/dashboard_live.ex`
- Modify: `elixir/priv/static/dashboard.css`
- Modify: `elixir/lib/symphony_elixir/status_dashboard.ex`
- Modify: `elixir/test/symphony_elixir/orchestrator_status_test.exs`
- Modify: `elixir/test/symphony_elixir/status_dashboard_snapshot_test.exs`

- [ ] **Step 1: Write failing API expectations**

Update `static_snapshot/0` in `elixir/test/symphony_elixir/extensions_test.exs` running entry:

```elixir
health: %{
  status: :quiet,
  reason: :quiet,
  next_action: :watching,
  last_meaningful_progress_at: DateTime.utc_now(),
  idle_ms: 301_000,
  details: %{repeated_event_count: 0, self_report_deadline_at: nil}
}
```

Update the expected `/api/v1/state` running row to include:

```elixir
"health" => %{
  "status" => "quiet",
  "reason" => "quiet",
  "next_action" => "watching",
  "last_meaningful_progress_at" => state_payload["running"] |> List.first() |> get_in(["health", "last_meaningful_progress_at"]),
  "idle_ms" => 301_000,
  "details" => %{"repeated_event_count" => 0, "self_report_deadline_at" => nil}
}
```

Update the issue payload top level and `running` map to include the same `health`.

- [ ] **Step 2: Run API tests and verify they fail**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/extensions_test.exs
```

Expected: fails because `Presenter` omits health.

- [ ] **Step 3: Implement presenter health payload**

In `elixir/lib/symphony_elixir_web/presenter.ex`, add `health: health_payload(entry.health)` to `running_entry_payload/1`.

Add `health: health_payload(running.health)` to `issue_payload_body/3` and `running_issue_payload/1`.

Add helper:

```elixir
defp health_payload(nil), do: nil

defp health_payload(%{} = health) do
  %{
    status: health.status && to_string(health.status),
    reason: health.reason && to_string(health.reason),
    next_action: health.next_action && to_string(health.next_action),
    last_meaningful_progress_at: iso8601(health.last_meaningful_progress_at),
    idle_ms: health.idle_ms,
    details: health_details_payload(Map.get(health, :details, %{}))
  }
end

defp health_details_payload(details) when is_map(details) do
  Enum.reduce(details, %{}, fn {key, value}, acc ->
    Map.put(acc, to_string(key), health_detail_value(value))
  end)
end

defp health_details_payload(_details), do: %{}
defp health_detail_value(%DateTime{} = value), do: iso8601(value)
defp health_detail_value(value), do: value
```

- [ ] **Step 4: Add dashboard rendering**

In `elixir/lib/symphony_elixir_web/live/dashboard_live.ex`, add a Health column to the running table:

```heex
<col style="width: 9rem;" />
```

Add header:

```heex
<th>Health</th>
```

Add cell after State:

```heex
<td>
  <div class="health-stack">
    <span class={health_badge_class(entry.health && entry.health.status)}>
      <%= health_label(entry.health) %>
    </span>
    <span class="muted event-meta">
      <%= health_idle(entry.health) %> <%= health_reason(entry.health) %>
    </span>
  </div>
</td>
```

Add helpers:

```elixir
defp health_label(%{status: status}) when is_binary(status), do: String.capitalize(status)
defp health_label(_health), do: "Unknown"

defp health_reason(%{reason: reason}) when is_binary(reason), do: reason
defp health_reason(_health), do: "n/a"

defp health_idle(%{idle_ms: idle_ms}) when is_integer(idle_ms) do
  "idle #{format_runtime_seconds(div(idle_ms, 1_000))}"
end

defp health_idle(_health), do: "idle n/a"

defp health_badge_class(status) do
  base = "health-badge"

  case to_string(status) do
    "active" -> "#{base} health-badge-active"
    "quiet" -> "#{base} health-badge-quiet"
    "suspect" -> "#{base} health-badge-suspect"
    "stalled" -> "#{base} health-badge-stalled"
    _ -> base
  end
end
```

- [ ] **Step 5: Add CSS**

In `elixir/priv/static/dashboard.css`, add compact styles near the badge/table styles:

```css
.health-stack {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  min-width: 0;
}

.health-badge {
  display: inline-flex;
  width: fit-content;
  max-width: 100%;
  border-radius: 999px;
  padding: 0.18rem 0.55rem;
  font-size: 0.74rem;
  font-weight: 700;
  line-height: 1.2;
  background: #eef2f7;
  color: #334155;
}

.health-badge-active {
  background: #dcfce7;
  color: #166534;
}

.health-badge-quiet {
  background: #f1f5f9;
  color: #475569;
}

.health-badge-suspect {
  background: #fef3c7;
  color: #92400e;
}

.health-badge-stalled {
  background: #fee2e2;
  color: #991b1b;
}
```

- [ ] **Step 6: Add terminal rendering**

In `elixir/lib/symphony_elixir/status_dashboard.ex`, update `format_running_summary/2` to include health. Add helpers:

```elixir
defp format_health_summary(%{health: %{status: status, reason: reason, idle_ms: idle_ms}}) do
  "#{humanize_health_status(status)} #{format_health_idle(idle_ms)} #{format_health_reason(reason)}"
  |> String.trim()
end

defp format_health_summary(_entry), do: "Health n/a"

defp humanize_health_status(status) do
  status
  |> to_string()
  |> String.replace("_", " ")
  |> String.capitalize()
end

defp format_health_idle(idle_ms) when is_integer(idle_ms), do: "idle #{format_runtime_seconds(div(idle_ms, 1_000))}"
defp format_health_idle(_idle_ms), do: "idle n/a"

defp format_health_reason(reason), do: reason |> to_string() |> String.replace("_", "-")
```

Add a fixed-width health column:

```elixir
@running_health_width 24
```

Update `fixed_running_width/0` to include `@running_health_width`, update `running_table_header_row/1`
to include `format_cell("HEALTH", @running_health_width)`, and add
`format_cell(format_health_summary(running_entry), @running_health_width)` to
`format_running_summary/2` before the event cell.

- [ ] **Step 7: Run UI/API tests**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/extensions_test.exs test/symphony_elixir/orchestrator_status_test.exs test/symphony_elixir/status_dashboard_snapshot_test.exs
```

Expected: tests pass after updating affected terminal snapshots with the project’s snapshot helper if required by existing test output.

- [ ] **Step 8: Commit**

```bash
git add elixir/lib/symphony_elixir_web/presenter.ex elixir/test/symphony_elixir/extensions_test.exs elixir/lib/symphony_elixir_web/live/dashboard_live.ex elixir/priv/static/dashboard.css elixir/lib/symphony_elixir/status_dashboard.ex elixir/test/symphony_elixir/orchestrator_status_test.exs elixir/test/symphony_elixir/status_dashboard_snapshot_test.exs elixir/test/fixtures/status_dashboard_snapshots
git commit -m "feat: show run health in observability surfaces"
```

---

### Task 5: Add Tracker Workpad Section Upsert

**Files:**
- Modify: `elixir/lib/symphony_elixir/tracker.ex`
- Modify: `elixir/lib/symphony_elixir/tracker/memory.ex`
- Modify: `elixir/lib/symphony_elixir/linear/adapter.ex`
- Modify: `elixir/test/symphony_elixir/extensions_test.exs`

- [ ] **Step 1: Write failing tracker delegation tests**

In `elixir/test/symphony_elixir/extensions_test.exs`, add to `FakeLinearClient` handling:

```elixir
def upsert_workpad_section(issue_id, section_key, markdown) do
  send(self(), {:fake_upsert_workpad_section, issue_id, section_key, markdown})
  Process.get({__MODULE__, :upsert_result}, :ok)
end
```

Add test:

```elixir
test "tracker delegates workpad section upserts" do
  Application.put_env(:symphony_elixir, :linear_client_module, FakeLinearClient)
  write_workflow_file!(Workflow.workflow_file_path(), tracker_kind: "linear")

  assert :ok =
           Tracker.upsert_workpad_section(
             "issue-health",
             "run-health-warning",
             "### Run Health Warning\n\n- Status: Suspect\n"
           )
end
```

Add adapter-level tests using fake GraphQL responses:

```elixir
test "linear adapter creates missing workpad comment for section upsert" do
  Application.put_env(:symphony_elixir, :linear_client_module, FakeLinearClient)

  Process.put(
    {FakeLinearClient, :graphql_results},
    [
      {:ok, %{"data" => %{"issue" => %{"comments" => %{"nodes" => []}}}}},
      {:ok, %{"data" => %{"commentCreate" => %{"success" => true}}}}
    ]
  )

  assert :ok = Adapter.upsert_workpad_section("issue-health", "run-health-warning", "### Run Health Warning\n\n- Status: Suspect\n")
end
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/extensions_test.exs
```

Expected: compile failure because callback/facade is missing.

- [ ] **Step 3: Add tracker callback and memory implementation**

In `elixir/lib/symphony_elixir/tracker.ex`, add callback:

```elixir
@callback upsert_workpad_section(String.t(), String.t(), String.t()) :: :ok | {:error, term()}
```

Add facade:

```elixir
@spec upsert_workpad_section(String.t(), String.t(), String.t()) :: :ok | {:error, term()}
def upsert_workpad_section(issue_id, section_key, markdown) do
  adapter().upsert_workpad_section(issue_id, section_key, markdown)
end
```

In `elixir/lib/symphony_elixir/tracker/memory.ex`, add:

```elixir
@impl true
def upsert_workpad_section(issue_id, section_key, markdown)
    when is_binary(issue_id) and is_binary(section_key) and is_binary(markdown) do
  if recipient = Application.get_env(:symphony_elixir, :memory_tracker_recipient) do
    send(recipient, {:memory_workpad_upsert, issue_id, section_key, markdown})
  end

  :ok
end
```

- [ ] **Step 4: Add Linear adapter implementation**

In `elixir/lib/symphony_elixir/linear/adapter.ex`, add query/mutations:

```elixir
@workpad_comments_query """
query SymphonyWorkpadComments($issueId: String!) {
  issue(id: $issueId) {
    comments(first: 50) {
      nodes {
        id
        body
      }
    }
  }
}
"""

@update_comment_mutation """
mutation SymphonyUpdateComment($id: String!, $body: String!) {
  commentUpdate(id: $id, input: {body: $body}) {
    success
  }
}
"""
```

Add public function:

```elixir
@spec upsert_workpad_section(String.t(), String.t(), String.t()) :: :ok | {:error, term()}
def upsert_workpad_section(issue_id, section_key, markdown)
    when is_binary(issue_id) and is_binary(section_key) and is_binary(markdown) do
  with {:ok, response} <- client_module().graphql(@workpad_comments_query, %{issueId: issue_id}),
       {:ok, comment_id, body} <- find_or_build_workpad(response),
       updated_body <- upsert_section(body, section_key, markdown),
       :ok <- write_workpad_comment(issue_id, comment_id, updated_body) do
    :ok
  end
end
```

Add helpers:

```elixir
defp find_or_build_workpad(response) do
  comments = get_in(response, ["data", "issue", "comments", "nodes"]) || []

  case Enum.find(comments, &(String.contains?(Map.get(&1, "body", ""), "## Codex Workpad"))) do
    %{"id" => id, "body" => body} when is_binary(id) and is_binary(body) ->
      {:ok, id, body}

    _ ->
      {:ok, nil, "## Codex Workpad\n"}
  end
end

defp upsert_section(body, section_key, markdown) do
  start_marker = "<!-- symphony:#{section_key}:start -->"
  end_marker = "<!-- symphony:#{section_key}:end -->"
  section = "#{start_marker}\n#{String.trim(markdown)}\n#{end_marker}"
  pattern = ~r/<!-- symphony:#{Regex.escape(section_key)}:start -->.*?<!-- symphony:#{Regex.escape(section_key)}:end -->/s

  if Regex.match?(pattern, body) do
    Regex.replace(pattern, body, section)
  else
    String.trim_trailing(body) <> "\n\n" <> section <> "\n"
  end
end

defp write_workpad_comment(issue_id, nil, body), do: create_comment(issue_id, body)

defp write_workpad_comment(_issue_id, comment_id, body) do
  with {:ok, response} <- client_module().graphql(@update_comment_mutation, %{id: comment_id, body: body}),
       true <- get_in(response, ["data", "commentUpdate", "success"]) == true do
    :ok
  else
    false -> {:error, :comment_update_failed}
    {:error, reason} -> {:error, reason}
    _ -> {:error, :comment_update_failed}
  end
end
```

- [ ] **Step 5: Run tracker tests**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/extensions_test.exs
```

Expected: tests pass.

- [ ] **Step 6: Commit**

```bash
git add elixir/lib/symphony_elixir/tracker.ex elixir/lib/symphony_elixir/tracker/memory.ex elixir/lib/symphony_elixir/linear/adapter.ex elixir/test/symphony_elixir/extensions_test.exs
git commit -m "feat: add workpad section upsert"
```

---

### Task 6: Add Self-Report Request and Early Retry

**Files:**
- Modify: `elixir/lib/symphony_elixir/orchestrator.ex`
- Modify: `elixir/lib/symphony_elixir/agent_runner.ex`
- Modify: `elixir/test/symphony_elixir/orchestrator_status_test.exs`

- [ ] **Step 1: Write failing orchestrator tests**

Add tests in `elixir/test/symphony_elixir/orchestrator_status_test.exs`:

```elixir
test "suspect run requests self-report and writes one workpad warning" do
  write_workflow_file!(Workflow.workflow_file_path(),
    tracker_kind: "memory",
    observability_run_health_quiet_after_ms: 100,
    observability_run_health_suspect_after_ms: 200,
    observability_run_health_self_report_timeout_ms: 1_000
  )

  Application.put_env(:symphony_elixir, :memory_tracker_recipient, self())

  issue_id = "issue-self-report"
  issue = %Issue{id: issue_id, identifier: "MT-SR", title: "Self report", state: "In Progress"}
  orchestrator_name = Module.concat(__MODULE__, :SelfReportOrchestrator)
  {:ok, pid} = Orchestrator.start_link(name: orchestrator_name)

  on_exit(fn -> if Process.alive?(pid), do: Process.exit(pid, :normal) end)

  worker_pid =
    spawn(fn ->
      receive do
        {:symphony_control, :request_self_report, payload} -> send(self(), {:unexpected_self_report_payload, payload})
        :done -> :ok
      end
    end)

  stale_at = DateTime.add(DateTime.utc_now(), -5, :second)
  initial_state = :sys.get_state(pid)

  running_entry = %{
    pid: worker_pid,
    ref: make_ref(),
    identifier: issue.identifier,
    issue: issue,
    session_id: "thread-self-report-turn",
    turn_count: 1,
    last_codex_message: %{event: :notification, message: %{}, timestamp: stale_at},
    last_codex_timestamp: stale_at,
    last_codex_event: :notification,
    last_meaningful_progress_at: stale_at,
    repeated_event_count: 0,
    codex_total_tokens: 0,
    health_last_progress_total_tokens: 0,
    health_last_progress_turn_count: 1,
    self_report_state: nil,
    self_report_attempts: 0,
    started_at: stale_at
  }

  :sys.replace_state(pid, fn _ ->
    initial_state
    |> Map.put(:running, %{issue_id => running_entry})
    |> Map.put(:claimed, MapSet.put(initial_state.claimed, issue_id))
  end)

  send(pid, :tick)
  Process.sleep(100)

  state = :sys.get_state(pid)
  updated = state.running[issue_id]

  assert updated.self_report_state == :requested
  assert updated.self_report_attempts == 1
  assert %DateTime{} = updated.self_report_deadline_at

  assert_receive {:memory_workpad_upsert, ^issue_id, "run-health-warning", markdown}, 1_000
  assert markdown =~ "Run Health Warning"
  assert markdown =~ "Suspect"
end

test "self-report deadline schedules early retry" do
  write_workflow_file!(Workflow.workflow_file_path(),
    tracker_kind: "memory",
    observability_run_health_quiet_after_ms: 100,
    observability_run_health_suspect_after_ms: 200,
    observability_run_health_self_report_timeout_ms: 100
  )

  issue_id = "issue-self-report-timeout"
  issue = %Issue{id: issue_id, identifier: "MT-SRT", title: "Self report timeout", state: "In Progress"}
  orchestrator_name = Module.concat(__MODULE__, :SelfReportTimeoutOrchestrator)
  {:ok, pid} = Orchestrator.start_link(name: orchestrator_name)

  on_exit(fn -> if Process.alive?(pid), do: Process.exit(pid, :normal) end)

  worker_pid =
    spawn(fn ->
      receive do
        :done -> :ok
      end
    end)

  stale_at = DateTime.add(DateTime.utc_now(), -5, :second)
  deadline = DateTime.add(DateTime.utc_now(), -1, :second)
  initial_state = :sys.get_state(pid)

  running_entry = %{
    pid: worker_pid,
    ref: make_ref(),
    identifier: issue.identifier,
    issue: issue,
    session_id: "thread-self-report-timeout-turn",
    turn_count: 1,
    last_codex_message: %{event: :notification, message: %{}, timestamp: stale_at},
    last_codex_timestamp: stale_at,
    last_codex_event: :notification,
    last_meaningful_progress_at: stale_at,
    repeated_event_count: 0,
    codex_total_tokens: 0,
    health_last_progress_total_tokens: 0,
    health_last_progress_turn_count: 1,
    self_report_state: :requested,
    self_report_attempts: 1,
    self_report_requested_at: DateTime.add(DateTime.utc_now(), -2, :second),
    self_report_deadline_at: deadline,
    started_at: stale_at
  }

  :sys.replace_state(pid, fn _ ->
    initial_state
    |> Map.put(:running, %{issue_id => running_entry})
    |> Map.put(:claimed, MapSet.put(initial_state.claimed, issue_id))
  end)

  send(pid, :tick)
  Process.sleep(100)
  state = :sys.get_state(pid)

  refute Process.alive?(worker_pid)
  refute Map.has_key?(state.running, issue_id)
  assert %{attempt: 1, error: "self-report missing" <> _} = state.retry_attempts[issue_id]
end
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/orchestrator_status_test.exs
```

Expected: tests fail because suspect health has no self-report side effects.

- [ ] **Step 3: Add self-report orchestration**

In `elixir/lib/symphony_elixir/orchestrator.ex`, update `reconcile_stalled_running_issues/1` to evaluate run health before hard stall:

```elixir
state =
  Enum.reduce(state.running, state, fn {issue_id, running_entry}, state_acc ->
    running_entry = evaluate_run_health(running_entry, now)
    state_acc = put_running_entry(state_acc, issue_id, running_entry)
    handle_run_health_action(state_acc, issue_id, running_entry, now)
  end)
```

Add helper:

```elixir
defp put_running_entry(%State{} = state, issue_id, running_entry) do
  %{state | running: Map.put(state.running, issue_id, running_entry)}
end
```

Add action handling:

```elixir
defp handle_run_health_action(state, issue_id, %{health_status: :stalled} = running_entry, _now) do
  restart_self_report_stalled_issue(state, issue_id, running_entry)
end

defp handle_run_health_action(state, issue_id, %{health_status: :suspect} = running_entry, now) do
  maybe_request_self_report(state, issue_id, running_entry, now)
end

defp handle_run_health_action(state, _issue_id, _running_entry, _now), do: state
```

Add self-report request:

```elixir
defp maybe_request_self_report(state, issue_id, running_entry, now) do
  if Map.get(running_entry, :self_report_state) == :requested do
    state
  else
    config = Config.settings!().observability.run_health
    deadline = DateTime.add(now, config.self_report_timeout_ms, :millisecond)
    payload = self_report_payload(issue_id, running_entry, deadline)

    deliver_self_report_request(Map.get(running_entry, :pid), payload)
    write_run_health_warning(issue_id, running_entry, "Requested agent self-report", deadline)

    updated =
      running_entry
      |> Map.put(:self_report_state, :requested)
      |> Map.put(:self_report_requested_at, now)
      |> Map.put(:self_report_deadline_at, deadline)
      |> Map.update(:self_report_attempts, 1, &(&1 + 1))
      |> Map.put(:health_next_action, :requesting_self_report)

    put_running_entry(state, issue_id, updated)
  end
end
```

Add payload and delivery helpers:

```elixir
defp self_report_payload(issue_id, running_entry, deadline) do
  %{
    issue_id: issue_id,
    issue_identifier: Map.get(running_entry, :identifier),
    reason: Map.get(running_entry, :health_reason),
    last_meaningful_progress_at: Map.get(running_entry, :last_meaningful_progress_at),
    deadline_at: deadline
  }
end

defp deliver_self_report_request(pid, payload) when is_pid(pid) do
  send(pid, {:symphony_control, :request_self_report, payload})
  :ok
end

defp deliver_self_report_request(_pid, _payload), do: :ok
```

Add retry:

```elixir
defp restart_self_report_stalled_issue(state, issue_id, running_entry) do
  identifier = Map.get(running_entry, :identifier, issue_id)
  reason = Map.get(running_entry, :health_reason, :self_report_missing)

  write_run_health_warning(issue_id, running_entry, "Self-report missing; retrying early", Map.get(running_entry, :self_report_deadline_at))

  state
  |> terminate_running_issue(issue_id, false)
  |> schedule_issue_retry(issue_id, next_retry_attempt_from_running(running_entry), %{
    identifier: identifier,
    error: "self-report missing after suspect run health: #{reason}",
    worker_host: Map.get(running_entry, :worker_host),
    workspace_path: Map.get(running_entry, :workspace_path)
  })
end
```

Add Workpad warning:

```elixir
defp write_run_health_warning(issue_id, running_entry, action, deadline) do
  markdown = """
  ### Run Health Warning

  - Status: #{human_health(Map.get(running_entry, :health_status))}
  - Reason: #{Map.get(running_entry, :health_reason)}
  - Last meaningful progress: #{iso8601(Map.get(running_entry, :last_meaningful_progress_at))}
  - Action: #{action}
  - Retry policy: Will retry early if no useful report arrives by #{iso8601(deadline)}
  """

  case Tracker.upsert_workpad_section(issue_id, "run-health-warning", markdown) do
    :ok -> :ok
    {:error, reason} -> Logger.warning("Failed to update run health Workpad warning for issue_id=#{issue_id}: #{inspect(reason)}")
  end
end

defp human_health(value), do: value |> to_string() |> String.capitalize()
defp iso8601(%DateTime{} = dt), do: DateTime.to_iso8601(dt)
defp iso8601(_), do: "n/a"
```

- [ ] **Step 4: Add AgentRunner deferred control receive**

In `elixir/lib/symphony_elixir/agent_runner.ex`, before building continuation prompts, drain control messages:

```elixir
defp do_run_codex_turns(app_session, workspace, issue, codex_update_recipient, opts, issue_state_fetcher, turn_number, max_turns) do
  opts = collect_control_messages(opts)
  prompt = build_turn_prompt(issue, opts, turn_number, max_turns)
  ...
end
```

Add helpers:

```elixir
defp collect_control_messages(opts) do
  receive do
    {:symphony_control, :request_self_report, payload} ->
      pending = Keyword.get(opts, :pending_self_reports, [])
      collect_control_messages(Keyword.put(opts, :pending_self_reports, pending ++ [payload]))
  after
    0 ->
      opts
  end
end
```

Update continuation prompt:

```elixir
defp build_turn_prompt(_issue, opts, turn_number, max_turns) do
  self_report_guidance = pending_self_report_guidance(Keyword.get(opts, :pending_self_reports, []))

  """
  Continuation guidance:

  - The previous Codex turn completed normally, but the Linear issue is still in an active state.
  - This is continuation turn ##{turn_number} of #{max_turns} for the current agent run.
  - Resume from the current workspace and workpad state instead of restarting from scratch.
  - The original task instructions and prior turn context are already present in this thread, so do not restate them before acting.
  - Focus on the remaining ticket work and do not end the turn while the issue stays active unless you are truly blocked.
  #{self_report_guidance}
  """
end

defp pending_self_report_guidance([]), do: ""

defp pending_self_report_guidance([latest | _]) do
  """

  Run health self-report request:

  - Symphony marked this run as suspicious because #{latest[:reason]}.
  - Briefly report current status, what changed since the last visible progress, what is taking time, and what you will do next.
  - Do not create extra Linear comments; Symphony owns the run-health Workpad warning.
  """
end
```

- [ ] **Step 5: Run tests**

Run:

```bash
cd elixir
mise exec -- mix test test/symphony_elixir/orchestrator_status_test.exs test/symphony_elixir/core_test.exs
```

Expected: tests pass.

- [ ] **Step 6: Commit**

```bash
git add elixir/lib/symphony_elixir/orchestrator.ex elixir/lib/symphony_elixir/agent_runner.ex elixir/test/symphony_elixir/orchestrator_status_test.exs elixir/test/symphony_elixir/core_test.exs
git commit -m "feat: retry suspicious runs after self-report timeout"
```

---

### Task 7: Final Verification

**Files:**
- Modify only if verification exposes defects.

- [ ] **Step 1: Run focused test suite**

Run:

```bash
cd elixir
mise exec -- mix test \
  test/symphony_elixir/run_health_test.exs \
  test/symphony_elixir/workspace_and_config_test.exs \
  test/symphony_elixir/orchestrator_status_test.exs \
  test/symphony_elixir/extensions_test.exs \
  test/symphony_elixir/status_dashboard_snapshot_test.exs
```

Expected: all focused tests pass.

- [ ] **Step 2: Run full Elixir test suite**

Run:

```bash
cd elixir
mise exec -- mix test
```

Expected: all non-live tests pass.

- [ ] **Step 3: Run formatting and static checks**

Run:

```bash
cd elixir
mise exec -- mix format --check-formatted
mise exec -- mix credo --strict
```

Expected: both commands pass. If `mix credo --strict` is not available in this project, run:

```bash
cd elixir
mise exec -- mix compile --warnings-as-errors
```

Expected: compilation succeeds without warnings.

- [ ] **Step 4: Inspect final diff**

Run:

```bash
git status --short
git log --oneline --max-count=8
git diff --stat main...HEAD
```

Expected: only files listed in this plan changed, and commits are grouped by the task boundaries above.

- [ ] **Step 5: Final commit if verification required fixes**

If Step 1, 2, or 3 required code changes, commit them:

```bash
git add elixir
git commit -m "fix: stabilize run health watcher"
```

If no files changed after verification, do not create an empty commit.
