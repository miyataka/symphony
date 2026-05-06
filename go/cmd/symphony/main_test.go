package main

import "testing"

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
