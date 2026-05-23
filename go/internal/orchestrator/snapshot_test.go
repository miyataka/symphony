package orchestrator

import (
	"testing"
	"time"

	"github.com/miyataka/symphony/go/internal/tracker"
)

func TestSnapshotCopiesRunningEntries(t *testing.T) {
	cfg := testConfig()
	cfg.Agent.MaxConcurrentAgents = 3
	service := New(Options{Config: cfg, Tracker: &recordingTracker{}})
	startedAt := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	service.running["I_1"] = &runHandle{
		issue: tracker.Issue{
			ID:         "I_1",
			Identifier: "repo#1",
			State:      "In Progress",
		},
		startedAt:  startedAt,
		retryCount: 2,
		turnCount:  4,
		agentKind:  "codex",
	}

	snapshot := service.Snapshot()

	if snapshot.MaxAgents != 3 {
		t.Fatalf("unexpected max agents: %d", snapshot.MaxAgents)
	}
	if len(snapshot.Running) != 1 {
		t.Fatalf("expected one running entry, got %#v", snapshot.Running)
	}
	entry := snapshot.Running[0]
	if entry.Identifier != "repo#1" || entry.State != "In Progress" || entry.RetryCount != 2 || entry.TurnCount != 4 || entry.AgentKind != "codex" || !entry.StartedAt.Equal(startedAt) {
		t.Fatalf("unexpected running entry: %#v", entry)
	}

	entry.Identifier = "mutated"
	if service.running["I_1"].issue.Identifier != "repo#1" {
		t.Fatalf("snapshot mutation changed service state")
	}
}
