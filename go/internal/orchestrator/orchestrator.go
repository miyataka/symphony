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
	cancel     context.CancelFunc
	issue      tracker.Issue
	startedAt  time.Time
	retryCount int
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
	if err := s.cleanupTerminalWorkspaces(ctx); err != nil {
		s.logger.Warn("startup terminal workspace cleanup failed", "error", err)
	}

	if err := s.poll(ctx); err != nil {
		s.logger.Warn("initial poll failed", "error", err)
	}

	ticker := time.NewTicker(s.cfg.PollInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.stopAll()
			return ctx.Err()
		case <-ticker.C:
			if err := s.poll(ctx); err != nil {
				s.logger.Warn("poll failed", "error", err)
			}
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
		if !s.canDispatch(issue) {
			continue
		}
		if s.todoState(issue.State) {
			if err := s.updateIssueState(ctx, issue, s.cfg.Tracker.StartState); err != nil {
				s.logger.Warn("failed to move issue to start state", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "state", s.cfg.Tracker.StartState, "error", err)
				continue
			}
			issue.State = s.cfg.Tracker.StartState
		}
		s.dispatch(ctx, issue, 0)
	}
	return nil
}

func (s *Service) canDispatch(issue tracker.Issue) bool {
	if issue.ID == "" || issue.Identifier == "" || issue.Title == "" {
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
			if parent.Err() == nil && s.canDispatch(issue) {
				s.dispatch(parent, issue, retryCount+1)
			}
		})
	}()
}

func (s *Service) runIssue(ctx context.Context, issue tracker.Issue) error {
	path, _, err := s.workspaces.Ensure(ctx, issue, s.cfg.HookTimeout())
	if err != nil {
		return err
	}
	s.upsertWorkpad(ctx, issue, workpadBody(issue, "Running", path, "Workspace prepared and agent execution started."))

	for turn := 1; turn <= s.cfg.Agent.MaxTurns; turn++ {
		if err := workspace.RunBefore(ctx, path, s.cfg.Hooks, issue, s.cfg.HookTimeout()); err != nil {
			return fmt.Errorf("before_run hook: %w", err)
		}
		if err := s.runAgentTurn(ctx, path, issue, turn); err != nil {
			workspace.RunAfter(ctx, path, s.cfg.Hooks, issue, s.cfg.HookTimeout())
			return err
		}
		workspace.RunAfter(ctx, path, s.cfg.Hooks, issue, s.cfg.HookTimeout())
		s.upsertWorkpad(ctx, issue, workpadBody(issue, "Running", path, fmt.Sprintf("Completed turn %d.", turn)))

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
		refreshed.State = s.cfg.Tracker.HandoffState
	}
	s.upsertWorkpad(ctx, refreshed, workpadBody(refreshed, "Human Review", "", "Agent run completed and issue is ready for review."))
}

func (s *Service) runAgentTurn(parent context.Context, path string, issue tracker.Issue, turn int) error {
	prompt, err := renderPrompt(s.promptTemplate, issue, turn)
	if err != nil {
		return err
	}
	stateDir := filepath.Join(path, ".symphony")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	promptPath := filepath.Join(stateDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o644); err != nil {
		return err
	}

	if strings.TrimSpace(s.cfg.Agent.Command) == "" {
		s.logger.Info("agent.command is empty; wrote prompt only", "issue_identifier", issue.Identifier, "prompt", promptPath)
		return nil
	}

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

func (s *Service) refreshIssue(ctx context.Context, id string) (tracker.Issue, bool, error) {
	issues, err := s.tracker.FetchIssueStatesByIDs(ctx, []string{id})
	if err != nil {
		return tracker.Issue{}, false, err
	}
	if len(issues) == 0 {
		return tracker.Issue{}, false, nil
	}
	issue := issues[0]
	return issue, s.activeState(issue.State), nil
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
		if !ok || !s.activeState(issue.State) {
			s.logger.Info("stopping ineligible issue", "issue_id", id, "issue_identifier", handle.issue.Identifier)
			handle.cancel()
			continue
		}
		handle.issue = issue
	}
}

func (s *Service) cleanupTerminalWorkspaces(ctx context.Context) error {
	issues, err := s.tracker.FetchIssuesByStates(ctx, s.cfg.Tracker.TerminalStates)
	if err != nil {
		return err
	}
	for _, issue := range issues {
		if issue.Identifier != "" {
			if err := s.workspaces.Remove(ctx, issue.Identifier, s.cfg.HookTimeout()); err != nil {
				s.logger.Warn("workspace cleanup failed", "issue_identifier", issue.Identifier, "error", err)
			}
		}
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

func (s *Service) todoState(state string) bool {
	return sameState(state, "Todo")
}

func (s *Service) writeback() tracker.Writeback {
	if writeback, ok := s.tracker.(tracker.Writeback); ok {
		return writeback
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

func renderPrompt(tmpl string, issue tracker.Issue, turn int) (string, error) {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = "You are working on {{ .Issue.Identifier }}.\n\nTitle: {{ .Issue.Title }}\n\n{{ .Issue.Description }}"
	}
	parsed, err := template.New("workflow").Parse(tmpl)
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

func workpadBody(issue tracker.Issue, status, workspacePath, note string) string {
	lines := []string{
		"## Codex Workpad",
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
