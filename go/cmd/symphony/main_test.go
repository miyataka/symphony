package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestOpenLogWriterEmptyPathReturnsStdout(t *testing.T) {
	w, closer, err := openLogWriter("")
	if err != nil {
		t.Fatal(err)
	}
	defer closer()
	if w != os.Stdout {
		t.Fatalf("expected os.Stdout for empty path, got %T", w)
	}
}

func TestOpenLogWriterAppendsAndCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "nested", "symphony.log")

	w, closer, err := openLogWriter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logger := newLogger(true, "info", w)
	logger.Info("first event", "issue_identifier", "miyataka/symphony#28")
	if err := closer(); err != nil {
		t.Fatal(err)
	}

	w2, closer2, err := openLogWriter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logger2 := newLogger(true, "info", w2)
	logger2.Info("second event", "retry", 1)
	if err := closer2(); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "first event") || !strings.Contains(got, "miyataka/symphony#28") {
		t.Fatalf("expected first event preserved across reopen, got %q", got)
	}
	if !strings.Contains(got, "second event") || !strings.Contains(got, `"retry":1`) {
		t.Fatalf("expected appended second event, got %q", got)
	}
	if strings.Count(got, "first event") != 1 {
		t.Fatalf("log file should be appended, not truncated, got %q", got)
	}
}
