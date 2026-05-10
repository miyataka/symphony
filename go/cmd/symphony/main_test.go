package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/miyataka/symphony/go/internal/workflow"
)

func TestParseCommandDefaultsToRun(t *testing.T) {
	command, args, help := parseCommand(nil)
	if command != "run" || len(args) != 0 || help {
		t.Fatalf("unexpected command parse: command=%q args=%#v help=%v", command, args, help)
	}
}

func TestParseCommandTreatsPositionalWorkflowAsRunArg(t *testing.T) {
	command, args, help := parseCommand([]string{"tmp/custom/WORKFLOW.md"})
	if command != "run" || len(args) != 1 || args[0] != "tmp/custom/WORKFLOW.md" || help {
		t.Fatalf("unexpected command parse: command=%q args=%#v help=%v", command, args, help)
	}
}

func TestParseCommandRecognizesSubcommands(t *testing.T) {
	command, args, help := parseCommand([]string{"run", "WORKFLOW.github.md"})
	if command != "run" || len(args) != 1 || args[0] != "WORKFLOW.github.md" || help {
		t.Fatalf("unexpected run command parse: command=%q args=%#v help=%v", command, args, help)
	}

	command, args, help = parseCommand([]string{"setup-github-project", "WORKFLOW.github.md"})
	if command != "setup-github-project" || len(args) != 1 || args[0] != "WORKFLOW.github.md" || help {
		t.Fatalf("unexpected setup command parse: command=%q args=%#v help=%v", command, args, help)
	}
}

func TestParseWorkflowPathDefaultsToWORKFLOW(t *testing.T) {
	path, err := parseWorkflowPath("run", nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != "WORKFLOW.md" {
		t.Fatalf("unexpected workflow path: %q", path)
	}
}

func TestParseWorkflowPathAcceptsFlagOverride(t *testing.T) {
	path, err := parseWorkflowPath("run", []string{"--workflow", "WORKFLOW.github.md"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "WORKFLOW.github.md" {
		t.Fatalf("unexpected workflow path: %q", path)
	}
}

func TestParseWorkflowPathAcceptsPositionalOverride(t *testing.T) {
	path, err := parseWorkflowPath("run", []string{"tmp/custom/WORKFLOW.md"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "tmp/custom/WORKFLOW.md" {
		t.Fatalf("unexpected workflow path: %q", path)
	}
}

func TestParseWorkflowPathRejectsAmbiguousOverrides(t *testing.T) {
	_, err := parseWorkflowPath("run", []string{"--workflow", "WORKFLOW.github.md", "tmp/custom/WORKFLOW.md"})
	if err == nil {
		t.Fatal("expected ambiguous workflow path error")
	}
}

func TestParseWorkflowPathRejectsMultiplePositionals(t *testing.T) {
	_, err := parseWorkflowPath("run", []string{"one.md", "two.md"})
	if err == nil {
		t.Fatal("expected multiple positional workflow path error")
	}
}

func TestPrintGitHubProjectSetupIncludesBacklogBeforeTodo(t *testing.T) {
	cfg := workflow.Config{
		Tracker: workflow.TrackerConfig{
			Owner:          "miyataka",
			OwnerType:      "user",
			ProjectNumber:  2,
			StatusField:    "Status",
			BacklogStates:  []string{"Backlog"},
			ActiveStates:   []string{"Todo", "In Progress", "Rework"},
			MonitorStates:  []string{"Human Review", "Merging"},
			TerminalStates: []string{"Done", "Closed", "Cancelled", "Canceled", "Duplicate"},
		},
	}

	var buf bytes.Buffer
	printGitHubProjectSetup(&buf, cfg)
	out := buf.String()

	if !strings.Contains(out, "Backlog,Todo,In Progress,Rework,Human Review,Merging,Done,Closed,Cancelled,Canceled,Duplicate") {
		t.Fatalf("expected Backlog to lead the single-select options list, got:\n%s", out)
	}
	if backlogIdx, todoIdx := strings.Index(out, "Backlog"), strings.Index(out, "Todo"); backlogIdx == -1 || todoIdx == -1 || backlogIdx >= todoIdx {
		t.Fatalf("expected Backlog before Todo in output, got:\n%s", out)
	}
}
