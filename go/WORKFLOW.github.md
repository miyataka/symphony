---
tracker:
  kind: github
  token: $GITHUB_TOKEN
  owner: miyataka
  owner_type: user
  project_number: 2
  status_field: Status
  priority_field: Priority
  allowed_repositories:
    - miyataka/symphony
    - robustengine/vmate-api
    - robustengine/vmate-frontend
    - robustengine/vmate-api-schema
    - robustengine/vmate-infra
    - robustengine/affiliate-service-provider
  start_state: In Progress
  handoff_state: Human Review
  rework_state: Rework
  merging_state: Merging
  done_state: Done
  workpad_marker: "## Codex Workpad"
  read_issue_dependencies: true
  active_states:
    - Todo
    - In Progress
    - Rework
  monitor_states:
    - Human Review
    - Merging
  terminal_states:
    - Done
    - Closed
    - Cancelled
    - Canceled
    - Duplicate
polling:
  interval_ms: 30000
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
    set -eu
    git clone "$SYMPHONY_REPOSITORY_SSH_URL" .
    git fetch origin --prune
    base_branch="$(git symbolic-ref --short refs/remotes/origin/HEAD | sed 's|^origin/||')"
    git checkout -B "$SYMPHONY_BRANCH" "origin/$base_branch"
    {
      echo ".symphony/"
      echo ".tmp/"
    } >> .git/info/exclude
  before_run: |
    set -eu
    git fetch origin --prune
  after_run: |
    set -eu
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

Issue context:
Title: {{ .Issue.Title }}
Status: {{ .Issue.State }}
URL: {{ .Issue.URL }}
Repository: {{ .Issue.RepositoryNameWithOwner }}

Description:
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
4. Read GitHub context when available, but leave issue status and PR creation to Symphony unless explicitly asked by the prompt.
5. Run validation before handoff.
6. Final output must report completed actions and blockers only.
