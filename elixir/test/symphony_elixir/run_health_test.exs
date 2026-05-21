defmodule SymphonyElixir.RunHealthTest do
  use ExUnit.Case, async: true

  alias SymphonyElixir.Config.Schema
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
      Map.new(overrides)
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
      Map.new(overrides)
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
    assert health.next_action == :requesting_self_report
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

  test "expired self-report with token progress is active" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)

    health =
      RunHealth.evaluate(
        entry(
          last_meaningful_progress_at: progress_at,
          self_report_deadline_at: DateTime.add(now, -60, :second),
          self_report_state: :requested,
          codex_total_tokens: 1_000,
          health_last_progress_total_tokens: 100
        ),
        now,
        config()
      )

    assert health.status == :active
    assert health.reason == :token_progress
    assert health.next_action == :watching
  end

  test "expired self-report with turn progress is active" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)

    health =
      RunHealth.evaluate(
        entry(
          last_meaningful_progress_at: progress_at,
          self_report_deadline_at: DateTime.add(now, -60, :second),
          self_report_state: :requested,
          turn_count: 2,
          health_last_progress_turn_count: 1
        ),
        now,
        config()
      )

    assert health.status == :active
    assert health.reason == :turn_progress
    assert health.next_action == :watching
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

  test "turn increase counts as meaningful progress" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)

    health =
      RunHealth.evaluate(
        entry(
          last_meaningful_progress_at: progress_at,
          turn_count: 2,
          health_last_progress_turn_count: 1
        ),
        now,
        config()
      )

    assert health.status == :active
    assert health.reason == :turn_progress
    assert health.last_meaningful_progress_at == now
  end

  test "command and file-change activity count as meaningful progress" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)

    command_entry =
      entry(
        last_meaningful_progress_at: progress_at,
        last_codex_timestamp: now,
        last_codex_event: :notification,
        last_codex_message: %{
          event: :notification,
          message: %{
            "payload" => %{
              "method" => "item/commandExecution/outputDelta",
              "params" => %{"outputDelta" => "still running"}
            }
          },
          timestamp: now
        }
      )

    assert RunHealth.meaningful_progress?(command_entry, config())

    command_health = RunHealth.evaluate(command_entry, now, config())
    assert command_health.status == :active
    assert command_health.reason == :codex_activity

    file_change_entry =
      entry(
        last_meaningful_progress_at: progress_at,
        last_codex_timestamp: now,
        last_codex_event: :notification,
        last_codex_message: %{
          event: :notification,
          message: %{
            "payload" => %{
              "method" => "item/completed",
              "params" => %{"item" => %{"type" => "fileChange", "status" => "completed"}}
            }
          },
          timestamp: now
        }
      )

    assert RunHealth.meaningful_progress?(file_change_entry, config())

    file_change_health = RunHealth.evaluate(file_change_entry, now, config())
    assert file_change_health.status == :active
    assert file_change_health.reason == :codex_activity
  end

  test "dynamic tool completion counts as meaningful progress" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)

    tool_entry =
      entry(
        last_meaningful_progress_at: progress_at,
        last_codex_timestamp: now,
        last_codex_event: :tool_call_completed,
        last_codex_message: %{
          event: :tool_call_completed,
          message: %{
            "payload" => %{
              "method" => "item/tool/call",
              "params" => %{"tool" => "linear_graphql"}
            }
          },
          timestamp: now
        }
      )

    assert RunHealth.meaningful_progress?(tool_entry, config())

    health = RunHealth.evaluate(tool_entry, now, config())
    assert health.status == :active
    assert health.reason == :codex_activity
  end

  test "repeated dynamic tool completion still counts as meaningful progress" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)

    tool_entry =
      entry(
        last_meaningful_progress_at: progress_at,
        last_progress_signature: "tool_call_completed",
        last_codex_timestamp: now,
        last_codex_event: :tool_call_completed,
        last_codex_message: %{
          event: :tool_call_completed,
          message: %{
            "payload" => %{
              "method" => "item/tool/call",
              "params" => %{"tool" => "linear_graphql"}
            }
          },
          timestamp: now
        }
      )

    assert RunHealth.meaningful_progress?(tool_entry, config())

    health = RunHealth.evaluate(tool_entry, now, config())
    assert health.status == :active
    assert health.reason == :codex_activity
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

  test "supports Ecto run health config structs" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)
    config = struct(Schema.RunHealth, config())

    health = RunHealth.evaluate(entry(last_meaningful_progress_at: progress_at), now, config)

    assert health.status == :suspect
    assert health.reason == :no_meaningful_progress
  end

  test "nil numeric counters do not crash classification" do
    now = DateTime.utc_now()

    health =
      RunHealth.evaluate(
        entry(
          codex_total_tokens: nil,
          health_last_progress_total_tokens: nil,
          turn_count: nil,
          health_last_progress_turn_count: nil,
          repeated_event_count: nil,
          self_report_attempts: nil,
          last_meaningful_progress_at: now
        ),
        now,
        config()
      )

    assert health.status == :active
    assert health.reason == :recent_progress
  end

  test "sparse config map uses plan defaults" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)

    health =
      RunHealth.evaluate(
        entry(last_meaningful_progress_at: progress_at),
        now,
        %{enabled: true}
      )

    assert health.status == :suspect
    assert health.reason == :no_meaningful_progress
  end

  test "event_signature returns compact stable signatures" do
    assert RunHealth.event_signature(entry(last_codex_event: :session_started)) == "session_started"

    assert RunHealth.event_signature(entry(last_codex_event: "agent_message", last_codex_message: %{type: "agent_message"})) == "agent_message"
  end

  test "meaningful_progress? detects token and turn progress" do
    assert RunHealth.meaningful_progress?(
             entry(codex_total_tokens: 1_000, health_last_progress_total_tokens: 100),
             config(min_token_progress_delta: 500)
           )

    assert RunHealth.meaningful_progress?(
             entry(turn_count: 2, health_last_progress_turn_count: 1),
             config()
           )

    refute RunHealth.meaningful_progress?(entry(), config())
  end

  test "zero token progress threshold still requires positive token delta" do
    now = DateTime.utc_now()
    progress_at = DateTime.add(now, -700, :second)

    health =
      RunHealth.evaluate(
        entry(
          last_meaningful_progress_at: progress_at,
          codex_total_tokens: 100,
          health_last_progress_total_tokens: 100
        ),
        now,
        config(min_token_progress_delta: 0)
      )

    assert health.status == :suspect
    assert health.reason == :no_meaningful_progress
    refute RunHealth.meaningful_progress?(entry(), config(min_token_progress_delta: 0))
  end
end
