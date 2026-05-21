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

  @spec evaluate(map() | struct(), DateTime.t(), map() | struct()) :: health()
  def evaluate(running_entry, now, config) do
    last_meaningful_progress_at = get(running_entry, :last_meaningful_progress_at)
    idle_ms = idle_ms(last_meaningful_progress_at, now)

    cond do
      not config_enabled?(config) ->
        health(:active, :disabled, :watching, last_meaningful_progress_at, idle_ms, %{})

      self_report_missing?(running_entry, now) ->
        health(:stalled, :self_report_missing, :retrying_soon, last_meaningful_progress_at, idle_ms, %{
          self_report_deadline_at: get(running_entry, :self_report_deadline_at),
          self_report_attempts: get(running_entry, :self_report_attempts, 0)
        })

      token_progress?(running_entry, config) ->
        health(:active, :token_progress, :watching, now, 0, %{
          token_delta: token_delta(running_entry)
        })

      turn_progress?(running_entry) ->
        health(:active, :turn_progress, :watching, now, 0, %{
          turn_delta: turn_delta(running_entry)
        })

      repeated_same_event?(running_entry, config) ->
        health(:suspect, :repeated_same_event, :requesting_self_report, last_meaningful_progress_at, idle_ms, %{
          repeated_event_count: get(running_entry, :repeated_event_count, 0),
          repeated_event_suspect_count: get(config, :repeated_event_suspect_count)
        })

      idle_ms == nil or idle_ms < get(config, :quiet_after_ms) ->
        health(:active, :recent_progress, :watching, last_meaningful_progress_at, idle_ms, %{})

      idle_ms < get(config, :suspect_after_ms) ->
        health(:quiet, :quiet, :watching, last_meaningful_progress_at, idle_ms, %{
          quiet_after_ms: get(config, :quiet_after_ms),
          suspect_after_ms: get(config, :suspect_after_ms)
        })

      true ->
        health(:suspect, :no_meaningful_progress, :requesting_self_report, last_meaningful_progress_at, idle_ms, %{
          suspect_after_ms: get(config, :suspect_after_ms)
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
    token_delta(entry) >= get(config, :min_token_progress_delta, 0)
  end

  defp token_delta(entry) do
    get(entry, :codex_total_tokens, 0) - get(entry, :health_last_progress_total_tokens, 0)
  end

  defp turn_progress?(entry) do
    get(entry, :turn_count, 0) > get(entry, :health_last_progress_turn_count, 0)
  end

  defp turn_delta(entry) do
    get(entry, :turn_count, 0) - get(entry, :health_last_progress_turn_count, 0)
  end

  defp repeated_same_event?(entry, config) do
    get(entry, :repeated_event_count, 0) >= get(config, :repeated_event_suspect_count)
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

  defp get(map_or_struct, key, default \\ nil)

  defp get(map_or_struct, key, default) when is_map(map_or_struct) do
    Map.get(map_or_struct, key, Map.get(map_or_struct, Atom.to_string(key), default))
  end

  defp get(_map_or_struct, _key, default), do: default
end
