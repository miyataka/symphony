defmodule SymphonyElixir.LinearRateLimitTest do
  use SymphonyElixir.TestSupport

  alias SymphonyElixir.Linear.RateLimit

  test "rate limit guard accepts atom-key GraphQL errors and integer reset headers" do
    now_ms = 1_000
    reset_at_ms = 61_000

    assert {:error, {:linear_rate_limited, %{reset_at_ms: ^reset_at_ms, retry_after_ms: 60_000}}} =
             RateLimit.record_response(
               %{
                 status: 400,
                 headers: [
                   {"x-ratelimit-requests-reset", reset_at_ms},
                   :ignored_header
                 ],
                 body: %{errors: [%{extensions: %{code: "RATELIMITED"}}]}
               },
               now_ms
             )
  end

  test "rate limit guard ignores malformed headers on non-limited responses" do
    assert :ok =
             RateLimit.record_response(
               %{
                 status: 200,
                 headers: [
                   {"x-ratelimit-requests-remaining", "not-an-integer"},
                   :ignored_header
                 ],
                 body: %{"data" => %{}}
               },
               1_000
             )

    assert :ok =
             RateLimit.record_response(
               %{
                 status: 200,
                 headers: "not-header-data",
                 body: %{"data" => %{}}
               },
               1_000
             )
  end

  test "rate limit guard accepts map headers with list values" do
    now_ms = 1_000
    reset_at_ms = 61_000

    assert {:error, {:linear_rate_limited, %{reset_at_ms: ^reset_at_ms, retry_after_ms: 60_000}}} =
             RateLimit.record_response(
               %{
                 status: 200,
                 headers: %{
                   "x-ratelimit-requests-remaining" => ["0"],
                   "x-ratelimit-requests-reset" => [Integer.to_string(reset_at_ms)]
                 },
                 body: %{"data" => %{}}
               },
               now_ms
             )
  end
end
