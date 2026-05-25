package orchestrator

import (
	"testing"
	"time"

	"github.com/miyataka/symphony/go/internal/workflow"
)

func TestEvaluateRunHealthClassifiesIdleDurations(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	cfg := workflow.RunHealthConfig{
		Enabled:             true,
		QuietAfterMS:        int((5 * time.Minute) / time.Millisecond),
		SuspectAfterMS:      int((10 * time.Minute) / time.Millisecond),
		SelfReportTimeoutMS: int((2 * time.Minute) / time.Millisecond),
	}

	for _, tc := range []struct {
		name       string
		progressAt time.Time
		status     string
		reason     string
		nextAction string
	}{
		{
			name:       "active before quiet threshold",
			progressAt: now.Add(-4 * time.Minute),
			status:     "active",
			reason:     "recent_progress",
			nextAction: "watching",
		},
		{
			name:       "quiet after quiet threshold",
			progressAt: now.Add(-7 * time.Minute),
			status:     "quiet",
			reason:     "quiet",
			nextAction: "watching",
		},
		{
			name:       "suspect after suspect threshold",
			progressAt: now.Add(-11 * time.Minute),
			status:     "suspect",
			reason:     "no_meaningful_progress",
			nextAction: "requesting_self_report",
		},
		{
			name:       "stalled after self report timeout",
			progressAt: now.Add(-13 * time.Minute),
			status:     "stalled",
			reason:     "self_report_missing",
			nextAction: "human_attention",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			health := evaluateRunHealth(runHandle{startedAt: now.Add(-30 * time.Minute), lastProgressAt: tc.progressAt}, now, cfg)

			if health.Status != tc.status || health.Reason != tc.reason || health.NextAction != tc.nextAction {
				t.Fatalf("unexpected health: %#v", health)
			}
			if health.Idle != now.Sub(tc.progressAt) {
				t.Fatalf("unexpected idle duration: %s", health.Idle)
			}
		})
	}
}

func TestEvaluateRunHealthFallsBackToStartedAt(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	cfg := workflow.RunHealthConfig{
		Enabled:             true,
		QuietAfterMS:        int(time.Minute / time.Millisecond),
		SuspectAfterMS:      int((2 * time.Minute) / time.Millisecond),
		SelfReportTimeoutMS: int(time.Minute / time.Millisecond),
	}

	health := evaluateRunHealth(runHandle{startedAt: now.Add(-90 * time.Second)}, now, cfg)

	if health.Status != "quiet" || health.Idle != 90*time.Second {
		t.Fatalf("expected quiet health based on startedAt, got %#v", health)
	}
}
