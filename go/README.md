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
9. Monitors long-running issue loops and creates backlog sub-issues when work needs to be split

## Requirements

- Go 1.23+
- A GitHub token with access to the target Project v2 and its issues
- `read:project` permission for classic PATs, or equivalent fine-grained project access
- `Issues: read` permission when `tracker.read_issue_dependencies` is enabled
- `Issues: write` permission when `loop_monitor.enabled` is true and sub-issue creation is needed

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

Runtime events that are emitted include dispatch decisions, retry/backoff scheduling, GitHub
Project status transitions, workspace cleanup, pull request merge attempts, and per-issue run
completion. Each event carries the issue identifier, state, and (where relevant) PR number, retry
count, and error so a tail/grep over the log file is enough to follow what Symphony is doing.

To persist logs to a file that another shell can `tail -f` or `rg` while Symphony runs, set
`observability.log_file` in the workflow front matter:

```yaml
observability:
  log_json: true
  log_level: info
  log_file: ~/symphony-logs/runtime.log
  dashboard_enabled: true
  refresh_ms: 1000
  render_interval_ms: 16
  run_health:
    enabled: true
    quiet_after_ms: 300000
    suspect_after_ms: 600000
    self_report_timeout_ms: 120000
```

When `log_file` is set, Symphony appends structured events to the file *and* mirrors them to
stdout. When the terminal dashboard is enabled, the dashboard owns stdout and mirrored logs are
written to stderr instead. The path supports `~/` and `$ENV` expansion, and any missing parent
directories are created at startup. The file is opened in append mode so restarts preserve prior
history.

The terminal dashboard is enabled by default and renders a compact lipgloss status frame with the
current running agents. Set `observability.dashboard_enabled: false` to keep stdout log-only.
Run health is also enabled by default. Because the Go implementation currently launches a
shell-based `agent.command`, health is based on time since dispatch or the last completed turn:
`active` before `quiet_after_ms`, `quiet` before `suspect_after_ms`, `suspect` until
`self_report_timeout_ms` elapses, and then `stalled`.

Without `log_file`, you can still capture stdout from another session via `tee`:

```bash
go run ./cmd/symphony ./WORKFLOW.github.md 2>&1 | tee symphony.log
```

Then in another shell:

```bash
tail -f ~/symphony-logs/runtime.log
rg 'failed|retry|backoff|panic' ~/symphony-logs/runtime.log
```

## Agent kinds

Symphony selects per-agent defaults from `agent.kind` in the workflow front matter:

| `agent.kind`         | Default `agent.command`                                                  | Default `tracker.workpad_marker` |
|----------------------|--------------------------------------------------------------------------|----------------------------------|
| `codex` (or omitted) | `mkdir -p .tmp && TMPDIR="$PWD/.tmp" TMP="$PWD/.tmp" TEMP="$PWD/.tmp" codex exec --sandbox workspace-write --skip-git-repo-check < "$SYMPHONY_PROMPT_FILE"` | `## Codex Workpad`               |
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

### Claude Code fallback to Codex

When `agent.kind: claude-code` is used, Symphony enables a one-way fallback to Codex by
default for the current active run. If the Claude Code command exits nonzero with the reserved
wrapper exit code `88`, Symphony classifies it as `claude_limit`. A narrow stderr/stdout
classifier remains as a compatibility path for known usage/rate/quota wording and structured
`rate_limit_error` output. Ordinary task failures still use the normal failed-run retry/backoff
path.

On fallback, Symphony records an explicit attempt boundary instead of treating the same attempt as
if it silently changed agents. The failed Claude Code attempt and the running/completed Codex
fallback attempt are written to the workspace fallback state, Symphony writes a Workpad note such as
`claude-code limit reached; retrying with codex`, and the same turn is retried with the fallback
command. The Workpad note also calls out that workspace and issue context are preserved while
Claude-only hooks, slash-skill assumptions, and per-agent restrictions are advisory in Codex.

The fallback is one-way only for the current issue state. Symphony writes
`.symphony/agent-fallback.json` inside the issue workspace before starting Codex; if the
orchestrator restarts while the issue is still in that state and the fallback is not completed, the
next run resumes with Codex instead of starting Claude Code again. If the persisted fallback is
already completed, Symphony skips duplicate agent execution and lets the normal successful handoff
path run. If the issue later changes state, for example from `In Progress` to `Rework`, the stale
fallback record is ignored and the configured primary `agent.kind` can run again.

The default fallback is equivalent to:

```yaml
agent:
  kind: claude-code
  fallback:
    enabled: true
    kind: codex
    on: [claude_limit]
    command: |
      mkdir -p .tmp
      TMPDIR="$PWD/.tmp" TMP="$PWD/.tmp" TEMP="$PWD/.tmp" \
        codex exec --sandbox workspace-write --skip-git-repo-check < "$SYMPHONY_PROMPT_FILE"
```

To disable automatic fallback:

```yaml
agent:
  kind: claude-code
  fallback:
    enabled: false
```

To customize only the fallback command:

```yaml
agent:
  kind: claude-code
  fallback:
    kind: codex
    on: [claude_limit]
    command: |
      mkdir -p .tmp
      TMPDIR="$PWD/.tmp" TMP="$PWD/.tmp" TEMP="$PWD/.tmp" \
        codex exec --sandbox workspace-write --skip-git-repo-check --model gpt-5.4 < "$SYMPHONY_PROMPT_FILE"
```

The classifier intentionally does not match generic command failures. Prefer a thin Claude Code
wrapper that owns message parsing and exits `88` for `claude_limit`; when Claude Code later exposes
native structured exit codes, map them in that wrapper. The compatibility text classifier recognizes
`rate_limit_error`-style output, and otherwise requires known usage/rate/quota or HTTP 429 wording to
also mention a Claude/Anthropic context.

## Configuration

Use YAML front matter plus a Go `text/template` prompt body. The prompt receives:

- `.Issue`: normalized issue metadata
- `.Issue.Comments`: non-Workpad issue comments from repository owners or organization members for additional human instructions and context
- `.Issue.PRReviewComments`: unresolved review thread comments from repository owners or organization members on linked pull requests (bot comments older than the latest commit are skipped)
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
  backlog_states: [Backlog]
  active_states: [Todo, In Progress, Rework]
  monitor_states: [Human Review, Merging]
  terminal_states: [Done, Closed, Cancelled, Canceled, Duplicate]
pull_request:
  auto_merge: false
  merge_method: MERGE
  require_approval: true
  require_passing_checks: true
  required_check_names: []
loop_monitor:
  enabled: true
  interval_ms: 3600000
  max_runtime_ms: 21600000
  min_turns: 3
  sub_issue_state: Backlog
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

{{ if .Issue.Comments }}Issue comments from repository owners or organization members:
{{ range .Issue.Comments }}
- {{ .Author }} {{ .URL }}
{{ .Body }}
{{ end }}
{{ end }}

{{ if .Issue.PRReviewComments }}Unresolved PR review comments from repository owners or organization members:
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
- `SYMPHONY_AGENT_KIND` (`codex` or `claude-code`; during fallback this is the active fallback kind)
- `SYMPHONY_PROMPT_FILE` for `agent.command`; when `agent.command` is configured this prompt file is outside the workspace and removed after the command exits
- `SYMPHONY_TURN` for `agent.command`

## GitHub writeback

When the tracker supports writeback, Symphony updates the configured `status_field`:

- `Todo` items move to `tracker.start_state` before dispatch
- successful active runs move to `tracker.handoff_state`

`tracker.monitor_states` are polled for writeback policies but do not dispatch agents. This lets
Symphony watch `Human Review` for requested changes and `Merging` for completed PRs without
starting another run while review or merge is still pending.

`tracker.backlog_states` are Project Status options that Symphony recognises as captured but
not-yet-dispatchable work. Items in a backlog state are excluded from agent dispatch so they will
not run until a human moves them into an active state. Use them as a holding area before work is
ready for Symphony:

- `Backlog`: accepted but not dispatchable yet — Symphony will not pick the issue up.
- `Todo`: ready for Symphony dispatch — the orchestrator may move it into `tracker.start_state`
  and run an agent once concurrency and repository filters allow.

Backlog states only influence the `setup-github-project` output (so the Project Status field is
created with a `Backlog` option). They are not used for dispatch, writeback, or workspace
lifecycle. To make backlog items eligible for agent runs, move them to `Todo` (or another
`active_states` entry) instead of adding `Backlog` to `tracker.active_states`.

It also creates or updates one issue comment containing `tracker.workpad_marker`, defaulting to
`## Codex Workpad`. This comment is the handoff surface for workspace path, status, and execution
notes.

## Loop monitor

When `loop_monitor.enabled` is true, Symphony checks running issues every
`loop_monitor.interval_ms` milliseconds. If a run has exceeded `loop_monitor.max_runtime_ms` and
has completed at least `loop_monitor.min_turns`, Symphony treats it as a suspected loop, creates a
new issue in the same repository, adds it to the configured GitHub Project, moves it to
`loop_monitor.sub_issue_state`, and records the created sub-issue in the parent Workpad. Each
running issue can trigger this once per Symphony process run.

The default interval is one hour, the default runtime threshold is six hours, the default minimum
turn count is three, and new breakdown issues default to `Backlog`.

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

## Restart and shutdown safety

Each running issue writes a durable lease to `.symphony/run_lease.json` in its workspace. The lease
contains the issue id, state, run id, process id, status, start time, and heartbeat time. While the
orchestrator process is alive it refreshes the heartbeat every 30 seconds.

On startup and each poll, active Project states such as `In Progress` and `Rework` are still
dispatchable, but they are not dispatched blindly. If the issue workspace has a non-stale `running`
lease from a previous process, Symphony skips the issue so a restarted orchestrator does not start a
second agent in the same workspace. A running lease becomes stale after five minutes without a
heartbeat; Symphony then marks the stale lease and may retry the issue from its current Project
state.

On graceful shutdown, Symphony cancels active runs, waits for their goroutines to clean up, records
the lease as `interrupted`, and updates the Workpad to say the issue remains in its current state for
a future retry. If cleanup does not finish before the shutdown grace period, the Workpad records that
cleanup was interrupted; a fresh process will continue to honor the still-running lease until it
goes stale.

`agent.command` runs in its own process group. Cancellation and turn timeouts kill that process
group, not just the immediate `bash -lc` wrapper, so shell-spawned child processes do not continue
working on the branch after the orchestrator has stopped the run.

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
- `WORKFLOW.md` is read once at startup; SPEC §6.2 dynamic reload is not implemented yet. See
  [`docs/hot_reload.md`](docs/hot_reload.md) for the proposed conformance path.
