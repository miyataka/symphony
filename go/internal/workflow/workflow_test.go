package workflow

import (
	"os"
	"path/filepath"
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
	if len(cfg.Tracker.MonitorStates) != 2 ||
		cfg.Tracker.MonitorStates[0] != "Human Review" ||
		cfg.Tracker.MonitorStates[1] != "Merging" {
		t.Fatalf("unexpected monitor states: %#v", cfg.Tracker.MonitorStates)
	}
	if cfg.PullRequest.MergeMethod != "SQUASH" || !cfg.PullRequest.RequireApproval || !cfg.PullRequest.RequirePassingChecks {
		t.Fatalf("unexpected pull request defaults: %#v", cfg.PullRequest)
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
