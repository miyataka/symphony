defmodule SymphonyElixir.Linear.Adapter do
  @moduledoc """
  Linear-backed tracker adapter.
  """

  @behaviour SymphonyElixir.Tracker

  alias SymphonyElixir.Linear.Client

  @create_comment_mutation """
  mutation SymphonyCreateComment($issueId: String!, $body: String!) {
    commentCreate(input: {issueId: $issueId, body: $body}) {
      success
    }
  }
  """

  @workpad_comments_query """
  query SymphonyWorkpadComments($issueId: String!, $after: String) {
    issue(id: $issueId) {
      comments(first: 50, after: $after) {
        nodes {
          id
          body
        }
        pageInfo {
          hasNextPage
          endCursor
        }
      }
    }
  }
  """

  @update_comment_mutation """
  mutation SymphonyUpdateComment($commentId: String!, $body: String!) {
    commentUpdate(id: $commentId, input: {body: $body}) {
      success
    }
  }
  """

  @workpad_heading "## Codex Workpad"
  @workpad_heading_pattern ~r/^## Codex Workpad\s*$/m
  @section_key_pattern ~r/^[a-z0-9][a-z0-9._-]*$/

  @update_state_mutation """
  mutation SymphonyUpdateIssueState($issueId: String!, $stateId: String!) {
    issueUpdate(id: $issueId, input: {stateId: $stateId}) {
      success
    }
  }
  """

  @state_lookup_query """
  query SymphonyResolveStateId($issueId: String!, $stateName: String!) {
    issue(id: $issueId) {
      team {
        states(filter: {name: {eq: $stateName}}, first: 1) {
          nodes {
            id
          }
        }
      }
    }
  }
  """

  @spec fetch_candidate_issues() :: {:ok, [term()]} | {:error, term()}
  def fetch_candidate_issues, do: client_module().fetch_candidate_issues()

  @spec fetch_issues_by_states([String.t()]) :: {:ok, [term()]} | {:error, term()}
  def fetch_issues_by_states(states), do: client_module().fetch_issues_by_states(states)

  @spec fetch_issue_states_by_ids([String.t()]) :: {:ok, [term()]} | {:error, term()}
  def fetch_issue_states_by_ids(issue_ids), do: client_module().fetch_issue_states_by_ids(issue_ids)

  @spec create_comment(String.t(), String.t()) :: :ok | {:error, term()}
  def create_comment(issue_id, body) when is_binary(issue_id) and is_binary(body) do
    with {:ok, response} <- client_module().graphql(@create_comment_mutation, %{issueId: issue_id, body: body}),
         :ok <- reject_graphql_errors(response),
         true <- get_in(response, ["data", "commentCreate", "success"]) == true do
      :ok
    else
      false -> {:error, :comment_create_failed}
      {:error, reason} -> {:error, reason}
      _ -> {:error, :comment_create_failed}
    end
  end

  @spec upsert_workpad_section(String.t(), String.t(), String.t()) :: :ok | {:error, term()}
  def upsert_workpad_section(issue_id, section_key, markdown)
      when is_binary(issue_id) and is_binary(section_key) and is_binary(markdown) do
    with :ok <- validate_section_key(section_key),
         :ok <- validate_section_markdown(section_key, markdown),
         {:ok, workpad} <- find_or_build_workpad(issue_id),
         body <- upsert_section(workpad.body, section_key, markdown) do
      write_workpad_comment(issue_id, workpad.comment_id, body)
    end
  end

  @spec update_issue_state(String.t(), String.t()) :: :ok | {:error, term()}
  def update_issue_state(issue_id, state_name)
      when is_binary(issue_id) and is_binary(state_name) do
    with {:ok, state_id} <- resolve_state_id(issue_id, state_name),
         {:ok, response} <-
           client_module().graphql(@update_state_mutation, %{issueId: issue_id, stateId: state_id}),
         true <- get_in(response, ["data", "issueUpdate", "success"]) == true do
      :ok
    else
      false -> {:error, :issue_update_failed}
      {:error, reason} -> {:error, reason}
      _ -> {:error, :issue_update_failed}
    end
  end

  defp client_module do
    Application.get_env(:symphony_elixir, :linear_client_module, Client)
  end

  defp resolve_state_id(issue_id, state_name) do
    with {:ok, response} <-
           client_module().graphql(@state_lookup_query, %{issueId: issue_id, stateName: state_name}),
         state_id when is_binary(state_id) <-
           get_in(response, ["data", "issue", "team", "states", "nodes", Access.at(0), "id"]) do
      {:ok, state_id}
    else
      {:error, reason} -> {:error, reason}
      _ -> {:error, :state_not_found}
    end
  end

  defp find_or_build_workpad(issue_id) do
    with {:ok, workpad} <- fetch_workpad_comment(issue_id, nil, []) do
      {:ok,
       %{
         comment_id: workpad && workpad["id"],
         body: if(workpad, do: workpad["body"], else: @workpad_heading)
       }}
    end
  end

  defp fetch_workpad_comment(issue_id, after_cursor, candidates) do
    with {:ok, response} <-
           client_module().graphql(@workpad_comments_query, %{issueId: issue_id, after: after_cursor}),
         :ok <- reject_graphql_errors(response),
         {:ok, comments, page_info} <- decode_comments_page(response) do
      candidates = candidates ++ workpad_candidates(comments)

      workpad = preferred_workpad(candidates)

      cond do
        candidate_has_symphony_marker?(workpad) ->
          {:ok, workpad}

        page_info["hasNextPage"] == true and is_binary(page_info["endCursor"]) ->
          fetch_workpad_comment(issue_id, page_info["endCursor"], candidates)

        page_info["hasNextPage"] == true ->
          {:error, :workpad_comments_failed}

        true ->
          {:ok, workpad}
      end
    end
  end

  defp decode_comments_page(response) do
    comments = get_in(response, ["data", "issue", "comments", "nodes"])
    page_info = get_in(response, ["data", "issue", "comments", "pageInfo"])

    if is_list(comments) and is_map(page_info) do
      {:ok, comments, page_info}
    else
      {:error, :workpad_comments_failed}
    end
  end

  defp workpad_candidates(comments) do
    Enum.filter(comments, fn comment ->
      case Map.get(comment, "body") do
        body when is_binary(body) ->
          Regex.match?(@workpad_heading_pattern, body) or String.contains?(body, "<!-- symphony:")

        _ ->
          false
      end
    end)
  end

  defp preferred_workpad([]), do: nil

  defp preferred_workpad(candidates) do
    Enum.find(candidates, &candidate_has_symphony_marker?/1) || List.first(candidates)
  end

  defp candidate_has_symphony_marker?(%{"body" => body}) when is_binary(body) do
    String.contains?(body, "<!-- symphony:")
  end

  defp candidate_has_symphony_marker?(_comment), do: false

  defp upsert_section(body, section_key, markdown) do
    start_marker = marker(section_key, "start")
    end_marker = marker(section_key, "end")
    section = [start_marker, markdown, end_marker] |> Enum.join("\n")
    pattern = Regex.compile!(Regex.escape(start_marker) <> "[\\s\\S]*?" <> Regex.escape(end_marker))

    if Regex.match?(pattern, body) do
      String.replace(body, pattern, section)
    else
      body
      |> String.trim_trailing()
      |> Kernel.<>("\n\n" <> section)
    end
  end

  defp write_workpad_comment(issue_id, nil, body), do: create_comment(issue_id, body)

  defp write_workpad_comment(_issue_id, comment_id, body) do
    with {:ok, response} <-
           client_module().graphql(@update_comment_mutation, %{commentId: comment_id, body: body}),
         :ok <- reject_graphql_errors(response),
         true <- get_in(response, ["data", "commentUpdate", "success"]) == true do
      :ok
    else
      false -> {:error, :comment_update_failed}
      {:error, reason} -> {:error, reason}
      _ -> {:error, :comment_update_failed}
    end
  end

  defp validate_section_key(section_key) do
    if Regex.match?(@section_key_pattern, section_key) do
      :ok
    else
      {:error, :invalid_section_key}
    end
  end

  defp validate_section_markdown(section_key, markdown) do
    start_marker = marker(section_key, "start")
    end_marker = marker(section_key, "end")

    if String.contains?(markdown, [start_marker, end_marker]) do
      {:error, :invalid_workpad_section_markdown}
    else
      :ok
    end
  end

  defp reject_graphql_errors(%{"errors" => errors}) when is_list(errors) and errors != [] do
    {:error, {:linear_graphql_errors, errors}}
  end

  defp reject_graphql_errors(_response), do: :ok

  defp marker(section_key, boundary) do
    "<!-- symphony:#{section_key}:#{boundary} -->"
  end
end
