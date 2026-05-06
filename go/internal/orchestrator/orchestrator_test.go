package orchestrator

import (
	"context"
	"strings"
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

func TestRenderPromptUsesStrictVariables(t *testing.T) {
	_, err := renderPrompt("Issue {{ .Missing.TicketID }}", tracker.Issue{Identifier: "repo#1"}, 1)
	if err == nil {
		t.Fatal("expected missing variable error")
	}
	if !strings.Contains(err.Error(), `map has no entry for key "Missing"`) {
		t.Fatalf("unexpected error: %v", err)
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

func TestApplyReviewStatePolicyAutoMergesReadyPR(t *testing.T) {
	recorder := &recordingTracker{}
	cfg := testConfig()
	cfg.PullRequest.AutoMerge = true
	service := New(Options{
		Config:  cfg,
		Tracker: recorder,
	})
	handled := service.applyReviewStatePolicy(context.Background(), tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Merging",
		PullRequests: []tracker.PullRequest{{
			ID:                     "PR_1",
			Number:                 10,
			State:                  "OPEN",
			ReviewDecision:         "APPROVED",
			StatusCheckRollupState: "SUCCESS",
		}},
	})
	if !handled {
		t.Fatal("expected policy to handle issue")
	}
	if recorder.mergedPRID != "PR_1" {
		t.Fatalf("unexpected merged pr id: %q", recorder.mergedPRID)
	}
	if recorder.mergeMethod != "SQUASH" {
		t.Fatalf("unexpected merge method: %q", recorder.mergeMethod)
	}
	if recorder.workpad == "" {
		t.Fatal("expected workpad update")
	}
}

func TestApplyReviewStatePolicyRequiresNamedChecks(t *testing.T) {
	recorder := &recordingTracker{}
	cfg := testConfig()
	cfg.PullRequest.RequiredCheckNames = []string{"go", "make-all"}
	service := New(Options{
		Config:  cfg,
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
			Checks: []tracker.StatusCheck{
				{Name: "go", State: "SUCCESS"},
				{Name: "make-all", State: "PENDING"},
			},
		}},
	})
	if handled {
		t.Fatal("expected policy not to handle until required checks pass")
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

func TestRunIssuePropagatesAfterRunHookFailure(t *testing.T) {
	cfg := testConfig()
	cfg.Workspace.Root = t.TempDir()
	cfg.Agent.Command = ""
	cfg.Hooks.AfterRun = "exit 7"
	service := New(Options{
		Config:  cfg,
		Tracker: &recordingTracker{},
	})

	err := service.runIssue(context.Background(), tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "In Progress",
	})
	if err == nil {
		t.Fatal("expected after_run failure")
	}
	if !strings.Contains(err.Error(), "after_run hook") {
		t.Fatalf("unexpected error: %v", err)
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
	mergedPRID   string
	mergeMethod  string
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

func (r *recordingTracker) MergePullRequest(_ context.Context, _ tracker.Issue, pr tracker.PullRequest, opts tracker.MergeOptions) error {
	r.mergedPRID = pr.ID
	r.mergeMethod = opts.Method
	return nil
}
