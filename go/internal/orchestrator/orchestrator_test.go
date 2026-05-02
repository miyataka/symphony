package orchestrator

import (
	"testing"
	"time"

	"github.com/miyataka/symphony/go/internal/tracker"
)

func TestRenderPrompt(t *testing.T) {
	prompt, err := renderPrompt("Issue {{ .Issue.Identifier }} turn {{ .Turn }}", tracker.Issue{Identifier: "repo#1"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "Issue repo#1 turn 3\n" {
		t.Fatalf("unexpected prompt: %q", prompt)
	}
}

func TestSortIssues(t *testing.T) {
	low, high := 4, 1
	old := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	issues := []tracker.Issue{
		{Identifier: "repo#2", Priority: &low, CreatedAt: &old},
		{Identifier: "repo#3", CreatedAt: &old},
		{Identifier: "repo#1", Priority: &high, CreatedAt: &newer},
	}
	sortIssues(issues)
	if issues[0].Identifier != "repo#1" || issues[1].Identifier != "repo#2" || issues[2].Identifier != "repo#3" {
		t.Fatalf("unexpected order: %#v", issues)
	}
}
