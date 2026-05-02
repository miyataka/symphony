---
tracker:
  kind: github
  token: $GITHUB_TOKEN
  owner: miyataka
  owner_type: user
  project_number: 1
  status_field: Status
  priority_field: Priority
  active_states:
    - Todo
    - In Progress
    - Rework
    - Merging
  terminal_states:
    - Done
    - Closed
    - Cancelled
    - Canceled
    - Duplicate
polling:
  interval_ms: 30000
workspace:
  root: ~/code/symphony-workspaces
hooks:
  after_create: |
    git clone --depth 1 git@github.com:miyataka/symphony.git .
agent:
  max_concurrent_agents: 4
  max_turns: 20
  command: |
    codex exec --skip-git-repo-check < "$SYMPHONY_PROMPT_FILE"
---

You are working on GitHub issue {{ .Issue.Identifier }}.

Issue context:
Title: {{ .Issue.Title }}
Status: {{ .Issue.State }}
URL: {{ .Issue.URL }}

Description:
{{ .Issue.Description }}

Instructions:

1. Work only inside the current workspace.
2. Keep issue and PR status current using the GitHub tools available in the workspace.
3. Run validation before handoff.
4. Final output must report completed actions and blockers only.
