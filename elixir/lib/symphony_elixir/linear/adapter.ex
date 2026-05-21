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
  mutation SymphonyUpdateComment($commentId: String!, $body: String!) {
    commentUpdate(id: $commentId, input: {body: $body}) {
      success
    }
  }
  """

  @workpad_heading "## Codex Workpad"

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
         {:ok, response} <- client_module().graphql(@workpad_comments_query, %{issueId: issue_id}),
         {:ok, workpad} <- find_or_build_workpad(response),
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

  defp find_or_build_workpad(response) do
    case get_in(response, ["data", "issue", "comments", "nodes"]) do
      comments when is_list(comments) ->
        workpad =
          Enum.find(comments, fn comment ->
            case Map.get(comment, "body") do
              body when is_binary(body) -> String.contains?(body, @workpad_heading)
              _ -> false
            end
          end)

        {:ok,
         %{
           comment_id: workpad && workpad["id"],
           body: if(workpad, do: workpad["body"], else: @workpad_heading)
         }}

      _ ->
        {:error, :workpad_comments_failed}
    end
  end

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
         true <- get_in(response, ["data", "commentUpdate", "success"]) == true do
      :ok
    else
      false -> {:error, :comment_update_failed}
      {:error, reason} -> {:error, reason}
      _ -> {:error, :comment_update_failed}
    end
  end

  defp validate_section_key(section_key) do
    if String.contains?(section_key, ["-->", "\n", "\r"]) do
      {:error, :invalid_section_key}
    else
      :ok
    end
  end

  defp marker(section_key, boundary) do
    "<!-- symphony:#{section_key}:#{boundary} -->"
  end
end
