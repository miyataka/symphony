package shellcmd

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRunBashCapturesOutputAndActivity(t *testing.T) {
	result := RunBash(context.Background(), t.TempDir(), "echo visible-output; echo visible-error >&2", nil, io.Discard, io.Discard)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !result.Seen {
		t.Fatal("expected command output activity")
	}
	for _, want := range []string{"visible-output", "visible-error"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("expected captured output to contain %q, got %q", want, result.Output)
		}
	}
}

func TestRunBashTimeoutKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	result := RunBash(ctx, t.TempDir(), "echo before-timeout; sleep 5", nil, io.Discard, io.Discard)
	elapsed := time.Since(start)

	if !result.TimedOut {
		t.Fatalf("expected timeout, got err=%v", result.Err)
	}
	if elapsed > time.Second {
		t.Fatalf("timeout did not stop process group promptly: elapsed=%s", elapsed)
	}
	if !strings.Contains(result.Output, "before-timeout") {
		t.Fatalf("expected timeout output to be captured, got %q", result.Output)
	}
}

func TestRecorderTruncatesCapturedOutput(t *testing.T) {
	recorder := NewRecorder(8)
	if _, err := recorder.Write([]byte("1234567890")); err != nil {
		t.Fatal(err)
	}
	got := recorder.String()
	if !strings.Contains(got, "12345678") || !strings.Contains(got, "truncated") {
		t.Fatalf("expected truncated output, got %q", got)
	}
}
