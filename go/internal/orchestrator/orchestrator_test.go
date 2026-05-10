package orchestrator

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

func TestRenderPrompt(t *testing.T) {
	prompt, err := renderPrompt("Issue {{ .Issue.Identifier }} turn {{ .Turn }}", tracker.Issue{Identifier: "repo#1"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "Issue repo#1 turn 3\n" {
		t.Fatalf("unexpected prompt: %q", prompt)
	}
}

func TestRenderPromptCanRenderIssueComments(t *testing.T) {
	prompt, err := renderPrompt(`{{ range .Issue.Comments }}{{ .Author }}: {{ .Body }} {{ .URL }}{{ end }}`, tracker.Issue{
		Identifier: "repo#1",
		Comments: []tracker.IssueComment{{
			Author: "miyataka",
			Body:   "please read the linked PR comment",
			URL:    "https://github.com/miyataka/symphony/issues/1#issuecomment-1",
		}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "miyataka: please read the linked PR comment") {
		t.Fatalf("unexpected prompt: %q", prompt)
	}
}

func TestRenderPromptCanRenderPRReviewComments(t *testing.T) {
	prompt, err := renderPrompt(
		`{{ range .Issue.PRReviewComments }}{{ .Author }} on PR #{{ .PRNumber }} {{ .Path }}:{{ .Line }} {{ .URL }}: {{ .Body }}{{ end }}`,
		tracker.Issue{
			Identifier: "repo#1",
			PRReviewComments: []tracker.PRReviewComment{{
				Author:   "reviewer",
				PRNumber: 17,
				PRURL:    "https://github.com/miyataka/symphony/pull/17",
				Path:     "go/orchestrator.go",
				Line:     42,
				URL:      "https://github.com/miyataka/symphony/pull/17#discussion_r1",
				Body:     "needs early return",
			}},
		},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "reviewer on PR #17 go/orchestrator.go:42 https://github.com/miyataka/symphony/pull/17#discussion_r1: needs early return") {
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

func TestRunAgentTurnUsesTempPromptOutsideWorkspace(t *testing.T) {
	cfg := testConfig()
	cfg.Agent.Command = `printf '%s' "$SYMPHONY_PROMPT_FILE" > prompt_path.txt; cat "$SYMPHONY_PROMPT_FILE" > prompt_stdin.txt`
	service := New(Options{
		Config:         cfg,
		PromptTemplate: "Issue {{ .Issue.Identifier }} turn {{ .Turn }}",
		Tracker:        &recordingTracker{},
	})
	workspace := t.TempDir()

	err := service.runAgentTurn(context.Background(), workspace, tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "In Progress",
	}, 2)
	if err != nil {
		t.Fatal(err)
	}

	stdin, err := os.ReadFile(filepath.Join(workspace, "prompt_stdin.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != "Issue repo#1 turn 2\n" {
		t.Fatalf("unexpected prompt stdin: %q", stdin)
	}
	promptPathBytes, err := os.ReadFile(filepath.Join(workspace, "prompt_path.txt"))
	if err != nil {
		t.Fatal(err)
	}
	promptPath := string(promptPathBytes)
	rel, err := filepath.Rel(workspace, promptPath)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		t.Fatalf("prompt path should be outside workspace: %q", promptPath)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".symphony", "prompt.md")); !os.IsNotExist(err) {
		t.Fatalf("workspace prompt should not be written for agent.command runs: %v", err)
	}
	if _, err := os.Stat(promptPath); !os.IsNotExist(err) {
		t.Fatalf("temporary prompt should be cleaned up after agent.command: %v", err)
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
	if recorder.mergeMethod != "MERGE" {
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

func TestDispatchRetryRefreshesIssueStateBeforeDispatch(t *testing.T) {
	recorder := &recordingTracker{
		issueStatesByID: []tracker.Issue{{
			ID:         "I_1",
			Identifier: "repo#1",
			Title:      "Issue",
			State:      "Human Review",
		}},
	}
	service := New(Options{
		Config:  testConfig(),
		Tracker: recorder,
	})

	service.dispatchRetry(context.Background(), tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Rework",
	}, 1)

	if len(recorder.fetchIssueStateIDs) != 1 {
		t.Fatalf("expected retry to refresh issue state, got %d calls", len(recorder.fetchIssueStateIDs))
	}
	if _, ok := service.running["I_1"]; ok {
		t.Fatal("expected inactive refreshed issue not to dispatch")
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

func TestApplyReviewStatePolicyUsesConfiguredWorkpadMarker(t *testing.T) {
	cfg := testConfig()
	cfg.Tracker.WorkpadMarker = "## Claude Workpad"
	recorder := &recordingTracker{}
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
			ReviewDecision: "CHANGES_REQUESTED",
		}},
	})
	if !handled {
		t.Fatal("expected policy to handle issue")
	}
	if recorder.workpad == "" {
		t.Fatal("expected workpad update")
	}
	if !strings.HasPrefix(recorder.workpad, "## Claude Workpad") {
		t.Fatalf("expected workpad body to start with configured marker, got: %q", recorder.workpad)
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
	updatedState       string
	workpad            string
	mergedPRID         string
	mergeMethod        string
	issueStatesByID    []tracker.Issue
	fetchIssueStateIDs [][]string
}

func (r *recordingTracker) FetchCandidateIssues(context.Context) ([]tracker.Issue, error) {
	return nil, nil
}

func (r *recordingTracker) FetchIssuesByStates(context.Context, []string) ([]tracker.Issue, error) {
	return nil, nil
}

func (r *recordingTracker) FetchIssueStatesByIDs(_ context.Context, ids []string) ([]tracker.Issue, error) {
	r.fetchIssueStateIDs = append(r.fetchIssueStateIDs, append([]string(nil), ids...))
	return r.issueStatesByID, nil
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
