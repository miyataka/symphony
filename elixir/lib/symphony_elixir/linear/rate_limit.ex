defmodule SymphonyElixir.Linear.RateLimit do
  @moduledoc """
  Shared Linear API rate-limit guard.
  """

  @default_retry_ms 60_000
  @state_key {__MODULE__, :blocked_until_ms}

  @type limited_reason :: {:linear_rate_limited, %{retry_after_ms: non_neg_integer(), reset_at_ms: integer()}}

  @spec check() :: :ok | {:error, limited_reason()}
  def check, do: check(System.system_time(:millisecond))

  @doc false
  @spec check(integer()) :: :ok | {:error, limited_reason()}
  def check(now_ms) when is_integer(now_ms) do
    case :persistent_term.get(@state_key, nil) do
      reset_at_ms when is_integer(reset_at_ms) and reset_at_ms > now_ms ->
        {:error, limited_reason(reset_at_ms, now_ms)}

      _ ->
        :ok
    end
  end

  @spec record_response(map()) :: :ok | {:error, limited_reason()}
  def record_response(response), do: record_response(response, System.system_time(:millisecond))

  @doc false
  @spec record_response(map(), integer()) :: :ok | {:error, limited_reason()}
  def record_response(response, now_ms) when is_map(response) and is_integer(now_ms) do
    headers = Map.get(response, :headers) || Map.get(response, "headers") || []

    case limited_response?(response, headers) do
      true ->
        limit_until(reset_from_headers(headers, now_ms), now_ms)

      {:exhausted, reset_at_ms} ->
        limit_until(reset_at_ms, now_ms)

      false ->
        :ok
    end
  end

  @doc false
  @spec clear() :: :ok
  def clear do
    :persistent_term.erase(@state_key)
    :ok
  end

  defp limit_until(reset_at_ms, now_ms) when is_integer(reset_at_ms) do
    current = :persistent_term.get(@state_key, nil)
    blocked_until = max(reset_at_ms, if(is_integer(current), do: current, else: 0))
    :persistent_term.put(@state_key, blocked_until)
    {:error, limited_reason(blocked_until, now_ms)}
  end

  defp limited_reason(reset_at_ms, now_ms) do
    {:linear_rate_limited,
     %{
       reset_at_ms: reset_at_ms,
       retry_after_ms: max(reset_at_ms - now_ms, 0)
     }}
  end

  defp reset_from_headers(headers, now_ms) do
    max_reset_from_headers(headers) || now_ms + @default_retry_ms
  end

  defp limited_response?(response, headers) do
    body = Map.get(response, :body) || Map.get(response, "body")
    status = Map.get(response, :status) || Map.get(response, "status")

    cond do
      rate_limited_body?(body) -> true
      status == 429 -> true
      reset_at_ms = exhausted_reset_from_headers(headers) -> {:exhausted, reset_at_ms}
      true -> false
    end
  end

  defp exhausted_reset_from_headers(headers) do
    header_map = normalize_headers(headers)

    [
      {"x-ratelimit-requests-remaining", "x-ratelimit-requests-reset"},
      {"x-ratelimit-endpoint-requests-remaining", "x-ratelimit-endpoint-requests-reset"},
      {"x-ratelimit-complexity-remaining", "x-ratelimit-complexity-reset"}
    ]
    |> Enum.find_value(fn {remaining_header, reset_header} ->
      remaining = parse_integer(Map.get(header_map, remaining_header))
      reset = parse_integer(Map.get(header_map, reset_header))

      if remaining == 0 and is_integer(reset), do: reset
    end)
  end

  defp max_reset_from_headers(headers) do
    header_map = normalize_headers(headers)

    [
      "x-ratelimit-requests-reset",
      "x-ratelimit-endpoint-requests-reset",
      "x-ratelimit-complexity-reset"
    ]
    |> Enum.flat_map(fn header ->
      case parse_integer(Map.get(header_map, header)) do
        nil -> []
        reset_at_ms -> [reset_at_ms]
      end
    end)
    |> Enum.max(fn -> nil end)
  end

  defp normalize_headers(headers) when is_map(headers) do
    Enum.reduce(headers, %{}, fn {key, value}, acc ->
      Map.put(acc, normalize_header_name(key), first_header_value(value))
    end)
  end

  defp normalize_headers(headers) when is_list(headers) do
    Enum.reduce(headers, %{}, fn
      {key, value}, acc -> Map.put(acc, normalize_header_name(key), first_header_value(value))
      _other, acc -> acc
    end)
  end

  defp normalize_headers(_headers), do: %{}

  defp normalize_header_name(key) do
    key
    |> to_string()
    |> String.downcase()
  end

  defp first_header_value([value | _rest]), do: value
  defp first_header_value(value), do: value

  defp parse_integer(value) when is_integer(value), do: value

  defp parse_integer(value) when is_binary(value) do
    case Integer.parse(String.trim(value)) do
      {integer, ""} -> integer
      _ -> nil
    end
  end

  defp parse_integer(_value), do: nil

  defp rate_limited_body?(%{"errors" => errors}) when is_list(errors) do
    Enum.any?(errors, &rate_limited_error?/1)
  end

  defp rate_limited_body?(%{errors: errors}) when is_list(errors) do
    Enum.any?(errors, &rate_limited_error?/1)
  end

  defp rate_limited_body?(_body), do: false

  defp rate_limited_error?(%{"extensions" => %{"code" => "RATELIMITED"}}), do: true
  defp rate_limited_error?(%{extensions: %{code: "RATELIMITED"}}), do: true
  defp rate_limited_error?(_error), do: false
end
