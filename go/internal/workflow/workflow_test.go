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
