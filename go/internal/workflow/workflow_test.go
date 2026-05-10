package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if !strings.Contains(workflow, "{{ range .Issue.PRReviewComments }}") {
		t.Fatal("expected GitHub workflow to include unresolved PR review comments in agent prompt")
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
			"log_json":  true,
			"log_level": "DEBUG",
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

func TestParseConfigCodexLeavesCommandEmpty(t *testing.T) {
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
	if cfg.Agent.Command != "" {
		t.Fatalf("expected codex default to leave command empty, got %q", cfg.Agent.Command)
	}
}
