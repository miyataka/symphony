package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/miyataka/symphony/go/internal/tracker"
	"github.com/miyataka/symphony/go/internal/workflow"
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

func TestApplyReviewStatePolicyMovesHumanReviewToRework(t *testing.T) {
	recorder := &recordingTracker{}
	service := New(Options{
		Config:  testConfig(),
		Tracker: recorder,
	})
	handled := service.applyReviewStatePolicy(context.Background(), tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Human Review",
		PullRequests: []tracker.PullRequest{{
			ReviewDecision: "CHANGES_REQUESTED",
		}},
	})
	if !handled {
		t.Fatal("expected policy to handle issue")
	}
	if recorder.updatedState != "Rework" {
		t.Fatalf("unexpected updated state: %q", recorder.updatedState)
	}
	if recorder.workpad == "" {
		t.Fatal("expected workpad update")
	}
}

func TestApplyReviewStatePolicyMovesHumanReviewToMergingWhenPRReady(t *testing.T) {
	recorder := &recordingTracker{}
	service := New(Options{
		Config:  testConfig(),
		Tracker: recorder,
	})
	handled := service.applyReviewStatePolicy(context.Background(), tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Human Review",
		PullRequests: []tracker.PullRequest{{
			State:                  "OPEN",
			ReviewDecision:         "APPROVED",
			StatusCheckRollupState: "SUCCESS",
		}},
	})
	if !handled {
		t.Fatal("expected policy to handle issue")
	}
	if recorder.updatedState != "Merging" {
		t.Fatalf("unexpected updated state: %q", recorder.updatedState)
	}
	if recorder.workpad == "" {
		t.Fatal("expected workpad update")
	}
}

func TestApplyReviewStatePolicyDoesNotMoveHumanReviewWhenReadyPRIsDraft(t *testing.T) {
	recorder := &recordingTracker{}
	service := New(Options{
		Config:  testConfig(),
		Tracker: recorder,
	})
	handled := service.applyReviewStatePolicy(context.Background(), tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Human Review",
		PullRequests: []tracker.PullRequest{{
			State:                  "OPEN",
			IsDraft:                true,
			ReviewDecision:         "APPROVED",
			StatusCheckRollupState: "SUCCESS",
		}},
	})
	if handled {
		t.Fatal("expected policy not to handle draft PR")
	}
	if recorder.updatedState != "" {
		t.Fatalf("unexpected updated state: %q", recorder.updatedState)
	}
}

func TestApplyReviewStatePolicyMovesMergingToDoneWhenPRMerged(t *testing.T) {
	recorder := &recordingTracker{}
	service := New(Options{
		Config:  testConfig(),
		Tracker: recorder,
	})
	handled := service.applyReviewStatePolicy(context.Background(), tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Merging",
		PullRequests: []tracker.PullRequest{{
			State: "MERGED",
		}},
	})
	if !handled {
		t.Fatal("expected policy to handle issue")
	}
	if recorder.updatedState != "Done" {
		t.Fatalf("unexpected updated state: %q", recorder.updatedState)
	}
}

func TestApplyReviewStatePolicyMovesMergingToReworkWhenPRNeedsRework(t *testing.T) {
	recorder := &recordingTracker{}
	service := New(Options{
		Config:  testConfig(),
		Tracker: recorder,
	})
	handled := service.applyReviewStatePolicy(context.Background(), tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Merging",
		PullRequests: []tracker.PullRequest{{
			State:                  "OPEN",
			ReviewDecision:         "APPROVED",
			StatusCheckRollupState: "FAILURE",
		}},
	})
	if !handled {
		t.Fatal("expected policy to handle issue")
	}
	if recorder.updatedState != "Rework" {
		t.Fatalf("unexpected updated state: %q", recorder.updatedState)
	}
	if recorder.workpad == "" {
		t.Fatal("expected workpad update")
	}
}

func TestApplyReviewStatePolicyKeepsMergingWhenPRChecksPending(t *testing.T) {
	recorder := &recordingTracker{}
	service := New(Options{
		Config:  testConfig(),
		Tracker: recorder,
	})
	handled := service.applyReviewStatePolicy(context.Background(), tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Merging",
		PullRequests: []tracker.PullRequest{{
			State:                  "OPEN",
			ReviewDecision:         "APPROVED",
			StatusCheckRollupState: "PENDING",
		}},
	})
	if handled {
		t.Fatal("expected policy not to handle pending checks")
	}
	if recorder.updatedState != "" {
		t.Fatalf("unexpected updated state: %q", recorder.updatedState)
	}
}

func TestCanDispatchRequiresActiveState(t *testing.T) {
	service := New(Options{
		Config:  testConfig(),
		Tracker: &recordingTracker{},
	})
	if service.canDispatch(tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Human Review",
	}) {
		t.Fatal("expected monitor-only state not to dispatch")
	}
	if service.canDispatch(tracker.Issue{
		ID:         "I_2",
		Identifier: "repo#2",
		Title:      "Issue",
		State:      "Merging",
	}) {
		t.Fatal("expected merging state not to dispatch")
	}
	if !service.canDispatch(tracker.Issue{
		ID:         "I_3",
		Identifier: "repo#3",
		Title:      "Issue",
		State:      "Todo",
	}) {
		t.Fatal("expected active state to dispatch")
	}
}

func TestCanDispatchPrefersMonitorStateOverActiveState(t *testing.T) {
	cfg := testConfig()
	cfg.Tracker.ActiveStates = append(cfg.Tracker.ActiveStates, "Waiting")
	cfg.Tracker.MonitorStates = append(cfg.Tracker.MonitorStates, "Waiting")
	service := New(Options{
		Config:  cfg,
		Tracker: &recordingTracker{},
	})
	if service.canDispatch(tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Waiting",
	}) {
		t.Fatal("expected monitor state not to dispatch even when also active")
	}
}

func testConfig() workflow.Config {
	cfg, err := workflow.ParseConfig(map[string]any{
		"tracker": map[string]any{
			"token":          "token",
			"owner":          "miyataka",
			"project_number": 1,
		},
	})
	if err != nil {
		panic(err)
	}
	return cfg
}

type recordingTracker struct {
	updatedState string
	workpad      string
}

func (r *recordingTracker) FetchCandidateIssues(context.Context) ([]tracker.Issue, error) {
	return nil, nil
}

func (r *recordingTracker) FetchIssuesByStates(context.Context, []string) ([]tracker.Issue, error) {
	return nil, nil
}

func (r *recordingTracker) FetchIssueStatesByIDs(context.Context, []string) ([]tracker.Issue, error) {
	return nil, nil
}

func (r *recordingTracker) UpdateIssueState(_ context.Context, _ tracker.Issue, state string) error {
	r.updatedState = state
	return nil
}

func (r *recordingTracker) UpsertWorkpad(_ context.Context, _ tracker.Issue, body string) error {
	r.workpad = body
	return nil
}
