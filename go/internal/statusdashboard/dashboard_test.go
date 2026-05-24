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
			Identifier:       "repo#12",
			State:            "In Progress",
			AgentKind:        "codex",
			RetryCount:       1,
			TurnCount:        3,
			StartedAt:        time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC),
			SessionID:        "thread-abcdef1234567890",
			ProcessID:        "5252",
			TotalTokens:      89200,
			HealthStatus:     "suspect",
			HealthIdle:       125 * time.Second,
			LastEvent:        "codex/event/task_started",
			LastEventMessage: "mix test --cover",
		}},
		MaxAgents:    4,
		InputTokens:  250000,
		OutputTokens: 18500,
		TotalTokens:  268500,
		Throughput:   1842.7,
		ProjectURL:   "https://github.com/orgs/acme/projects/7",
		DashboardURL: "http://127.0.0.1:4000/",
		NextRefresh:  30 * time.Second,
		Now:          time.Date(2026, 5, 23, 10, 2, 5, 0, time.UTC),
	}

	rendered := Render(snapshot, Options{Width: 115, Color: false})

	for _, want := range []string{
		"╭─ SYMPHONY STATUS",
		"SYMPHONY STATUS",
		"│ Agents: 1/4",
		"│ Throughput: 1,842 tps",
		"│ Runtime: 2m 5s",
		"│ Tokens: in 250,000 | out 18,500 | total 268,500",
		"│ Project: https://github.com/orgs/acme/projects/7",
		"│ Dashboard: http://127.0.0.1:4000/",
		"│ Next refresh: 30s",
		"Running",
		"repo#12",
		"In Progress",
		"2m 5s",
		"Sus 2m",
		"89,200",
		"thre...567890",
		"mix test --cover",
		"Backoff queue",
		"No queued retries",
		"╰─",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected rendered dashboard to contain %q, got:\n%s", want, rendered)
		}
	}
}

func TestRenderShowsBackoffQueueAndRateLimits(t *testing.T) {
	snapshot := Snapshot{
		Running: []RunningEntry{{
			Identifier:       "MT-638",
			State:            "retrying",
			RetryCount:       3,
			TurnCount:        7,
			StartedAt:        time.Date(2026, 5, 23, 10, 0, 0, 0, time.UTC),
			TotalTokens:      14200,
			HealthStatus:     "stalled",
			HealthIdle:       601 * time.Second,
			LastEventMessage: "waiting on rate-limit backoff window",
		}},
		Retrying: []RetryEntry{
			{Identifier: "MT-450", Attempt: 4, DueIn: 1250 * time.Millisecond, Error: "rate limit exhausted"},
			{Identifier: "MT-451", Attempt: 2, DueIn: 3900 * time.Millisecond, Error: "retrying after API timeout with jitter"},
			{Identifier: "MT-452", Attempt: 6, DueIn: 8100 * time.Millisecond, Error: "worker crashed\nrestarting cleanly"},
		},
		MaxAgents: 10,
		RateLimits: &RateLimits{
			LimitID:   "gpt-5",
			Primary:   RateLimitBucket{Remaining: intPtr(0), Limit: intPtr(20000), ResetIn: 95 * time.Second},
			Secondary: RateLimitBucket{Remaining: intPtr(0), Limit: intPtr(60), ResetIn: 45 * time.Second},
			Credits:   Credits{HasCredits: boolPtr(false)},
		},
		Now: time.Date(2026, 5, 23, 10, 20, 25, 0, time.UTC),
	}

	rendered := Render(snapshot, Options{Width: 115, Color: false})

	for _, want := range []string{
		"│ Rate Limits: gpt-5 | primary 0/20,000 reset 95s | secondary 0/60 reset 45s | credits none",
		"MT-638",
		"20m 25s / 7",
		"Stl 10m",
		"14,200",
		"MT-450 attempt=4 in 1.250s error=rate limit exhausted",
		"MT-451 attempt=2 in 3.900s error=retrying after API timeout with jitter",
		"MT-452 attempt=6 in 8.100s error=worker crashed restarting cleanly",
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

	if !strings.Contains(rendered, "│ Agents: 0/2") {
		t.Fatalf("expected agent count, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "No active agents") {
		t.Fatalf("expected idle message, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "No queued retries") {
		t.Fatalf("expected idle backoff queue, got:\n%s", rendered)
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

func intPtr(value int) *int {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}
