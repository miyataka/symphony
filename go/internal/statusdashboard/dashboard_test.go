package statusdashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderShowsRunningAgents(t *testing.T) {
	snapshot := Snapshot{
		Running: []RunningEntry{{
			Identifier: "repo#12",
			State:      "In Progress",
			AgentKind:  "codex",
			RetryCount: 1,
			TurnCount:  3,
			StartedAt:  time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC),
		}},
		MaxAgents: 4,
		Now:       time.Date(2026, 5, 23, 10, 2, 5, 0, time.UTC),
	}

	rendered := Render(snapshot, Options{Width: 100, Color: false})

	for _, want := range []string{
		"SYMPHONY STATUS",
		"Agents: 1/4",
		"Running",
		"repo#12",
		"In Progress",
		"codex",
		"2m 5s",
		"retry 1",
		"turns 3",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered dashboard to contain %q, got:\n%s", want, rendered)
		}
	}
}

func TestRenderTruncatesRowsToRequestedWidth(t *testing.T) {
	snapshot := Snapshot{
		Running: []RunningEntry{{
			Identifier: "very-long-repository-name#12345",
			State:      "Very Long State Name",
			AgentKind:  "claude-code",
			StartedAt:  time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC),
		}},
		MaxAgents: 1,
		Now:       time.Date(2026, 5, 23, 10, 0, 30, 0, time.UTC),
	}

	rendered := Render(snapshot, Options{Width: 56, Color: false})

	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if len(line) > 56 {
			t.Fatalf("expected line to fit width 56, got %d: %q\n%s", len(line), line, rendered)
		}
	}
	if !strings.Contains(rendered, "...") {
		t.Fatalf("expected narrow output to truncate cells, got:\n%s", rendered)
	}
}

func TestRenderFallsBackToCompactRowsForVeryNarrowWidth(t *testing.T) {
	snapshot := Snapshot{
		Running: []RunningEntry{{
			Identifier: "very-long-repository-name#12345",
			State:      "In Progress",
			AgentKind:  "codex",
		}},
		MaxAgents: 1,
	}

	rendered := Render(snapshot, Options{Width: 40, Color: false})

	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if len(line) > 40 {
			t.Fatalf("expected line to fit width 40, got %d: %q\n%s", len(line), line, rendered)
		}
	}
}

func TestRenderShowsIdleState(t *testing.T) {
	rendered := Render(Snapshot{MaxAgents: 2}, Options{Width: 80, Color: false})

	if !strings.Contains(rendered, "Agents: 0/2") {
		t.Fatalf("expected agent count, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "No active agents") {
		t.Fatalf("expected idle message, got:\n%s", rendered)
	}
}

func TestRenderColorOutputPreservesVisibleWidth(t *testing.T) {
	snapshot := Snapshot{
		Running: []RunningEntry{{
			Identifier: "repo#12",
			State:      "In Progress",
			AgentKind:  "codex",
		}},
		MaxAgents: 1,
	}

	rendered := Render(snapshot, Options{Width: 60, Color: true})

	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if lipgloss.Width(line) > 60 {
			t.Fatalf("expected visible line width <= 60, got %d: %q", lipgloss.Width(line), line)
		}
	}
}
