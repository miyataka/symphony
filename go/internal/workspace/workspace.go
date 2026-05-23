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
	root, err := canonicalRoot(m.Root)
	if err != nil {
		return "", false, err
	}

	path := filepath.Join(root, safeIdentifier(issue.Identifier))
	canonicalPath, exists, err := validateWorkspacePath(path, root)
	if err != nil {
		return "", false, err
	}
	path = canonicalPath
	created := false
	if exists {
		stat, err := os.Stat(path)
		if err != nil {
			return "", false, err
		}
		if !stat.IsDir() {
			if err := os.RemoveAll(path); err != nil {
				return "", false, err
			}
			created = true
		}
	} else {
		created = true
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

func (m Manager) PathForIssue(identifier string) (string, bool, error) {
	root, err := canonicalRoot(m.Root)
	if err != nil {
		return "", false, err
	}
	return validateWorkspacePath(filepath.Join(root, safeIdentifier(identifier)), root)
}

func (m Manager) Remove(ctx context.Context, identifier string, timeout time.Duration) error {
	root, err := canonicalRoot(m.Root)
	if err != nil {
		return err
	}
	path, _, err := validateWorkspacePath(filepath.Join(root, safeIdentifier(identifier)), root)
	if err != nil {
		return err
	}
	if strings.TrimSpace(m.Hooks.BeforeRemove) != "" {
		issue := tracker.Issue{Identifier: identifier}
		_ = runHook(ctx, path, m.Hooks.BeforeRemove, issue, timeout)
	}
	return os.RemoveAll(path)
}

func (m Manager) RemoveEntry(ctx context.Context, entry Entry, timeout time.Duration) error {
	root, err := canonicalRoot(m.Root)
	if err != nil {
		return err
	}
	path, _, err := validateWorkspacePath(entry.Path, root)
	if err != nil {
		return err
	}
	if strings.TrimSpace(m.Hooks.BeforeRemove) != "" {
		issue := tracker.Issue{Identifier: entry.Name}
		_ = runHook(ctx, path, m.Hooks.BeforeRemove, issue, timeout)
	}
	return os.RemoveAll(path)
}

func (m Manager) List() ([]Entry, error) {
	root, err := canonicalRoot(m.Root)
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

func canonicalRoot(rawRoot string) (string, error) {
	if strings.TrimSpace(rawRoot) == "" {
		return "", fmt.Errorf("workspace root is empty")
	}
	root, err := filepath.Abs(rawRoot)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(root), nil
}

func validateWorkspacePath(path, root string) (string, bool, error) {
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		absPath, err := filepath.Abs(cleanPath)
		if err != nil {
			return "", false, err
		}
		cleanPath = filepath.Clean(absPath)
	}

	canonicalPath, err := filepath.EvalSymlinks(cleanPath)
	switch {
	case err == nil:
		canonicalPath = filepath.Clean(canonicalPath)
		if canonicalPath == cleanRoot {
			return "", true, fmt.Errorf("workspace equals root: path=%q root=%q", canonicalPath, cleanRoot)
		}
		if !pathWithinRoot(canonicalPath, cleanRoot) {
			return "", true, fmt.Errorf("workspace resolves outside root: path=%q root=%q", canonicalPath, cleanRoot)
		}
		return canonicalPath, true, nil
	case os.IsNotExist(err):
		if !pathWithinRoot(cleanPath, cleanRoot) {
			return "", false, fmt.Errorf("workspace resolves outside root: path=%q root=%q", cleanPath, cleanRoot)
		}
		if cleanPath == cleanRoot {
			return "", false, fmt.Errorf("workspace equals root: path=%q root=%q", cleanPath, cleanRoot)
		}
		return cleanPath, false, nil
	default:
		return "", false, err
	}
}

func pathWithinRoot(path, root string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	value = strings.Trim(value, "._-")
	if value == "" {
		return "issue"
	}
	return value
}
