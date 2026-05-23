package statusdashboard

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRenderToTerminalClearsAndHomesBeforeFrame(t *testing.T) {
	var buf bytes.Buffer

	err := RenderToTerminal(&buf, Snapshot{MaxAgents: 1}, Options{Width: 80, Color: false})
	if err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	if !strings.HasPrefix(got, "\x1b[H\x1b[2J") {
		t.Fatalf("expected terminal clear prefix, got %q", got)
	}
	if !strings.Contains(got, "SYMPHONY STATUS") {
		t.Fatalf("expected rendered status frame, got %q", got)
	}
}

func TestRunnerRendersInitialSnapshot(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	runner := Runner{
		Writer:          &buf,
		RefreshInterval: time.Hour,
		RenderInterval:  time.Millisecond,
		Options:         Options{Width: 80, Color: false},
		Snapshot: func() Snapshot {
			calls++
			cancel()
			return Snapshot{MaxAgents: 2}
		},
	}

	err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Fatalf("expected one initial snapshot, got %d", calls)
	}
	if !strings.Contains(buf.String(), "Agents: 0/2") {
		t.Fatalf("expected initial render, got %q", buf.String())
	}
}
