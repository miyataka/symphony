package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseWorkflowWithFrontMatter(t *testing.T) {
	def, err := Parse([]byte(`---
tracker:
  owner: miyataka
  project_number: 7
---
Hello {{ .Issue.Identifier }}
`))
	if err != nil {
		t.Fatal(err)
	}
	if def.Config["tracker"] == nil {
		t.Fatalf("expected tracker config")
	}
	if def.PromptTemplate != "Hello {{ .Issue.Identifier }}" {
		t.Fatalf("unexpected prompt: %q", def.PromptTemplate)
	}
}

func TestParseWorkflowPromptOnly(t *testing.T) {
	def, err := Parse([]byte("Prompt only\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Config) != 0 {
		t.Fatalf("expected empty config: %#v", def.Config)
	}
	if def.PromptTemplate != "Prompt only" {
		t.Fatalf("unexpected prompt: %q", def.PromptTemplate)
	}
}

func TestGitHubWorkflowKeepsRuntimeFilesOutOfAgentCommits(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "WORKFLOW.github.md"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)
	if !strings.Contains(workflow, "git rm -f --ignore-unmatch .symphony/prompt.md") {
		t.Fatal("expected GitHub workflow to clean tracked runtime prompts")
	}
	if !strings.Contains(workflow, "Do not edit, stage, or commit `.symphony/` or `.tmp/`") {
		t.Fatal("expected GitHub workflow to instruct agents away from runtime files")
	}
	if !strings.Contains(workflow, "Do not run `git commit`, `git push`, `gh pr create`, or `gh pr edit`") {
		t.Fatal("expected GitHub workflow to reserve publishing for hooks")
	}
	if !strings.Contains(workflow, "{{ range .Issue.Comments }}") {
		t.Fatal("expected GitHub workflow to include issue comments in agent prompt")
	}
	if !strings.Contains(workflow, "Issue comments from repository owners or organization members") {
		t.Fatal("expected GitHub workflow to document trusted issue comment sources")
	}
	if !strings.Contains(workflow, "{{ range .Issue.PullRequests }}") {
		t.Fatal("expected GitHub workflow to include linked pull requests in agent prompt")
	}
	if !strings.Contains(workflow, "{{ range .Checks }}") {
		t.Fatal("expected GitHub workflow to include pull request check statuses in agent prompt")
	}
	if !strings.Contains(workflow, `issue_number="${SYMPHONY_ISSUE_IDENTIFIER##*#}"`) ||
		!strings.Contains(workflow, `Closes #$issue_number`) {
		t.Fatal("expected GitHub workflow to create PRs with a closing issue reference")
	}
	if !strings.Contains(workflow, "{{ range .Issue.PRReviewComments }}") {
		t.Fatal("expected GitHub workflow to include unresolved PR review comments in agent prompt")
	}
	if !strings.Contains(workflow, "Unresolved PR review comments from repository owners or organization members") {
		t.Fatal("expected GitHub workflow to document trusted PR review comment sources")
	}
}

func TestParseWorkflowAcceptsUnterminatedFrontMatter(t *testing.T) {
	def, err := Parse([]byte(`---
tracker:
  kind: github
`))
	if err != nil {
		t.Fatal(err)
	}
	tracker, ok := def.Config["tracker"].(map[string]any)
	if !ok {
		t.Fatalf("expected tracker config map: %#v", def.Config["tracker"])
	}
	if tracker["kind"] != "github" {
		t.Fatalf("unexpected tracker kind: %#v", tracker["kind"])
	}
	if def.PromptTemplate != "" {
		t.Fatalf("unexpected prompt: %q", def.PromptTemplate)
	}
}

func TestParseWorkflowRejectsNonMapFrontMatter(t *testing.T) {
	_, err := Parse([]byte(`---
- not-a-map
---
Prompt body
`))
	if !errors.Is(err, ErrFrontMatterNotMap) {
		t.Fatalf("expected ErrFrontMatterNotMap, got %v", err)
	}
}

func TestParseConfigResolvesEnvAndDefaults(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	root := filepath.Join("~", "symphony-test")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":                "miyataka",
			"project_number":       1,
			"allowed_repositories": []string{" Miyataka/API ", "miyataka/api", "miyataka/frontend"},
		},
		"workspace": map[string]any{
			"root": root,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracker.Token != "test-token" {
		t.Fatalf("token not resolved: %q", cfg.Tracker.Token)
	}
	if cfg.Tracker.StatusField != "Status" {
		t.Fatalf("unexpected status field: %q", cfg.Tracker.StatusField)
	}
	if cfg.Tracker.RestEndpoint != "https://api.github.com" {
		t.Fatalf("unexpected rest endpoint: %q", cfg.Tracker.RestEndpoint)
	}
	if !cfg.Tracker.ReadIssueDependencies {
		t.Fatal("expected issue dependency reads to default on")
	}
	if len(cfg.Tracker.ActiveStates) != 3 || cfg.Tracker.ActiveStates[2] != "Rework" {
		t.Fatalf("unexpected active states: %#v", cfg.Tracker.ActiveStates)
	}
	if len(cfg.Tracker.BacklogStates) != 1 || cfg.Tracker.BacklogStates[0] != "Backlog" {
		t.Fatalf("unexpected backlog states: %#v", cfg.Tracker.BacklogStates)
	}
	for _, state := range cfg.Tracker.BacklogStates {
		for _, active := range cfg.Tracker.ActiveStates {
			if state == active {
				t.Fatalf("backlog state %q must not be dispatchable via active_states", state)
			}
		}
	}
	if len(cfg.Tracker.MonitorStates) != 2 ||
		cfg.Tracker.MonitorStates[0] != "Human Review" ||
		cfg.Tracker.MonitorStates[1] != "Merging" {
		t.Fatalf("unexpected monitor states: %#v", cfg.Tracker.MonitorStates)
	}
	if cfg.PullRequest.MergeMethod != "MERGE" || !cfg.PullRequest.RequireApproval || !cfg.PullRequest.RequirePassingChecks {
		t.Fatalf("unexpected pull request defaults: %#v", cfg.PullRequest)
	}
	if cfg.Observability.LogLevel != "info" {
		t.Fatalf("unexpected observability log level: %q", cfg.Observability.LogLevel)
	}
	if len(cfg.Tracker.AllowedRepositories) != 2 ||
		cfg.Tracker.AllowedRepositories[0] != "miyataka/api" ||
		cfg.Tracker.AllowedRepositories[1] != "miyataka/frontend" {
		t.Fatalf("allowed repositories not normalized: %#v", cfg.Tracker.AllowedRepositories)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace.Root != filepath.Join(home, "symphony-test") {
		t.Fatalf("workspace root not expanded: %q", cfg.Workspace.Root)
	}
}

func TestParseConfigResolvesObservabilityOptions(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"observability": map[string]any{
			"log_json":           true,
			"log_level":          "DEBUG",
			"dashboard_enabled":  false,
			"refresh_ms":         2500,
			"render_interval_ms": 33,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Observability.LogJSON {
		t.Fatal("expected JSON logging to be enabled")
	}
	if cfg.Observability.LogLevel != "debug" {
		t.Fatalf("unexpected log level: %q", cfg.Observability.LogLevel)
	}
	if cfg.Observability.DashboardEnabled {
		t.Fatal("expected dashboard_enabled override to be false")
	}
	if cfg.Observability.RefreshMS != 2500 || cfg.Observability.RenderIntervalMS != 33 {
		t.Fatalf("unexpected dashboard timing config: %#v", cfg.Observability)
	}
}

func TestParseConfigResolvesObservabilityLogFile(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("SYMPHONY_LOG_DIR", "/tmp/symphony-logs")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"observability": map[string]any{
			"log_file": "~/symphony-logs/runtime.log",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(home, "symphony-logs", "runtime.log")
	if cfg.Observability.LogFile != expected {
		t.Fatalf("log_file not expanded: got %q, want %q", cfg.Observability.LogFile, expected)
	}
}

func TestParseConfigDefaultsObservabilityLogFileToEmpty(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Observability.LogFile != "" {
		t.Fatalf("expected empty log_file by default, got %q", cfg.Observability.LogFile)
	}
	if !cfg.Observability.DashboardEnabled {
		t.Fatal("expected dashboard to default enabled")
	}
	if cfg.Observability.RefreshMS != 1000 {
		t.Fatalf("unexpected dashboard refresh default: %d", cfg.Observability.RefreshMS)
	}
	if cfg.Observability.RenderIntervalMS != 16 {
		t.Fatalf("unexpected dashboard render interval default: %d", cfg.Observability.RenderIntervalMS)
	}
}

func TestParseConfigRejectsInvalidDashboardTiming(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	_, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"observability": map[string]any{
			"refresh_ms": 0,
		},
	})
	if err == nil {
		t.Fatal("expected invalid refresh_ms error")
	}
}

func TestParseConfigResolvesLoopMonitorDefaults(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LoopMonitor.Enabled {
		t.Fatal("expected loop monitor to default enabled")
	}
	if cfg.LoopMonitor.IntervalMS != int((time.Hour)/time.Millisecond) {
		t.Fatalf("unexpected loop monitor interval: %d", cfg.LoopMonitor.IntervalMS)
	}
	if cfg.LoopMonitor.MaxRuntimeMS != int((6*time.Hour)/time.Millisecond) {
		t.Fatalf("unexpected loop monitor max runtime: %d", cfg.LoopMonitor.MaxRuntimeMS)
	}
	if cfg.LoopMonitor.MinTurns != 3 {
		t.Fatalf("unexpected loop monitor min turns: %d", cfg.LoopMonitor.MinTurns)
	}
	if cfg.LoopMonitor.SubIssueState != "Backlog" {
		t.Fatalf("unexpected loop monitor sub issue state: %q", cfg.LoopMonitor.SubIssueState)
	}
}

func TestParseConfigResolvesLoopMonitorOverrides(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"loop_monitor": map[string]any{
			"enabled":         false,
			"interval_ms":     5000,
			"max_runtime_ms":  60000,
			"min_turns":       2,
			"sub_issue_state": "Todo",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LoopMonitor.Enabled {
		t.Fatal("expected loop monitor to honor enabled=false")
	}
	if cfg.LoopMonitor.IntervalMS != 5000 {
		t.Fatalf("unexpected loop monitor interval: %d", cfg.LoopMonitor.IntervalMS)
	}
	if cfg.LoopMonitor.MaxRuntimeMS != 60000 {
		t.Fatalf("unexpected loop monitor max runtime: %d", cfg.LoopMonitor.MaxRuntimeMS)
	}
	if cfg.LoopMonitor.MinTurns != 2 {
		t.Fatalf("unexpected loop monitor min turns: %d", cfg.LoopMonitor.MinTurns)
	}
	if cfg.LoopMonitor.SubIssueState != "Todo" {
		t.Fatalf("unexpected loop monitor sub issue state: %q", cfg.LoopMonitor.SubIssueState)
	}
}

func TestParseConfigRejectsInvalidObservabilityLogLevel(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	_, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"observability": map[string]any{
			"log_level": "trace",
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseConfigResolvesPullRequestAndWorkspaceOptions(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"pull_request": map[string]any{
			"auto_merge":           true,
			"merge_method":         "rebase",
			"required_check_names": []string{" go ", "go", "make-all"},
		},
		"workspace": map[string]any{
			"cleanup_orphans":          true,
			"cleanup_stale_after_days": 14,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PullRequest.AutoMerge || cfg.PullRequest.MergeMethod != "REBASE" {
		t.Fatalf("unexpected pull request config: %#v", cfg.PullRequest)
	}
	if len(cfg.PullRequest.RequiredCheckNames) != 2 ||
		cfg.PullRequest.RequiredCheckNames[0] != "go" ||
		cfg.PullRequest.RequiredCheckNames[1] != "make-all" {
		t.Fatalf("unexpected required checks: %#v", cfg.PullRequest.RequiredCheckNames)
	}
	if !cfg.Workspace.CleanupOrphans || cfg.Workspace.CleanupStaleAfterDays != 14 {
		t.Fatalf("unexpected workspace config: %#v", cfg.Workspace)
	}
}

func TestParseConfigRejectsMissingProject(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	_, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner": "miyataka",
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseConfigAcceptsClaudeCodeKind(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": " Claude-Code ",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Kind != "claude-code" {
		t.Fatalf("expected normalized kind \"claude-code\", got %q", cfg.Agent.Kind)
	}
}

func TestParseConfigDefaultsKindToCodex(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Kind != "codex" {
		t.Fatalf("expected default kind \"codex\", got %q", cfg.Agent.Kind)
	}
}

func TestParseConfigRejectsUnknownAgentKind(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	_, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": "gemini",
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown agent kind")
	}
	if !strings.Contains(err.Error(), "agent.kind") {
		t.Fatalf("expected error to mention agent.kind, got %v", err)
	}
}

func TestParseConfigDefaultsClaudeCodeWorkpadMarker(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": "claude-code",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracker.WorkpadMarker != "## Claude Workpad" {
		t.Fatalf("expected default marker \"## Claude Workpad\", got %q", cfg.Tracker.WorkpadMarker)
	}
}

func TestParseConfigDefaultsCodexWorkpadMarker(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracker.WorkpadMarker != "## Codex Workpad" {
		t.Fatalf("expected default marker \"## Codex Workpad\", got %q", cfg.Tracker.WorkpadMarker)
	}
}

func TestParseConfigPreservesExplicitWorkpadMarker(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
			"workpad_marker": "## Custom Workpad",
		},
		"agent": map[string]any{
			"kind": "claude-code",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracker.WorkpadMarker != "## Custom Workpad" {
		t.Fatalf("expected user-provided marker preserved, got %q", cfg.Tracker.WorkpadMarker)
	}
}

func TestParseConfigDefaultsClaudeCodeCommand(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": "claude-code",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := `cat "$SYMPHONY_PROMPT_FILE" | claude -p --dangerously-skip-permissions`
	if cfg.Agent.Command != expected {
		t.Fatalf("expected default claude-code command\n  want: %q\n  got:  %q", expected, cfg.Agent.Command)
	}
}

func TestParseConfigDefaultsClaudeCodeFallbackToCodex(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": "claude-code",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Fallback.Enabled == nil || !*cfg.Agent.Fallback.Enabled {
		t.Fatalf("expected claude-code fallback to default enabled, got %#v", cfg.Agent.Fallback.Enabled)
	}
	if cfg.Agent.Fallback.Kind != "codex" {
		t.Fatalf("expected fallback kind codex, got %q", cfg.Agent.Fallback.Kind)
	}
	if len(cfg.Agent.Fallback.On) != 1 || cfg.Agent.Fallback.On[0] != "claude_limit" {
		t.Fatalf("expected fallback_on claude_limit, got %#v", cfg.Agent.Fallback.On)
	}
	if !strings.Contains(cfg.Agent.Fallback.Command, "codex exec") {
		t.Fatalf("expected fallback command to use codex, got %q", cfg.Agent.Fallback.Command)
	}
}

func TestParseConfigCanDisableClaudeCodeFallback(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": "claude-code",
			"fallback": map[string]any{
				"enabled": false,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Fallback.Enabled == nil || *cfg.Agent.Fallback.Enabled {
		t.Fatalf("expected claude-code fallback disabled, got %#v", cfg.Agent.Fallback.Enabled)
	}
	if cfg.Agent.Fallback.Kind != "" || cfg.Agent.Fallback.Command != "" || len(cfg.Agent.Fallback.On) != 0 {
		t.Fatalf("expected disabled fallback not to receive defaults, got %#v", cfg.Agent.Fallback)
	}
}

func TestParseConfigAcceptsLegacyClaudeCodeFallbackShape(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind":           "claude-code",
			"fallback_kinds": []string{"codex"},
			"fallback_on":    []string{"claude_limit"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Fallback.Kind != "codex" {
		t.Fatalf("expected legacy fallback_kinds to configure codex, got %q", cfg.Agent.Fallback.Kind)
	}
	if len(cfg.Agent.Fallback.On) != 1 || cfg.Agent.Fallback.On[0] != "claude_limit" {
		t.Fatalf("expected legacy fallback_on claude_limit, got %#v", cfg.Agent.Fallback.On)
	}
}

func TestParseConfigAcceptsFallbackCommandOverride(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": "claude-code",
			"fallback": map[string]any{
				"kind":    "codex",
				"on":      []string{"claude_limit"},
				"command": "codex exec --model gpt-5.4 < \"$SYMPHONY_PROMPT_FILE\"",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Fallback.Command != "codex exec --model gpt-5.4 < \"$SYMPHONY_PROMPT_FILE\"" {
		t.Fatalf("expected custom fallback command preserved, got %q", cfg.Agent.Fallback.Command)
	}
}

func TestParseConfigPreservesExplicitClaudeCodeCommand(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind":    "claude-code",
			"command": "claude --print < $SYMPHONY_PROMPT_FILE",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Command != "claude --print < $SYMPHONY_PROMPT_FILE" {
		t.Fatalf("user command not preserved, got %q", cfg.Agent.Command)
	}
}

func TestParseConfigDefaultsCodexCommand(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := `mkdir -p .tmp
TMPDIR="$PWD/.tmp" TMP="$PWD/.tmp" TEMP="$PWD/.tmp" codex exec --sandbox workspace-write --skip-git-repo-check < "$SYMPHONY_PROMPT_FILE"`
	if cfg.Agent.Command != expected {
		t.Fatalf("expected default codex command\n  want: %q\n  got:  %q", expected, cfg.Agent.Command)
	}
}
