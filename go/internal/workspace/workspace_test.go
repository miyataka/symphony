package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miyataka/symphony/go/internal/tracker"
	"github.com/miyataka/symphony/go/internal/workflow"
)

const testHookTimeout = 5 * time.Second

func TestEnsureDeterministicPathPerIssueIdentifier(t *testing.T) {
	root := t.TempDir()
	manager := Manager{Root: root}
	issue := tracker.Issue{Identifier: "MT/Det"}

	first, firstCreated, err := manager.Ensure(context.Background(), issue, testHookTimeout)
	if err != nil {
		t.Fatal(err)
	}
	second, secondCreated, err := manager.Ensure(context.Background(), issue, testHookTimeout)
	if err != nil {
		t.Fatal(err)
	}

	if !firstCreated {
		t.Fatal("expected first workspace creation")
	}
	if secondCreated {
		t.Fatal("expected existing workspace reuse")
	}
	if first != second {
		t.Fatalf("workspace path changed: first=%q second=%q", first, second)
	}
	if filepath.Base(first) != "MT_Det" {
		t.Fatalf("unexpected workspace basename: %q", filepath.Base(first))
	}
}

func TestEnsureReusesExistingIssueDirectory(t *testing.T) {
	root := t.TempDir()
	manager := Manager{
		Root: root,
		Hooks: workflow.HooksConfig{
			AfterCreate: "echo first > README.md",
		},
	}
	issue := tracker.Issue{Identifier: "MT-REUSE"}

	first, _, err := manager.Ensure(context.Background(), issue, testHookTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "local-progress.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	second, created, err := manager.Ensure(context.Background(), issue, testHookTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected existing workspace to be reused")
	}
	if second != first {
		t.Fatalf("workspace path changed: first=%q second=%q", first, second)
	}
	assertFileContents(t, filepath.Join(second, "README.md"), "changed\n")
	assertFileContents(t, filepath.Join(second, "local-progress.txt"), "in progress\n")
}

func TestEnsureReplacesStaleNonDirectoryPath(t *testing.T) {
	root := t.TempDir()
	stalePath := filepath.Join(root, "MT-STALE")
	if err := os.WriteFile(stalePath, []byte("old state\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, created, err := Manager{Root: root}.Ensure(context.Background(), tracker.Issue{Identifier: "MT-STALE"}, testHookTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected stale path replacement to count as creation")
	}
	expected, err := filepath.EvalSymlinks(stalePath)
	if err != nil {
		t.Fatal(err)
	}
	if path != expected {
		t.Fatalf("unexpected workspace path: %q", path)
	}
	if stat, err := os.Stat(path); err != nil || !stat.IsDir() {
		t.Fatalf("expected workspace directory, stat=%v err=%v", stat, err)
	}
}

func TestEnsureRejectsSymlinkEscapeUnderRoot(t *testing.T) {
	testRoot := t.TempDir()
	root := filepath.Join(testRoot, "workspaces")
	outside := filepath.Join(testRoot, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "MT-SYM")); err != nil {
		t.Fatal(err)
	}

	_, _, err := Manager{Root: root}.Ensure(context.Background(), tracker.Issue{Identifier: "MT-SYM"}, testHookTimeout)
	if err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
	if !strings.Contains(err.Error(), "workspace resolves outside root") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureCanonicalizesSymlinkedRoot(t *testing.T) {
	testRoot := t.TempDir()
	actualRoot := filepath.Join(testRoot, "actual-workspaces")
	linkedRoot := filepath.Join(testRoot, "linked-workspaces")
	if err := os.MkdirAll(actualRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actualRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}

	path, _, err := Manager{Root: linkedRoot}.Ensure(context.Background(), tracker.Issue{Identifier: "MT-LINK"}, testHookTimeout)
	if err != nil {
		t.Fatal(err)
	}
	canonicalActualRoot, err := filepath.EvalSymlinks(actualRoot)
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(canonicalActualRoot, "MT-LINK")
	if path != expected {
		t.Fatalf("workspace root was not canonicalized: got=%q want=%q", path, expected)
	}
}

func TestRemoveEntryRejectsWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Manager{Root: root}.RemoveEntry(context.Background(), Entry{Name: "root", Path: root}, testHookTimeout)
	if err == nil {
		t.Fatal("expected workspace root removal to be rejected")
	}
	if !strings.Contains(err.Error(), "workspace equals root") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertFileContents(t, filepath.Join(root, "marker.txt"), "keep\n")
}

func TestRemoveEntryRejectsWorkspaceOutsideRoot(t *testing.T) {
	testRoot := t.TempDir()
	root := filepath.Join(testRoot, "workspaces")
	outside := filepath.Join(testRoot, "outside")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "marker.txt")
	if err := os.WriteFile(outsideFile, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := Manager{Root: root}.RemoveEntry(context.Background(), Entry{Name: "outside", Path: outside}, testHookTimeout)
	if err == nil {
		t.Fatal("expected outside workspace removal to be rejected")
	}
	if !strings.Contains(err.Error(), "workspace resolves outside root") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertFileContents(t, outsideFile, "keep\n")
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("unexpected contents for %s: got=%q want=%q", path, got, want)
	}
}
