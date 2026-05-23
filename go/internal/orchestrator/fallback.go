package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/miyataka/symphony/go/internal/tracker"
	"github.com/miyataka/symphony/go/internal/workspace"
)

const (
	claudeLimitReason          = "claude_limit"
	claudeLimitExitCode        = 88
	maxAgentOutputCaptureBytes = 64 * 1024
)

type agentProfile struct {
	Kind    string
	Command string
}

type agentCommandError struct {
	kind     string
	exitCode int
	output   string
	err      error
}

func (e *agentCommandError) Error() string {
	return fmt.Sprintf("agent command (%s): %v", e.kind, e.err)
}

func (e *agentCommandError) Unwrap() error {
	return e.err
}

type agentFallbackState struct {
	IssueID           string              `json:"issue_id"`
	IssueIdentifier   string              `json:"issue_identifier"`
	IssueState        string              `json:"issue_state"`
	OriginalAgentKind string              `json:"original_agent_kind"`
	FallbackAgentKind string              `json:"fallback_agent_kind"`
	Reason            string              `json:"reason"`
	Status            string              `json:"status"`
	Attempts          []agentAttemptState `json:"attempts,omitempty"`
	StartedAt         time.Time           `json:"started_at"`
	CompletedAt       *time.Time          `json:"completed_at,omitempty"`
}

type agentAttemptState struct {
	ID           string     `json:"id"`
	Turn         int        `json:"turn"`
	AgentKind    string     `json:"agent_kind"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason,omitempty"`
	FallbackFrom string     `json:"fallback_from,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

func (s *Service) primaryAgentProfile() agentProfile {
	return agentProfile{
		Kind:    s.cfg.Agent.Kind,
		Command: s.cfg.Agent.Command,
	}
}

func (s *Service) configuredFallbackProfile() (agentProfile, bool) {
	if s.cfg.Agent.Fallback.Enabled == nil || !*s.cfg.Agent.Fallback.Enabled {
		return agentProfile{}, false
	}
	if s.cfg.Agent.Fallback.Kind == "" {
		return agentProfile{}, false
	}
	return agentProfile{
		Kind:    s.cfg.Agent.Fallback.Kind,
		Command: s.cfg.Agent.Fallback.Command,
	}, true
}

func (s *Service) fallbackProfileForFailure(current agentProfile, err error) (agentProfile, string, bool) {
	fallbackProfile, ok := s.configuredFallbackProfile()
	if !ok {
		return agentProfile{}, "", false
	}
	if current.Kind != s.cfg.Agent.Kind || current.Kind != "claude-code" || fallbackProfile.Kind == current.Kind {
		return agentProfile{}, "", false
	}
	if !fallbackReasonEnabled(s.cfg.Agent.Fallback.On, claudeLimitReason) {
		return agentProfile{}, "", false
	}
	if !isClaudeLimitFailure(current.Kind, err) {
		return agentProfile{}, "", false
	}
	return fallbackProfile, claudeLimitReason, true
}

func fallbackReasonEnabled(reasons []string, reason string) bool {
	for _, configured := range reasons {
		if configured == reason {
			return true
		}
	}
	return false
}

func isClaudeLimitFailure(kind string, err error) bool {
	if kind != "claude-code" {
		return false
	}
	var commandErr *agentCommandError
	if !errors.As(err, &commandErr) {
		return false
	}
	if commandErr.exitCode == claudeLimitExitCode {
		return true
	}
	return outputLooksLikeClaudeLimit(commandErr.output)
}

func outputLooksLikeClaudeLimit(output string) bool {
	raw := strings.ToLower(output)
	if strings.Contains(raw, "rate_limit_error") {
		return true
	}
	text := strings.NewReplacer("_", " ", "-", " ").Replace(raw)
	hasClaudeContext := strings.Contains(text, "claude") || strings.Contains(text, "anthropic")
	if !hasClaudeContext {
		return false
	}
	for _, phrase := range []string{
		"claude ai usage limit reached",
		"usage limit reached",
		"usage limit exceeded",
		"rate limit reached",
		"rate limit exceeded",
		"rate limit error",
		"quota reached",
		"quota exceeded",
		"quota limit reached",
		"quota limit exceeded",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return strings.Contains(text, "too many requests") || strings.Contains(text, "429")
}

func (s *Service) resumeFallbackProfile(path string, issue tracker.Issue) (agentFallbackState, agentProfile, bool) {
	state, ok := readFallbackState(path)
	if !ok || state.IssueID != issue.ID || state.FallbackAgentKind == "" || state.Reason != claudeLimitReason {
		return agentFallbackState{}, agentProfile{}, false
	}
	if state.CompletedAt != nil || state.Status == "completed" {
		return agentFallbackState{}, agentProfile{}, false
	}
	if state.IssueState != "" && !sameState(state.IssueState, issue.State) {
		return agentFallbackState{}, agentProfile{}, false
	}
	fallbackProfile, ok := s.configuredFallbackProfile()
	if !ok || fallbackProfile.Kind != state.FallbackAgentKind {
		return agentFallbackState{}, agentProfile{}, false
	}
	return state, fallbackProfile, true
}

func completedFallbackState(path string, issue tracker.Issue) (agentFallbackState, bool) {
	state, ok := readFallbackState(path)
	if !ok || state.IssueID != issue.ID || state.FallbackAgentKind == "" {
		return agentFallbackState{}, false
	}
	if state.CompletedAt == nil && state.Status != "completed" {
		return agentFallbackState{}, false
	}
	if state.IssueState != "" && !sameState(state.IssueState, issue.State) {
		return agentFallbackState{}, false
	}
	return state, true
}

func (s *Service) recordFallbackState(path string, issue tracker.Issue, turn int, original, fallback agentProfile, reason string) error {
	now := time.Now().UTC()
	state := agentFallbackState{
		IssueID:           issue.ID,
		IssueIdentifier:   issue.Identifier,
		IssueState:        issue.State,
		OriginalAgentKind: original.Kind,
		FallbackAgentKind: fallback.Kind,
		Reason:            reason,
		Status:            "running",
		Attempts: []agentAttemptState{
			{
				ID:          fmt.Sprintf("turn-%d-%s", turn, original.Kind),
				Turn:        turn,
				AgentKind:   original.Kind,
				Status:      "failed",
				Reason:      reason,
				StartedAt:   now,
				CompletedAt: &now,
			},
			{
				ID:           fmt.Sprintf("turn-%d-%s-fallback", turn, fallback.Kind),
				Turn:         turn,
				AgentKind:    fallback.Kind,
				Status:       "running",
				Reason:       reason,
				FallbackFrom: original.Kind,
				StartedAt:    now,
			},
		},
		StartedAt: now,
	}
	return writeFallbackState(path, state)
}

func (s *Service) markFallbackStateCompleted(path string, issue tracker.Issue) {
	state, ok := readFallbackState(path)
	if !ok || state.IssueID != issue.ID || state.CompletedAt != nil {
		return
	}
	if state.IssueState != "" && !sameState(state.IssueState, issue.State) {
		return
	}
	now := time.Now().UTC()
	state.Status = "completed"
	state.CompletedAt = &now
	for i := range state.Attempts {
		attempt := &state.Attempts[i]
		if attempt.AgentKind == state.FallbackAgentKind && attempt.Status == "running" {
			attempt.Status = "completed"
			attempt.CompletedAt = &now
		}
	}
	if err := writeFallbackState(path, state); err != nil {
		s.logger.Warn("failed to mark agent fallback completed", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
	}
}

func (s *Service) markFallbackStateFailed(path string, issue tracker.Issue, profile agentProfile, err error) {
	state, ok := readFallbackState(path)
	if !ok || state.IssueID != issue.ID || state.FallbackAgentKind != profile.Kind || state.CompletedAt != nil {
		return
	}
	if state.IssueState != "" && !sameState(state.IssueState, issue.State) {
		return
	}
	now := time.Now().UTC()
	state.Status = "failed"
	for i := range state.Attempts {
		attempt := &state.Attempts[i]
		if attempt.AgentKind == state.FallbackAgentKind && attempt.Status == "running" {
			attempt.Status = "failed"
			attempt.CompletedAt = &now
			if attempt.Reason == "" {
				attempt.Reason = err.Error()
			}
		}
	}
	if writeErr := writeFallbackState(path, state); writeErr != nil {
		s.logger.Warn("failed to mark agent fallback failed", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", writeErr)
	}
}

func (s *Service) completedTurnNote(path string, issue tracker.Issue, profile agentProfile, turn int) string {
	if state, ok := readFallbackState(path); ok && state.IssueID == issue.ID && profile.Kind == state.FallbackAgentKind {
		return fmt.Sprintf("%s; completed turn %d with %s.", fallbackTransitionWorkpadNote(state.OriginalAgentKind, state.FallbackAgentKind), turn, profile.Kind)
	}
	return fmt.Sprintf("Completed turn %d.", turn)
}

func (s *Service) runFailureNoteForProfile(issue tracker.Issue, path string, profile agentProfile, err error) string {
	if state, ok := readFallbackState(path); ok && state.IssueID == issue.ID && profile.Kind == state.FallbackAgentKind {
		return fmt.Sprintf("%s; fallback agent failed: %s; leaving issue in its current state for retry instead of moving it to %s.", fallbackTransitionWorkpadNote(state.OriginalAgentKind, state.FallbackAgentKind), err.Error(), s.cfg.Tracker.HandoffState)
	}
	return s.runFailureNote(issue.State, err)
}

func (s *Service) fallbackStateForIssue(issue tracker.Issue) (agentFallbackState, bool) {
	if issue.Identifier == "" {
		return agentFallbackState{}, false
	}
	path := filepath.Join(s.cfg.Workspace.Root, workspace.NameForIssue(issue.Identifier))
	state, ok := readFallbackState(path)
	if !ok || state.IssueID != issue.ID {
		return agentFallbackState{}, false
	}
	if state.IssueState != "" && !sameState(state.IssueState, issue.State) {
		return agentFallbackState{}, false
	}
	return state, true
}

func fallbackTransitionNote(originalKind, fallbackKind string) string {
	return fmt.Sprintf("%s limit reached; retrying with %s", originalKind, fallbackKind)
}

func fallbackTransitionWorkpadNote(originalKind, fallbackKind string) string {
	return fmt.Sprintf(
		"%s; workspace and issue context preserved; Claude-only hooks, slash-skill assumptions, and per-agent restrictions are advisory in %s.",
		fallbackTransitionNote(originalKind, fallbackKind),
		fallbackKind,
	)
}

func readFallbackState(path string) (agentFallbackState, bool) {
	body, err := os.ReadFile(fallbackStatePath(path))
	if err != nil {
		return agentFallbackState{}, false
	}
	var state agentFallbackState
	if err := json.Unmarshal(body, &state); err != nil {
		return agentFallbackState{}, false
	}
	return state, true
}

func writeFallbackState(path string, state agentFallbackState) error {
	statePath := fallbackStatePath(path)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return os.WriteFile(statePath, body, 0o644)
}

func fallbackStatePath(path string) string {
	return filepath.Join(path, ".symphony", "agent-fallback.json")
}
