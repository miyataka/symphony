package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/miyataka/symphony/go/internal/tracker"
	"github.com/miyataka/symphony/go/internal/workflow"
)

type Manager struct {
	Root  string
	Hooks workflow.HooksConfig
}

func (m Manager) Ensure(ctx context.Context, issue tracker.Issue, timeout time.Duration) (string, bool, error) {
	if m.Root == "" {
		return "", false, fmt.Errorf("workspace root is empty")
	}
	root, err := filepath.Abs(m.Root)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", false, err
	}

	path := filepath.Join(root, safeIdentifier(issue.Identifier))
	created := false
	if stat, err := os.Stat(path); err == nil {
		if !stat.IsDir() {
			if err := os.RemoveAll(path); err != nil {
				return "", false, err
			}
			created = true
		}
	} else if os.IsNotExist(err) {
		created = true
	} else {
		return "", false, err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", false, err
	}
	if created && strings.TrimSpace(m.Hooks.AfterCreate) != "" {
		if err := runHook(ctx, path, m.Hooks.AfterCreate, issue, timeout); err != nil {
			return "", false, fmt.Errorf("after_create hook: %w", err)
		}
	}
	return path, created, nil
}

func (m Manager) Remove(ctx context.Context, identifier string, timeout time.Duration) error {
	path := filepath.Join(m.Root, safeIdentifier(identifier))
	if strings.TrimSpace(m.Hooks.BeforeRemove) != "" {
		issue := tracker.Issue{Identifier: identifier}
		_ = runHook(ctx, path, m.Hooks.BeforeRemove, issue, timeout)
	}
	return os.RemoveAll(path)
}

func RunBefore(ctx context.Context, path string, hooks workflow.HooksConfig, issue tracker.Issue, timeout time.Duration) error {
	if strings.TrimSpace(hooks.BeforeRun) == "" {
		return nil
	}
	return runHook(ctx, path, hooks.BeforeRun, issue, timeout)
}

func RunAfter(ctx context.Context, path string, hooks workflow.HooksConfig, issue tracker.Issue, timeout time.Duration) {
	if strings.TrimSpace(hooks.AfterRun) == "" {
		return
	}
	_ = runHook(ctx, path, hooks.AfterRun, issue, timeout)
}

func runHook(parent context.Context, path, command string, issue tracker.Issue, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = path
	cmd.Env = append(os.Environ(), issueEnv(issue)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func issueEnv(issue tracker.Issue) []string {
	return []string{
		"SYMPHONY_ISSUE_ID=" + issue.ID,
		"SYMPHONY_ISSUE_IDENTIFIER=" + issue.Identifier,
		"SYMPHONY_ISSUE_TITLE=" + issue.Title,
		"SYMPHONY_ISSUE_URL=" + issue.URL,
		"SYMPHONY_ISSUE_STATE=" + issue.State,
	}
}

var unsafeIdentifier = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "issue"
	}
	value = unsafeIdentifier.ReplaceAllString(value, "_")
	return strings.Trim(value, "._-")
}
