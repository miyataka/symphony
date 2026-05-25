package orchestrator

import (
	"testing"
	"time"

	"github.com/miyataka/symphony/go/internal/codexappserver"
	"github.com/miyataka/symphony/go/internal/tracker"
)

func TestSnapshotCopiesRunningEntries(t *testing.T) {
	cfg := testConfig()
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Polling.IntervalMS = int((45 * time.Second) / time.Millisecond)
	cfg.Tracker.Owner = "miyataka"
	cfg.Tracker.OwnerType = "user"
	cfg.Tracker.ProjectNumber = 12
	service := New(Options{Config: cfg, Tracker: &recordingTracker{}})
	startedAt := time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC)
	lastProgressAt := time.Now().Add(-7 * time.Minute)
	service.running["I_1"] = &runHandle{
		issue: tracker.Issue{
			ID:         "I_1",
			Identifier: "repo#1",
			State:      "In Progress",
		},
		startedAt:      startedAt,
		lastProgressAt: lastProgressAt,
		retryCount:     2,
		turnCount:      4,
		agentKind:      "codex",
	}

	snapshot := service.Snapshot()

	if snapshot.MaxAgents != 3 {
		t.Fatalf("unexpected max agents: %d", snapshot.MaxAgents)
	}
	if snapshot.ProjectURL != "https://github.com/users/miyataka/projects/12" {
		t.Fatalf("unexpected project URL: %q", snapshot.ProjectURL)
	}
	if snapshot.NextRefresh != 45*time.Second {
		t.Fatalf("unexpected next refresh: %s", snapshot.NextRefresh)
	}
	if len(snapshot.Running) != 1 {
		t.Fatalf("expected one running entry, got %#v", snapshot.Running)
	}
	entry := snapshot.Running[0]
	if entry.Identifier != "repo#1" || entry.State != "In Progress" || entry.RetryCount != 2 || entry.TurnCount != 4 || entry.AgentKind != "codex" || !entry.StartedAt.Equal(startedAt) {
		t.Fatalf("unexpected running entry: %#v", entry)
	}
	if entry.HealthStatus != "quiet" || entry.HealthReason != "quiet" || entry.HealthNextAction != "watching" {
		t.Fatalf("unexpected health fields: %#v", entry)
	}
	if entry.HealthIdle < 7*time.Minute || entry.HealthIdle > 8*time.Minute {
		t.Fatalf("unexpected health idle: %s", entry.HealthIdle)
	}

	entry.Identifier = "mutated"
	if service.running["I_1"].issue.Identifier != "repo#1" {
		t.Fatalf("snapshot mutation changed service state")
	}
}

func TestRecordAppServerEventUpdatesRunningSnapshot(t *testing.T) {
	cfg := testConfig()
	service := New(Options{Config: cfg, Tracker: &recordingTracker{}})
	service.running["I_1"] = &runHandle{
		issue: tracker.Issue{
			ID:         "I_1",
			Identifier: "repo#1",
			State:      "In Progress",
		},
		startedAt:      time.Now().Add(-time.Minute),
		lastProgressAt: time.Now().Add(-time.Minute),
		agentKind:      "codex",
	}

	service.recordAppServerEvent("I_1", codexappserver.Event{
		Type:     codexappserver.EventNotification,
		ThreadID: "thread-abcdef123456",
		TurnID:   "turn-1",
		Method:   "thread/tokenUsage/updated",
		Usage: &codexappserver.TokenUsage{
			TotalTokens: 45,
		},
	})
	service.recordAppServerEvent("I_1", codexappserver.Event{
		Type:     codexappserver.EventToolCallUnsupported,
		ThreadID: "thread-abcdef123456",
		TurnID:   "turn-1",
		Method:   "item/tool/call",
		Message:  "Unsupported dynamic tool: linear_graphql",
	})

	snapshot := service.Snapshot()

	if len(snapshot.Running) != 1 {
		t.Fatalf("expected one running entry, got %#v", snapshot.Running)
	}
	entry := snapshot.Running[0]
	if entry.SessionID != "thread-abcdef123456/turn-1" {
		t.Fatalf("unexpected session id: %q", entry.SessionID)
	}
	if entry.TotalTokens != 45 {
		t.Fatalf("unexpected total tokens: %d", entry.TotalTokens)
	}
	if entry.LastEvent != "item/tool/call" {
		t.Fatalf("unexpected last event: %q", entry.LastEvent)
	}
	if entry.LastEventMessage != "Unsupported dynamic tool: linear_graphql" {
		t.Fatalf("unexpected last event message: %q", entry.LastEventMessage)
	}
}

func TestSnapshotCopiesScheduledRetries(t *testing.T) {
	cfg := testConfig()
	service := New(Options{Config: cfg, Tracker: &recordingTracker{}})
	dueAt := time.Now().Add(1250 * time.Millisecond)
	service.retrying["I_1"] = retryHandle{
		issue: tracker.Issue{
			ID:         "I_1",
			Identifier: "repo#1",
		},
		attempt: 4,
		dueAt:   dueAt,
		err:     "worker crashed\nrestarting cleanly",
	}

	snapshot := service.Snapshot()

	if len(snapshot.Retrying) != 1 {
		t.Fatalf("expected one retry entry, got %#v", snapshot.Retrying)
	}
	retry := snapshot.Retrying[0]
	if retry.Identifier != "repo#1" || retry.Attempt != 4 || retry.DueIn <= 0 || retry.Error != "worker crashed\nrestarting cleanly" {
		t.Fatalf("unexpected retry entry: %#v", retry)
	}

	retry.Identifier = "mutated"
	if service.retrying["I_1"].issue.Identifier != "repo#1" {
		t.Fatalf("snapshot mutation changed service retry state")
	}
}
