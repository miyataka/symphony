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

## Requirements

- Go 1.23+
- A GitHub token with access to the target Project v2 and its issues
- `read:project` permission for classic PATs, or equivalent fine-grained project access

## Run

```bash
cd go
export GITHUB_TOKEN=...
go run ./cmd/symphony --workflow ./WORKFLOW.github.md
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
  active_states: [Todo, In Progress, Rework, Merging]
  terminal_states: [Done, Closed, Cancelled, Canceled, Duplicate]
workspace:
  root: ~/code/symphony-workspaces
hooks:
  after_create: |
    git clone --depth 1 "$SYMPHONY_REPOSITORY_SSH_URL" .
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
- `SYMPHONY_REPOSITORY`
- `SYMPHONY_REPOSITORY_SSH_URL`
- `SYMPHONY_REPOSITORY_HTML_URL`
- `SYMPHONY_PROMPT_FILE` for `agent.command`
- `SYMPHONY_TURN` for `agent.command`

## Testing

```bash
make all
```

## Current limitations

- GitHub issue dependencies are not yet normalized into `Issue.BlockedBy`.
- The GitHub adapter polls Project v2 items; it does not write project fields, issue comments, or
  PR links. Those actions should be performed by `agent.command` or repo-local tooling.
- The Codex app-server JSON-RPC protocol is not implemented yet. Use `agent.command` as the bridge
  to Codex or another coding agent.
