package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/miyataka/symphony/go/internal/tracker"
	"github.com/miyataka/symphony/go/internal/workflow"
	"github.com/miyataka/symphony/go/internal/workspace"
)

type Options struct {
	Config         workflow.Config
	PromptTemplate string
	Tracker        tracker.Tracker
	Logger         *slog.Logger
}

type Service struct {
	cfg            workflow.Config
	promptTemplate string
	tracker        tracker.Tracker
	logger         *slog.Logger
	workspaces     workspace.Manager

	mu      sync.Mutex
	running map[string]*runHandle
}

type runHandle struct {
	cancel       context.CancelFunc
	issue        tracker.Issue
	startedAt    time.Time
	retryCount   int
	turnCount    int
	loopReported bool
}

func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		cfg:            opts.Config,
		promptTemplate: opts.PromptTemplate,
		tracker:        opts.Tracker,
		logger:         logger,
		workspaces: workspace.Manager{
			Root:  opts.Config.Workspace.Root,
			Hooks: opts.Config.Hooks,
		},
		running: map[string]*runHandle{},
	}
}

func (s *Service) Run(ctx context.Context) error {
	if s.tracker == nil {
		return errors.New("tracker is required")
	}
	if err := s.cleanupWorkspaces(ctx); err != nil {
		s.logger.Warn("startup workspace cleanup failed", "error", err)
	}

	if err := s.poll(ctx); err != nil {
		s.logger.Warn("initial poll failed", "error", err)
	}

	ticker := time.NewTicker(s.cfg.PollInterval())
	defer ticker.Stop()
	var loopTicker *time.Ticker
	var loopC <-chan time.Time
	if s.cfg.LoopMonitor.Enabled {
		loopTicker = time.NewTicker(s.cfg.LoopMonitorInterval())
		defer loopTicker.Stop()
		loopC = loopTicker.C
	}

	for {
		select {
		case <-ctx.Done():
			s.stopAll()
			return ctx.Err()
		case <-ticker.C:
			if err := s.poll(ctx); err != nil {
				s.logger.Warn("poll failed", "error", err)
			}
		case <-loopC:
			s.checkLoopingRuns(ctx)
		}
	}
}

func (s *Service) poll(ctx context.Context) error {
	s.reconcileRunning(ctx)

	issues, err := s.tracker.FetchCandidateIssues(ctx)
	if err != nil {
		return err
	}
	sortIssues(issues)
	for _, issue := range issues {
		if s.applyReviewStatePolicy(ctx, issue) {
			continue
		}
		if len(issue.BlockedBy) > 0 {
			s.logger.Info(
				"issue dispatch skipped",
				"issue_id", issue.ID,
				"issue_identifier", issue.Identifier,
				"state", issue.State,
				"reason", "blocked_by",
				"blocked_by", blockerIdentifiers(issue.BlockedBy),
			)
			continue
		}
		if !s.canDispatch(issue) {
			continue
		}
		if s.todoState(issue.State) {
			if err := s.updateIssueState(ctx, issue, s.cfg.Tracker.StartState); err != nil {
				s.logger.Warn("failed to move issue to start state", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", s.cfg.Tracker.StartState, "error", err)
				continue
			}
			s.logStateTransition(issue, issue.State, s.cfg.Tracker.StartState, "dispatch_start")
			issue.State = s.cfg.Tracker.StartState
		}
		s.dispatch(ctx, issue, 0)
	}
	return nil
}

func (s *Service) applyReviewStatePolicy(ctx context.Context, issue tracker.Issue) bool {
	if len(issue.PullRequests) == 0 {
		return false
	}
	switch {
	case sameState(issue.State, s.cfg.Tracker.HandoffState):
		if issueHasActionablePRFeedback(issue) {
			s.moveIssueWithWorkpad(ctx, issue, s.cfg.Tracker.ReworkState, "Linked PR has actionable review feedback.")
			return true
		}
		if s.issueHasFailingPRChecks(issue) {
			s.moveIssueWithWorkpad(ctx, issue, s.cfg.Tracker.ReworkState, "Linked PR has failing checks.")
			return true
		}
		if s.issueHasReadyPR(issue) {
			s.moveIssueWithWorkpad(ctx, issue, s.cfg.Tracker.MergingState, "Linked PR is approved and checks are passing.")
			return true
		}
	case sameState(issue.State, s.cfg.Tracker.MergingState):
		if issueHasMergedPR(issue) {
			s.moveIssueWithWorkpad(ctx, issue, s.cfg.Tracker.DoneState, "Linked PR is merged.")
			return true
		}
		if s.issueNeedsPRRework(issue) {
			s.moveIssueWithWorkpad(ctx, issue, s.cfg.Tracker.ReworkState, "Linked PR needs rework before merge.")
			return true
		}
		if s.cfg.PullRequest.AutoMerge {
			if pr, ok := s.readyPullRequest(issue); ok {
				s.mergePullRequest(ctx, issue, pr)
				return true
			}
		}
	}
	return false
}

func (s *Service) moveIssueWithWorkpad(ctx context.Context, issue tracker.Issue, state, note string) {
	if state == "" || sameState(issue.State, state) {
		return
	}
	if err := s.updateIssueState(ctx, issue, state); err != nil {
		s.logger.Warn("failed to apply review state policy", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", state, "error", err)
		return
	}
	s.logStateTransition(issue, issue.State, state, note)
	issue.State = state
	s.upsertWorkpad(ctx, issue, s.workpadBody(issue, state, "", note))
}

func (s *Service) canDispatch(issue tracker.Issue) bool {
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" {
		return false
	}
	if !s.dispatchState(issue.State) {
		return false
	}
	if len(issue.BlockedBy) > 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.running[issue.ID]; ok {
		return false
	}
	if len(s.running) >= s.cfg.Agent.MaxConcurrentAgents {
		return false
	}
	limit := s.limitForState(issue.State)
	if limit <= 0 {
		return false
	}
	count := 0
	for _, handle := range s.running {
		if normalize(handle.issue.State) == normalize(issue.State) {
			count++
		}
	}
	return count < limit
}

func (s *Service) dispatch(parent context.Context, issue tracker.Issue, retryCount int) {
	ctx, cancel := context.WithCancel(parent)
	handle := &runHandle{cancel: cancel, issue: issue, startedAt: time.Now(), retryCount: retryCount}

	s.mu.Lock()
	s.running[issue.ID] = handle
	s.mu.Unlock()

	s.logger.Info("dispatching issue", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", issue.State, "retry", retryCount)

	go func() {
		err := s.runIssue(ctx, issue)
		s.mu.Lock()
		delete(s.running, issue.ID)
		s.mu.Unlock()

		if err == nil {
			s.handleSuccessfulRun(parent, issue)
			s.logger.Info("issue run completed", "issue_id", issue.ID, "issue_identifier", issue.Identifier)
			return
		}
		if parent.Err() != nil {
			s.logger.Info("issue run stopped", "issue_id", issue.ID, "issue_identifier", issue.Identifier)
			return
		}
		delay := s.retryDelay(retryCount + 1)
		s.logger.Warn("issue run failed; scheduling retry", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "delay", delay.String(), "error", err)
		time.AfterFunc(delay, func() {
			s.dispatchRetry(parent, issue, retryCount+1)
		})
	}()
}

func (s *Service) dispatchRetry(parent context.Context, issue tracker.Issue, retryCount int) {
	if parent.Err() != nil {
		return
	}
	refreshed, active, err := s.refreshIssue(parent, issue.ID)
	if err != nil {
		s.logger.Warn("failed to refresh issue before retry", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return
	}
	if !active {
		s.logger.Info("skipping retry for inactive issue", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", refreshed.State)
		return
	}
	if s.canDispatch(refreshed) {
		s.dispatch(parent, refreshed, retryCount)
	}
}

func (s *Service) runIssue(ctx context.Context, issue tracker.Issue) error {
	path, _, err := s.workspaces.Ensure(ctx, issue, s.cfg.HookTimeout())
	if err != nil {
		return err
	}
	s.upsertWorkpad(ctx, issue, s.workpadBody(issue, "Running", path, "Workspace prepared and agent execution started."))

	for turn := 1; turn <= s.cfg.Agent.MaxTurns; turn++ {
		if err := workspace.RunBefore(ctx, path, s.cfg.Hooks, issue, s.cfg.HookTimeout()); err != nil {
			return fmt.Errorf("before_run hook: %w", err)
		}
		if err := s.runAgentTurn(ctx, path, issue, turn); err != nil {
			if afterErr := workspace.RunAfter(ctx, path, s.cfg.Hooks, issue, s.cfg.HookTimeout()); afterErr != nil {
				return errors.Join(err, fmt.Errorf("after_run hook: %w", afterErr))
			}
			return err
		}
		if err := workspace.RunAfter(ctx, path, s.cfg.Hooks, issue, s.cfg.HookTimeout()); err != nil {
			return fmt.Errorf("after_run hook: %w", err)
		}
		s.recordCompletedTurn(issue.ID, turn)
		s.upsertWorkpad(ctx, issue, s.workpadBody(issue, "Running", path, fmt.Sprintf("Completed turn %d.", turn)))

		refreshed, active, err := s.refreshIssue(ctx, issue.ID)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		if s.writeback() != nil {
			return nil
		}
		issue = refreshed
	}
	return nil
}

func (s *Service) recordCompletedTurn(issueID string, turn int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if handle, ok := s.running[issueID]; ok && turn > handle.turnCount {
		handle.turnCount = turn
	}
}

func (s *Service) checkLoopingRuns(ctx context.Context) {
	if !s.cfg.LoopMonitor.Enabled {
		return
	}
	creator := s.issueCreator()
	if creator == nil {
		s.logger.Warn("loop monitor enabled but tracker does not support issue creation")
		return
	}

	type candidate struct {
		issue     tracker.Issue
		turnCount int
		runtime   time.Duration
	}
	now := time.Now()
	candidates := []candidate{}

	s.mu.Lock()
	for _, handle := range s.running {
		runtime := now.Sub(handle.startedAt)
		if handle.loopReported || runtime < s.cfg.LoopMonitorMaxRuntime() || handle.turnCount < s.cfg.LoopMonitor.MinTurns {
			continue
		}
		handle.loopReported = true
		candidates = append(candidates, candidate{
			issue:     handle.issue,
			turnCount: handle.turnCount,
			runtime:   runtime,
		})
	}
	s.mu.Unlock()

	for _, candidate := range candidates {
		child, err := s.createLoopSubIssue(ctx, candidate.issue, candidate.turnCount, candidate.runtime)
		if err != nil {
			s.logger.Warn("failed to create loop sub-issue", "issue_id", candidate.issue.ID, "issue_identifier", candidate.issue.Identifier, "error", err)
			continue
		}
		s.logger.Warn("looping issue suspected; sub-issue created", "issue_id", candidate.issue.ID, "issue_identifier", candidate.issue.Identifier, "sub_issue", child.Identifier, "runtime", candidate.runtime.String(), "turns", candidate.turnCount)
	}
}

func (s *Service) createLoopSubIssue(ctx context.Context, issue tracker.Issue, turnCount int, runtime time.Duration) (tracker.Issue, error) {
	creator := s.issueCreator()
	if creator == nil {
		return tracker.Issue{}, errors.New("tracker does not support issue creation")
	}
	body := s.loopSubIssueBody(issue, turnCount, runtime)
	child, err := creator.CreateIssue(ctx, tracker.IssueCreation{
		RepositoryNameWithOwner: issue.RepositoryNameWithOwner,
		Title:                   "Break down " + issue.Identifier,
		Body:                    body,
		ProjectState:            s.cfg.LoopMonitor.SubIssueState,
	})
	if err != nil {
		return tracker.Issue{}, err
	}
	childRef := child.URL
	if childRef == "" {
		childRef = child.Identifier
	}
	note := fmt.Sprintf("Loop suspected after %s and %d completed turns. Created sub-issue %s.", runtime.Round(time.Second), turnCount, childRef)
	s.upsertWorkpad(ctx, issue, s.workpadBody(issue, issue.State, "", note))
	return child, nil
}

func (s *Service) loopSubIssueBody(issue tracker.Issue, turnCount int, runtime time.Duration) string {
	lines := []string{
		"Loop suspected for " + issue.Identifier,
		"",
		"Parent: " + issue.Identifier,
	}
	if issue.URL != "" {
		lines = append(lines, "Parent URL: "+issue.URL)
	}
	lines = append(lines,
		"Runtime: "+runtime.Round(time.Second).String(),
		fmt.Sprintf("Completed turns: %d", turnCount),
		"State: "+issue.State,
		"",
		"Split this work into smaller, independently verifiable tasks before continuing the parent issue.",
	)
	return strings.Join(lines, "\n")
}

func (s *Service) handleSuccessfulRun(ctx context.Context, issue tracker.Issue) {
	refreshed, active, err := s.refreshIssue(ctx, issue.ID)
	if err != nil {
		s.logger.Warn("failed to refresh issue after run", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return
	}
	if !active {
		return
	}
	if s.cfg.Tracker.HandoffState != "" && !sameState(refreshed.State, s.cfg.Tracker.HandoffState) {
		if err := s.updateIssueState(ctx, refreshed, s.cfg.Tracker.HandoffState); err != nil {
			s.logger.Warn("failed to move issue to handoff state", "issue_id", refreshed.ID, "issue_identifier", refreshed.Identifier, "state", s.cfg.Tracker.HandoffState, "error", err)
			return
		}
		s.logStateTransition(refreshed, refreshed.State, s.cfg.Tracker.HandoffState, "agent_run_completed")
		refreshed.State = s.cfg.Tracker.HandoffState
	}
	s.upsertWorkpad(ctx, refreshed, s.workpadBody(refreshed, "Human Review", "", "Agent run completed and issue is ready for review."))
}

func (s *Service) runAgentTurn(parent context.Context, path string, issue tracker.Issue, turn int) error {
	prompt, err := renderPrompt(s.promptTemplate, issue, turn)
	if err != nil {
		return err
	}

	if strings.TrimSpace(s.cfg.Agent.Command) == "" {
		promptPath, err := writeWorkspacePrompt(path, prompt)
		if err != nil {
			return err
		}
		s.logger.Info("agent.command is empty; wrote prompt only", "issue_identifier", issue.Identifier, "prompt", promptPath)
		return nil
	}

	promptPath, cleanupPrompt, err := writeCommandPrompt(prompt)
	if err != nil {
		return err
	}
	defer cleanupPrompt()

	ctx, cancel := context.WithTimeout(parent, s.cfg.TurnTimeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-lc", s.cfg.Agent.Command)
	cmd.Dir = path
	cmd.Env = append(os.Environ(),
		"SYMPHONY_PROMPT_FILE="+promptPath,
		"SYMPHONY_TURN="+fmt.Sprint(turn),
	)
	cmd.Env = append(cmd.Env, issue.Env()...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("agent command: %w", err)
	}
	return nil
}

func writeWorkspacePrompt(path, prompt string) (string, error) {
	stateDir := filepath.Join(path, ".symphony")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", err
	}
	promptPath := filepath.Join(stateDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return "", err
	}
	return promptPath, nil
}

func writeCommandPrompt(prompt string) (string, func(), error) {
	stateDir, err := os.MkdirTemp("", "symphony-agent-prompt-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(stateDir)
	}
	promptPath := filepath.Join(stateDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return promptPath, cleanup, nil
}

func (s *Service) refreshIssue(ctx context.Context, id string) (tracker.Issue, bool, error) {
	issues, err := s.tracker.FetchIssueStatesByIDs(ctx, []string{id})
	if err != nil {
		return tracker.Issue{}, false, err
	}
	if len(issues) == 0 {
		return tracker.Issue{}, false, nil
	}
	issue := issues[0]
	return issue, s.dispatchState(issue.State), nil
}

func (s *Service) reconcileRunning(ctx context.Context) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.running))
	for id := range s.running {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	issues, err := s.tracker.FetchIssueStatesByIDs(ctx, ids)
	if err != nil {
		s.logger.Warn("running issue refresh failed", "error", err)
		return
	}
	visible := map[string]tracker.Issue{}
	for _, issue := range issues {
		visible[issue.ID] = issue
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, handle := range s.running {
		issue, ok := visible[id]
		if !ok || !s.dispatchState(issue.State) {
			s.logger.Info("stopping ineligible issue", "issue_id", id, "issue_identifier", handle.issue.Identifier)
			handle.cancel()
			continue
		}
		handle.issue = issue
	}
}

func (s *Service) cleanupWorkspaces(ctx context.Context) error {
	issues, err := s.tracker.FetchIssuesByStates(ctx, s.cfg.Tracker.TerminalStates)
	if err != nil {
		return err
	}
	known := map[string]struct{}{}
	for _, issue := range issues {
		known[workspace.NameForIssue(issue.Identifier)] = struct{}{}
		if issue.Identifier != "" {
			if err := s.workspaces.Remove(ctx, issue.Identifier, s.cfg.HookTimeout()); err != nil {
				s.logger.Warn("workspace cleanup failed", "issue_identifier", issue.Identifier, "error", err)
			} else {
				s.logger.Info("workspace removed for terminal issue", "issue_identifier", issue.Identifier)
			}
		}
	}
	if !s.cfg.Workspace.CleanupOrphans && s.cfg.Workspace.CleanupStaleAfterDays == 0 {
		return nil
	}
	activeIssues, err := s.tracker.FetchCandidateIssues(ctx)
	if err != nil {
		return err
	}
	for _, issue := range activeIssues {
		known[workspace.NameForIssue(issue.Identifier)] = struct{}{}
	}
	entries, err := s.workspaces.List()
	if err != nil {
		return err
	}
	staleCutoff := time.Time{}
	if s.cfg.Workspace.CleanupStaleAfterDays > 0 {
		staleCutoff = time.Now().AddDate(0, 0, -s.cfg.Workspace.CleanupStaleAfterDays)
	}
	for _, entry := range entries {
		_, knownWorkspace := known[entry.Name]
		stale := !staleCutoff.IsZero() && entry.ModTime.Before(staleCutoff)
		orphan := s.cfg.Workspace.CleanupOrphans && !knownWorkspace
		if !orphan && !stale {
			continue
		}
		if err := s.workspaces.RemoveEntry(ctx, entry, s.cfg.HookTimeout()); err != nil {
			s.logger.Warn("workspace cleanup failed", "workspace", entry.Path, "orphan", orphan, "stale", stale, "error", err)
			continue
		}
		s.logger.Info("workspace removed", "workspace", entry.Path, "orphan", orphan, "stale", stale)
	}
	return nil
}

func (s *Service) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, handle := range s.running {
		handle.cancel()
	}
}

func (s *Service) activeState(state string) bool {
	state = normalize(state)
	for _, active := range s.cfg.Tracker.ActiveStates {
		if normalize(active) == state {
			return true
		}
	}
	return false
}

func (s *Service) monitorState(state string) bool {
	state = normalize(state)
	for _, monitor := range s.cfg.Tracker.MonitorStates {
		if normalize(monitor) == state {
			return true
		}
	}
	return false
}

func (s *Service) dispatchState(state string) bool {
	return s.activeState(state) && !s.monitorState(state)
}

func (s *Service) todoState(state string) bool {
	return sameState(state, "Todo")
}

func (s *Service) writeback() tracker.Writeback {
	if writeback, ok := s.tracker.(tracker.Writeback); ok {
		return writeback
	}
	return nil
}

func (s *Service) pullRequestMerger() tracker.PullRequestMerger {
	if merger, ok := s.tracker.(tracker.PullRequestMerger); ok {
		return merger
	}
	return nil
}

func (s *Service) issueCreator() tracker.IssueCreator {
	if creator, ok := s.tracker.(tracker.IssueCreator); ok {
		return creator
	}
	return nil
}

func (s *Service) updateIssueState(ctx context.Context, issue tracker.Issue, state string) error {
	writeback := s.writeback()
	if writeback == nil {
		return nil
	}
	return writeback.UpdateIssueState(ctx, issue, state)
}

func (s *Service) upsertWorkpad(ctx context.Context, issue tracker.Issue, body string) {
	writeback := s.writeback()
	if writeback == nil {
		return
	}
	if err := writeback.UpsertWorkpad(ctx, issue, body); err != nil {
		s.logger.Warn("failed to upsert workpad", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
	}
}

func (s *Service) mergePullRequest(ctx context.Context, issue tracker.Issue, pr tracker.PullRequest) {
	merger := s.pullRequestMerger()
	if merger == nil {
		s.logger.Warn("pull request merge requested but tracker does not support it", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "pr_number", pr.Number)
		return
	}
	opts := tracker.MergeOptions{
		Method:         s.cfg.PullRequest.MergeMethod,
		CommitHeadline: fmt.Sprintf("%s: %s", issue.Identifier, issue.Title),
	}
	if err := merger.MergePullRequest(ctx, issue, pr, opts); err != nil {
		s.logger.Warn("failed to merge pull request", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "pr_number", pr.Number, "error", err)
		s.upsertWorkpad(ctx, issue, s.workpadBody(issue, issue.State, "", "Linked PR is ready, but automatic merge failed: "+err.Error()))
		return
	}
	s.logger.Info("pull request merge submitted", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "pr_number", pr.Number, "method", opts.Method)
	s.upsertWorkpad(ctx, issue, s.workpadBody(issue, issue.State, "", "Linked PR is ready and automatic merge was submitted."))
}

func (s *Service) logStateTransition(issue tracker.Issue, from, to, reason string) {
	s.logger.Info(
		"issue state transition",
		"issue_id", issue.ID,
		"issue_identifier", issue.Identifier,
		"from", from,
		"to", to,
		"reason", reason,
	)
}

func (s *Service) limitForState(state string) int {
	if s.cfg.Agent.MaxConcurrentAgentsByState == nil {
		return s.cfg.Agent.MaxConcurrentAgents
	}
	if limit, ok := s.cfg.Agent.MaxConcurrentAgentsByState[normalize(state)]; ok {
		return limit
	}
	if limit, ok := s.cfg.Agent.MaxConcurrentAgentsByState[state]; ok {
		return limit
	}
	return s.cfg.Agent.MaxConcurrentAgents
}

func (s *Service) retryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	delay := 10 * time.Second
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= s.cfg.MaxRetryBackoff() {
			return s.cfg.MaxRetryBackoff()
		}
	}
	return delay
}

func blockerIdentifiers(blockers []tracker.Blocker) []string {
	out := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		if blocker.Identifier != "" {
			out = append(out, blocker.Identifier)
			continue
		}
		out = append(out, blocker.ID)
	}
	return out
}

func renderPrompt(tmpl string, issue tracker.Issue, turn int) (string, error) {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = "You are working on {{ .Issue.Identifier }}.\n\nTitle: {{ .Issue.Title }}\n\n{{ .Issue.Description }}"
	}
	parsed, err := template.New("workflow").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = parsed.Execute(&buf, map[string]any{
		"Issue": issue,
		"Turn":  turn,
	})
	return strings.TrimSpace(buf.String()) + "\n", err
}

func sortIssues(issues []tracker.Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		left, right := issues[i], issues[j]
		if left.Priority != nil && right.Priority != nil && *left.Priority != *right.Priority {
			return *left.Priority < *right.Priority
		}
		if left.Priority != nil && right.Priority == nil {
			return true
		}
		if left.Priority == nil && right.Priority != nil {
			return false
		}
		if left.CreatedAt != nil && right.CreatedAt != nil && !left.CreatedAt.Equal(*right.CreatedAt) {
			return left.CreatedAt.Before(*right.CreatedAt)
		}
		return left.Identifier < right.Identifier
	})
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func sameState(left, right string) bool {
	return normalize(left) == normalize(right)
}

func issueHasActionablePRFeedback(issue tracker.Issue) bool {
	for _, pr := range issue.PullRequests {
		if pr.HasActionableFeedback() {
			return true
		}
	}
	return false
}

func (s *Service) issueNeedsPRRework(issue tracker.Issue) bool {
	for _, pr := range issue.PullRequests {
		if pr.HasActionableFeedback() || pr.RequiredChecksFailing(s.cfg.PullRequest.RequiredCheckNames) {
			return true
		}
	}
	return false
}

func (s *Service) issueHasFailingPRChecks(issue tracker.Issue) bool {
	for _, pr := range issue.PullRequests {
		if pr.RequiredChecksFailing(s.cfg.PullRequest.RequiredCheckNames) {
			return true
		}
	}
	return false
}

func issueHasMergedPR(issue tracker.Issue) bool {
	for _, pr := range issue.PullRequests {
		if pr.IsMerged() {
			return true
		}
	}
	return false
}

func (s *Service) issueHasReadyPR(issue tracker.Issue) bool {
	_, ok := s.readyPullRequest(issue)
	return ok
}

func (s *Service) readyPullRequest(issue tracker.Issue) (tracker.PullRequest, bool) {
	for _, pr := range issue.PullRequests {
		if s.pullRequestReadyForMerge(pr) {
			return pr, true
		}
	}
	return tracker.PullRequest{}, false
}

func (s *Service) pullRequestReadyForMerge(pr tracker.PullRequest) bool {
	if pr.State != "OPEN" {
		return false
	}
	if pr.IsDraft && !s.cfg.PullRequest.AllowDraft {
		return false
	}
	if s.cfg.PullRequest.RequireApproval && pr.ReviewDecision != "APPROVED" {
		return false
	}
	if pr.HasActionableFeedback() {
		return false
	}
	if s.cfg.PullRequest.RequirePassingChecks && !pr.RequiredChecksPassing(s.cfg.PullRequest.RequiredCheckNames) {
		return false
	}
	return true
}

func (s *Service) workpadBody(issue tracker.Issue, status, workspacePath, note string) string {
	marker := strings.TrimSpace(s.cfg.Tracker.WorkpadMarker)
	if marker == "" {
		marker = "## Codex Workpad"
	}
	lines := []string{
		marker,
		"",
		"### Status",
		"",
		"- Issue: " + issue.Identifier,
		"- State: " + status,
		"- Repository: " + issue.RepositoryNameWithOwner,
	}
	if workspacePath != "" {
		lines = append(lines, "- Workspace: "+workspacePath)
	}
	if len(issue.PullRequests) > 0 {
		lines = append(lines, "", "### Pull Requests", "")
		for _, pr := range issue.PullRequests {
			summary := fmt.Sprintf("- #%d %s", pr.Number, pr.State)
			if pr.ReviewDecision != "" {
				summary += " review=" + pr.ReviewDecision
			}
			if pr.StatusCheckRollupState != "" {
				summary += " checks=" + pr.StatusCheckRollupState
			}
			if len(pr.Checks) > 0 {
				checks := make([]string, 0, len(pr.Checks))
				for _, check := range pr.Checks {
					checks = append(checks, check.Name+"="+check.State)
				}
				summary += " check_details=[" + strings.Join(checks, ",") + "]"
			}
			if pr.UnresolvedThreadCount > 0 {
				summary += fmt.Sprintf(" unresolved_threads=%d", pr.UnresolvedThreadCount)
			}
			if pr.URL != "" {
				summary += " " + pr.URL
			}
			lines = append(lines, summary)
		}
	}
	lines = append(lines,
		"",
		"### Notes",
		"",
		"- "+note,
	)
	return strings.Join(lines, "\n")
}
