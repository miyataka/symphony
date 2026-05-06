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

type Entry struct {
	Name    string
	Path    string
	ModTime time.Time
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

func (m Manager) RemoveEntry(ctx context.Context, entry Entry, timeout time.Duration) error {
	if strings.TrimSpace(m.Hooks.BeforeRemove) != "" {
		issue := tracker.Issue{Identifier: entry.Name}
		_ = runHook(ctx, entry.Path, m.Hooks.BeforeRemove, issue, timeout)
	}
	return os.RemoveAll(entry.Path)
}

func (m Manager) List() ([]Entry, error) {
	root, err := filepath.Abs(m.Root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, Entry{
			Name:    entry.Name(),
			Path:    filepath.Join(root, entry.Name()),
			ModTime: info.ModTime(),
		})
	}
	return out, nil
}

func NameForIssue(identifier string) string {
	return safeIdentifier(identifier)
}

func RunBefore(ctx context.Context, path string, hooks workflow.HooksConfig, issue tracker.Issue, timeout time.Duration) error {
	if strings.TrimSpace(hooks.BeforeRun) == "" {
		return nil
	}
	return runHook(ctx, path, hooks.BeforeRun, issue, timeout)
}

func RunAfter(ctx context.Context, path string, hooks workflow.HooksConfig, issue tracker.Issue, timeout time.Duration) error {
	if strings.TrimSpace(hooks.AfterRun) == "" {
		return nil
	}
	return runHook(ctx, path, hooks.AfterRun, issue, timeout)
}

func runHook(parent context.Context, path, command string, issue tracker.Issue, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = path
	cmd.Env = append(os.Environ(), issue.Env()...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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
