package shellcmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

const defaultCaptureLimit = 4096

type Result struct {
	Err      error
	Output   string
	TimedOut bool
	Seen     bool
}

type Recorder struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	seen      bool
	truncated bool
}

func NewRecorder(limit int) *Recorder {
	if limit <= 0 {
		limit = defaultCaptureLimit
	}
	return &Recorder{limit: limit}
}

func (r *Recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(bytes.TrimSpace(p)) > 0 {
		r.seen = true
	}
	if remaining := r.limit - r.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			_, _ = r.buf.Write(p[:remaining])
			r.truncated = true
		} else {
			_, _ = r.buf.Write(p)
		}
	} else if len(p) > 0 {
		r.truncated = true
	}
	return len(p), nil
}

func (r *Recorder) Seen() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen
}

func (r *Recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := r.buf.String()
	if r.truncated {
		out += "... (truncated)"
	}
	return out
}

func RunBash(ctx context.Context, dir, command string, env []string, stdout, stderr io.Writer) Result {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	recorder := NewRecorder(defaultCaptureLimit)
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	} else {
		cmd.Env = os.Environ()
	}
	cmd.Stdout = io.MultiWriter(stdout, recorder)
	cmd.Stderr = io.MultiWriter(stderr, recorder)
	configureProcessGroup(cmd)

	err := cmd.Run()
	return Result{
		Err:      err,
		Output:   recorder.String(),
		TimedOut: ctx.Err() != nil,
		Seen:     recorder.Seen(),
	}
}

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
}
