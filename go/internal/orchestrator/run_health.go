package orchestrator

import (
	"time"

	"github.com/miyataka/symphony/go/internal/workflow"
)

type runHealth struct {
	Status     string
	Reason     string
	NextAction string
	Idle       time.Duration
}

func evaluateRunHealth(handle runHandle, now time.Time, cfg workflow.RunHealthConfig) runHealth {
	progressAt := handle.lastProgressAt
	if progressAt.IsZero() {
		progressAt = handle.startedAt
	}
	if progressAt.IsZero() || now.Before(progressAt) {
		progressAt = now
	}
	idle := now.Sub(progressAt)
	if !cfg.Enabled {
		return runHealth{Status: "active", Reason: "disabled", NextAction: "watching", Idle: idle}
	}

	quietAfter := durationFromMillis(cfg.QuietAfterMS, 5*time.Minute)
	suspectAfter := durationFromMillis(cfg.SuspectAfterMS, 10*time.Minute)
	selfReportTimeout := durationFromMillis(cfg.SelfReportTimeoutMS, 2*time.Minute)

	if idle >= suspectAfter+selfReportTimeout {
		return runHealth{Status: "stalled", Reason: "self_report_missing", NextAction: "human_attention", Idle: idle}
	}
	if idle >= suspectAfter {
		return runHealth{Status: "suspect", Reason: "no_meaningful_progress", NextAction: "requesting_self_report", Idle: idle}
	}
	if idle >= quietAfter {
		return runHealth{Status: "quiet", Reason: "quiet", NextAction: "watching", Idle: idle}
	}
	return runHealth{Status: "active", Reason: "recent_progress", NextAction: "watching", Idle: idle}
}

func durationFromMillis(value int, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}
