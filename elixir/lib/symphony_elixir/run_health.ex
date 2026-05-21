defmodule SymphonyElixir.RunHealth do
  @moduledoc """
  Pure run health classification helpers.
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

  @default_config %{
    enabled: true,
    quiet_after_ms: 300_000,
    suspect_after_ms: 600_000,
    self_report_timeout_ms: 120_000,
    early_retry_on_self_report_failure: true,
    min_token_progress_delta: 500,
    repeated_event_suspect_count: 10
  }

  @spec evaluate(map() | struct(), DateTime.t(), map() | struct()) :: health()
  def evaluate(running_entry, now, config) do
    last_meaningful_progress_at = get(running_entry, :last_meaningful_progress_at)
    idle_ms = idle_ms(last_meaningful_progress_at, now)

    cond do
      not config_enabled?(config) ->
        health(:active, :disabled, :watching, last_meaningful_progress_at, idle_ms, %{})

      token_progress?(running_entry, config) ->
        health(:active, :token_progress, :watching, now, 0, %{
          token_delta: token_delta(running_entry)
        })

      turn_progress?(running_entry) ->
        health(:active, :turn_progress, :watching, now, 0, %{
          turn_delta: turn_delta(running_entry)
        })

      self_report_missing?(running_entry, now) ->
        health(:stalled, :self_report_missing, :retrying_soon, last_meaningful_progress_at, idle_ms, %{
          self_report_deadline_at: get(running_entry, :self_report_deadline_at),
          self_report_attempts: integer(running_entry, :self_report_attempts)
        })

      repeated_same_event?(running_entry, config) ->
        health(:suspect, :repeated_same_event, :requesting_self_report, last_meaningful_progress_at, idle_ms, %{
          repeated_event_count: integer(running_entry, :repeated_event_count),
          repeated_event_suspect_count: config_integer(config, :repeated_event_suspect_count)
        })

      idle_ms == nil or idle_ms < config_integer(config, :quiet_after_ms) ->
        health(:active, :recent_progress, :watching, last_meaningful_progress_at, idle_ms, %{})

      idle_ms < config_integer(config, :suspect_after_ms) ->
        health(:quiet, :quiet, :watching, last_meaningful_progress_at, idle_ms, %{
          quiet_after_ms: config_integer(config, :quiet_after_ms),
          suspect_after_ms: config_integer(config, :suspect_after_ms)
        })

      true ->
        health(:suspect, :no_meaningful_progress, :requesting_self_report, last_meaningful_progress_at, idle_ms, %{
          suspect_after_ms: config_integer(config, :suspect_after_ms)
        })
    end
  end

  @spec event_signature(map() | struct()) :: String.t()
  def event_signature(entry) do
    event =
      get(entry, :last_codex_event) ||
        entry
        |> get(:last_codex_message, %{})
        |> message_event()

    event
    |> normalize_part()
    |> case do
      "" -> "unknown"
      signature -> signature
    end
  end

  @spec meaningful_progress?(map() | struct(), map() | struct()) :: boolean()
  def meaningful_progress?(entry, config) do
    token_progress?(entry, config) or turn_progress?(entry)
  end

  defp health(status, reason, next_action, last_meaningful_progress_at, idle_ms, details) do
    %{
      status: status,
      reason: reason,
      next_action: next_action,
      last_meaningful_progress_at: last_meaningful_progress_at,
      idle_ms: idle_ms,
      details: details
    }
  end

  defp config_enabled?(config), do: get(config, :enabled, true)

  defp self_report_missing?(entry, now) do
    get(entry, :self_report_state) == :requested and deadline_passed?(get(entry, :self_report_deadline_at), now)
  end

  defp deadline_passed?(%DateTime{} = deadline, now) do
    DateTime.compare(deadline, now) in [:lt, :eq]
  end

  defp deadline_passed?(_deadline, _now), do: false

  defp token_progress?(entry, config) do
    delta = token_delta(entry)

    delta > 0 and delta >= config_integer(config, :min_token_progress_delta)
  end

  defp token_delta(entry) do
    integer(entry, :codex_total_tokens) - integer(entry, :health_last_progress_total_tokens)
  end

  defp turn_progress?(entry) do
    integer(entry, :turn_count) > integer(entry, :health_last_progress_turn_count)
  end

  defp turn_delta(entry) do
    integer(entry, :turn_count) - integer(entry, :health_last_progress_turn_count)
  end

  defp repeated_same_event?(entry, config) do
    integer(entry, :repeated_event_count) >= config_integer(config, :repeated_event_suspect_count)
  end

  defp idle_ms(nil, _now), do: nil

  defp idle_ms(%DateTime{} = last_meaningful_progress_at, %DateTime{} = now) do
    max(DateTime.diff(now, last_meaningful_progress_at, :millisecond), 0)
  end

  defp message_event(message) when is_map(message) do
    get(message, :event) || get(message, :type)
  end

  defp message_event(_message), do: nil

  defp normalize_part(value) when is_atom(value), do: Atom.to_string(value)
  defp normalize_part(value) when is_binary(value), do: value
  defp normalize_part(value), do: inspect(value)

  defp integer(map_or_struct, key) do
    case get(map_or_struct, key) do
      value when is_integer(value) -> value
      _value -> 0
    end
  end

  defp config_integer(config, key) do
    case get(config, key, Map.fetch!(@default_config, key)) do
      value when is_integer(value) -> value
      _value -> Map.fetch!(@default_config, key)
    end
  end

  defp get(map_or_struct, key, default \\ nil)

  defp get(map_or_struct, key, default) when is_map(map_or_struct) do
    map_or_struct
    |> Map.get(key, Map.get(map_or_struct, Atom.to_string(key), default))
    |> case do
      nil -> default
      value -> value
    end
  end

  defp get(_map_or_struct, _key, default), do: default
end
