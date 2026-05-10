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
5. Writes the rendered prompt to a temporary file for `agent.command`
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
go run ./cmd/symphony ./WORKFLOW.github.md
```

To print setup commands for a GitHub Project:

```bash
go run ./cmd/symphony setup-github-project ./WORKFLOW.github.md
```

## Logging

Symphony writes `Info` and higher logs to stdout by default. The GitHub tracker also has a debug
Project scan summary for setup troubleshooting, including the configured owner/project, requested
states, total Project items, matched issues, and counts/examples for skipped items such as non-Issue
items, missing Status values, state mismatches, assignee mismatches, and repository mismatches.

To keep a local log file:

```bash
go run ./cmd/symphony ./WORKFLOW.github.md 2>&1 | tee symphony.log
```

Use JSON logs or enable debug logging by adding this to the workflow front matter:

```yaml
observability:
  log_json: true
  log_level: debug
```

## Agent kinds

Symphony selects per-agent defaults from `agent.kind` in the workflow front matter:

| `agent.kind`         | Default `agent.command`                                                  | Default `tracker.workpad_marker` |
|----------------------|--------------------------------------------------------------------------|----------------------------------|
| `codex` (or omitted) | (none — must be set explicitly)                                          | `## Codex Workpad`               |
| `claude-code`        | `cat "$SYMPHONY_PROMPT_FILE" \| claude -p --dangerously-skip-permissions` | `## Claude Workpad`              |

Both `agent.command` and `tracker.workpad_marker` can be overridden per workflow. `agent.kind` is normalized to lower case and an unknown value is rejected at workflow load time.

`claude-code` requires the [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed and on `PATH`. The default command runs Claude Code with `--dangerously-skip-permissions` because Symphony already isolates each issue inside a per-issue workspace; if you need stricter sandboxing, set `agent.command` explicitly.

Minimal `claude-code` example:

```yaml
agent:
  kind: claude-code
  max_concurrent_agents: 4
  max_turns: 20
```

## Configuration

Use YAML front matter plus a Go `text/template` prompt body. The prompt receives:

- `.Issue`: normalized issue metadata
- `.Issue.Comments`: non-Workpad issue comments for additional human instructions and context
- `.Issue.PRReviewComments`: unresolved review thread comments from linked pull requests (bot comments older than the latest commit are skipped)
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
  merge_method: MERGE
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
    {
      echo ".symphony/"
      echo ".tmp/"
    } >> .git/info/exclude
  before_run: |
    git fetch origin --prune
  after_run: |
    git rm -f --ignore-unmatch .symphony/prompt.md >/dev/null 2>&1 || true
    changes="$(git status --porcelain -- . ':(exclude).symphony' ':(exclude).tmp')"
    prompt_cleanup="$(git diff --cached --name-only -- .symphony/prompt.md)"
    if [ -z "$changes$prompt_cleanup" ]; then
      echo "no non-Symphony workspace changes to commit" >&2
      exit 1
    fi
    git add -A -- . ':(exclude).symphony' ':(exclude).tmp'
    git commit -m "$SYMPHONY_ISSUE_IDENTIFIER: agent update"
    git push -u origin "$SYMPHONY_BRANCH"
    gh pr view "$SYMPHONY_BRANCH" --repo "$SYMPHONY_REPOSITORY" >/dev/null 2>&1 || \
      gh pr create --repo "$SYMPHONY_REPOSITORY" --head "$SYMPHONY_BRANCH" \
        --title "$SYMPHONY_ISSUE_TITLE" --body "Automated work for $SYMPHONY_ISSUE_URL"
agent:
  max_concurrent_agents: 4
  max_turns: 20
  command: |
    mkdir -p .tmp
    TMPDIR="$PWD/.tmp" TMP="$PWD/.tmp" TEMP="$PWD/.tmp" \
      codex exec --sandbox workspace-write --skip-git-repo-check < "$SYMPHONY_PROMPT_FILE"
---

You are working on GitHub issue {{ .Issue.Identifier }}.

Title: {{ .Issue.Title }}
URL: {{ .Issue.URL }}
Repository: {{ .Issue.RepositoryNameWithOwner }}

{{ .Issue.Description }}

{{ if .Issue.Comments }}Issue comments:
{{ range .Issue.Comments }}
- {{ .Author }} {{ .URL }}
{{ .Body }}
{{ end }}
{{ end }}

{{ if .Issue.PRReviewComments }}Unresolved PR review comments:
{{ range .Issue.PRReviewComments }}
- {{ .Author }}{{ if .AuthorIsBot }} (bot){{ end }} on PR #{{ .PRNumber }} {{ .Path }}{{ if .Line }}:{{ .Line }}{{ end }} {{ .URL }}
{{ .Body }}
{{ end }}
{{ end }}

Instructions:

1. Work only inside the current workspace.
2. Do not edit, stage, or commit `.symphony/` or `.tmp/`; they are Symphony runtime files.
3. Do not run `git commit`, `git push`, `gh pr create`, or `gh pr edit`; Symphony hooks publish the branch and PR after the turn.
4. Run validation before handoff.
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
- `SYMPHONY_PROMPT_FILE` for `agent.command`; when `agent.command` is configured this prompt file is outside the workspace and removed after the command exits
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
