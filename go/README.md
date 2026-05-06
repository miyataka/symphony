# Symphony Go

This directory contains a Go implementation of Symphony focused on GitHub Projects v2 as the issue
tracker.

> [!WARNING]
> This is an experimental implementation for trusted environments. It currently launches a
> shell-based `agent.command` inside each issue workspace rather than implementing the full Codex
> app-server streaming protocol from the Elixir reference implementation.

## What it does

1. Loads a repository-owned `WORKFLOW.md`
2. Polls a GitHub Projects v2 board for issues in configured active states
3. Creates a deterministic workspace per issue
4. Runs workspace lifecycle hooks
5. Writes the rendered prompt to `.symphony/prompt.md`
6. Runs `agent.command` inside the workspace with issue metadata in environment variables
7. Reconciles running work against GitHub Project status and retries failed runs with backoff
8. Updates GitHub Project status and a persistent `## Codex Workpad` issue comment when possible

## Requirements

- Go 1.23+
- A GitHub token with access to the target Project v2 and its issues
- `read:project` permission for classic PATs, or equivalent fine-grained project access
- `Issues: read` permission when `tracker.read_issue_dependencies` is enabled

## Run

```bash
cd go
export GITHUB_TOKEN=...
go run ./cmd/symphony --workflow ./WORKFLOW.github.md
```

To print setup commands for a GitHub Project:

```bash
go run ./cmd/symphony setup-github-project --workflow ./WORKFLOW.github.md
```

## Logging

Symphony writes `Info` and higher logs to stdout. The GitHub tracker logs one scan summary per
Project read, including the configured owner/project, requested states, total Project items, matched
issues, and counts/examples for skipped items such as non-Issue items, missing Status values, state
mismatches, assignee mismatches, and repository mismatches.

To keep a local log file:

```bash
go run ./cmd/symphony --workflow ./WORKFLOW.github.md 2>&1 | tee symphony.log
```

Use JSON logs by adding this to the workflow front matter:

```yaml
observability:
  log_json: true
```

## Configuration

Use YAML front matter plus a Go `text/template` prompt body. The prompt receives:

- `.Issue`: normalized issue metadata
- `.Turn`: current turn number

Minimal GitHub Projects example:

```md
---
tracker:
  kind: github
  token: $GITHUB_TOKEN
  owner: miyataka
  owner_type: user
  project_number: 1
  status_field: Status
  priority_field: Priority
  allowed_repositories:
    - miyataka/api
    - miyataka/frontend
  start_state: In Progress
  handoff_state: Human Review
  rework_state: Rework
  merging_state: Merging
  done_state: Done
  workpad_marker: "## Codex Workpad"
  read_issue_dependencies: true
  active_states: [Todo, In Progress, Rework]
  monitor_states: [Human Review, Merging]
  terminal_states: [Done, Closed, Cancelled, Canceled, Duplicate]
pull_request:
  auto_merge: false
  merge_method: SQUASH
  require_approval: true
  require_passing_checks: true
  required_check_names: []
workspace:
  root: ~/code/symphony-workspaces
  cleanup_orphans: false
  cleanup_stale_after_days: 0
hooks:
  after_create: |
    git clone "$SYMPHONY_REPOSITORY_SSH_URL" .
    git fetch origin --prune
    base_branch="$(git symbolic-ref --short refs/remotes/origin/HEAD | sed 's|^origin/||')"
    git checkout -B "$SYMPHONY_BRANCH" "origin/$base_branch"
  before_run: |
    git fetch origin --prune
  after_run: |
    if [ -z "$(git status --porcelain)" ]; then
      exit 0
    fi
    git add -A
    git commit -m "$SYMPHONY_ISSUE_IDENTIFIER: agent update"
    git push -u origin "$SYMPHONY_BRANCH"
    gh pr view "$SYMPHONY_BRANCH" --repo "$SYMPHONY_REPOSITORY" >/dev/null 2>&1 || \
      gh pr create --repo "$SYMPHONY_REPOSITORY" --head "$SYMPHONY_BRANCH" \
        --title "$SYMPHONY_ISSUE_TITLE" --body "Automated work for $SYMPHONY_ISSUE_URL"
agent:
  max_concurrent_agents: 4
  max_turns: 20
  command: |
    codex exec --skip-git-repo-check < "$SYMPHONY_PROMPT_FILE"
---

You are working on GitHub issue {{ .Issue.Identifier }}.

Title: {{ .Issue.Title }}
URL: {{ .Issue.URL }}
Repository: {{ .Issue.RepositoryNameWithOwner }}

{{ .Issue.Description }}
```

## Environment variables exposed to hooks and agent.command

- `SYMPHONY_ISSUE_ID`
- `SYMPHONY_ISSUE_IDENTIFIER`
- `SYMPHONY_ISSUE_TITLE`
- `SYMPHONY_ISSUE_URL`
- `SYMPHONY_ISSUE_STATE`
- `SYMPHONY_BRANCH`
- `SYMPHONY_REPOSITORY`
- `SYMPHONY_REPOSITORY_SSH_URL`
- `SYMPHONY_REPOSITORY_HTML_URL`
- `SYMPHONY_PROMPT_FILE` for `agent.command`
- `SYMPHONY_TURN` for `agent.command`

## GitHub writeback

When the tracker supports writeback, Symphony updates the configured `status_field`:

- `Todo` items move to `tracker.start_state` before dispatch
- successful active runs move to `tracker.handoff_state`

`tracker.monitor_states` are polled for writeback policies but do not dispatch agents. This lets
Symphony watch `Human Review` for requested changes and `Merging` for completed PRs without
starting another run while review or merge is still pending.

It also creates or updates one issue comment containing `tracker.workpad_marker`, defaulting to
`## Codex Workpad`. This comment is the handoff surface for workspace path, status, and execution
notes.

## Issue dependencies

When `tracker.read_issue_dependencies` is true, Symphony reads GitHub issue dependencies through
the REST API and populates `.Issue.BlockedBy` with open blockers. Issues with open blockers are not
dispatched.

## Linked pull requests

Symphony also reads pull requests referenced by GitHub's `closedByPullRequestsReferences` field and
exposes them through `.Issue.PullRequests`. Each PR includes review decision, merge state, status
check rollup state, comment count, and unresolved review thread count so workflows can decide
whether to hand off, rework, or wait.

## Review state policy

When linked PR data is present, Symphony applies two conservative state transitions:

- `tracker.handoff_state` moves to `tracker.rework_state` if any linked PR has requested changes
  or unresolved review threads.
- `tracker.handoff_state` moves to `tracker.merging_state` if any linked PR is open, non-draft,
  approved, and has passing checks.
- `tracker.merging_state` moves to `tracker.done_state` if any linked PR is merged.
- `tracker.merging_state` moves back to `tracker.rework_state` if a linked PR gets requested
  changes, unresolved review threads, or failing checks before merge.

When `pull_request.auto_merge` is true, a ready PR in `tracker.merging_state` is merged with
`pull_request.merge_method`. `required_check_names` can restrict readiness to specific check names;
when it is empty, the GitHub check rollup state is used.

## Workspace cleanup

Terminal issues remove their workspaces on startup. `workspace.cleanup_orphans` also removes
workspace directories that no longer match visible Project items, and
`workspace.cleanup_stale_after_days` removes old workspace directories by modification time.

## Hook examples

Reusable shell snippets live in `go/examples/`:

- `github-after-create.sh` clones the issue repository and checks out the Symphony branch.
- `github-before-run.sh` refreshes refs before each agent turn.
- `github-after-run-pr.sh` commits changes, pushes the branch, and creates a PR if needed.

## Testing

```bash
make all
```

## Current limitations

- The Codex app-server JSON-RPC protocol is not implemented yet. Use `agent.command` as the bridge
  to Codex or another coding agent.
