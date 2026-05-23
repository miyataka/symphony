package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/miyataka/symphony/go/internal/tracker"
)

const (
	runLeaseFileName          = "run_lease.json"
	runLeaseHeartbeatInterval = 30 * time.Second
	runLeaseStaleAfter        = 5 * time.Minute
)

type runLease struct {
	IssueID         string     `json:"issue_id"`
	IssueIdentifier string     `json:"issue_identifier"`
	IssueState      string     `json:"issue_state"`
	RunID           string     `json:"run_id"`
	AgentKind       string     `json:"agent_kind"`
	Hostname        string     `json:"hostname,omitempty"`
	PID             int        `json:"pid,omitempty"`
	Status          string     `json:"status"`
	Note            string     `json:"note,omitempty"`
	StartedAt       time.Time  `json:"started_at"`
	HeartbeatAt     time.Time  `json:"heartbeat_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

func (s *Service) shouldSkipForRunLease(ctx context.Context, issue tracker.Issue) bool {
	lease, path, ok, err := s.existingRunLease(issue)
	if err != nil {
		s.logger.Warn("run lease check failed; skipping dispatch to avoid duplicate run", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return true
	}
	if !ok || lease.Status != "running" {
		return false
	}
	if s.runLeaseFresh(lease) {
		s.logger.Info("issue dispatch skipped for fresh run lease", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "run_id", lease.RunID, "heartbeat_at", lease.HeartbeatAt)
		return true
	}

	note := fmt.Sprintf("Previous in-flight run lease %s is stale; last heartbeat was %s. Retrying active issue from current state.", lease.RunID, leaseHeartbeat(lease).Format(time.RFC3339))
	lease.Status = "stale"
	lease.Note = note
	now := time.Now().UTC()
	lease.UpdatedAt = now
	lease.CompletedAt = &now
	if err := writeRunLease(path, lease); err != nil {
		s.logger.Warn("failed to mark stale run lease; skipping dispatch to avoid duplicate run", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return true
	}
	s.upsertWorkpad(ctx, issue, s.workpadBody(issue, issue.State, path, note))
	return false
}

func (s *Service) existingRunLease(issue tracker.Issue) (runLease, string, bool, error) {
	path, exists, err := s.workspaces.PathForIssue(issue.Identifier)
	if err != nil || !exists {
		return runLease{}, path, false, err
	}
	lease, ok, err := readRunLease(path)
	if err != nil || !ok {
		return runLease{}, path, false, err
	}
	if !runLeaseMatchesIssue(lease, issue) {
		return runLease{}, path, false, nil
	}
	return lease, path, true, nil
}

func (s *Service) runLeaseFresh(lease runLease) bool {
	heartbeat := leaseHeartbeat(lease)
	if heartbeat.IsZero() {
		return false
	}
	return time.Since(heartbeat) <= runLeaseStaleAfter
}

func (s *Service) startRunLease(path string, issue tracker.Issue, agentKind string) (runLease, func(), error) {
	now := time.Now().UTC()
	hostname, _ := os.Hostname()
	lease := runLease{
		IssueID:         issue.ID,
		IssueIdentifier: issue.Identifier,
		IssueState:      issue.State,
		RunID:           fmt.Sprintf("%d-%d", os.Getpid(), now.UnixNano()),
		AgentKind:       agentKind,
		Hostname:        hostname,
		PID:             os.Getpid(),
		Status:          "running",
		StartedAt:       now,
		HeartbeatAt:     now,
		UpdatedAt:       now,
	}
	if err := writeRunLease(path, lease); err != nil {
		return runLease{}, nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(runLeaseHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				lease.HeartbeatAt = now.UTC()
				lease.UpdatedAt = lease.HeartbeatAt
				if err := writeRunLease(path, lease); err != nil {
					s.logger.Warn("failed to refresh run lease heartbeat", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
				}
			}
		}
	}()

	stop := func() {
		cancel()
		<-done
	}
	return lease, stop, nil
}

func (s *Service) finishRunLease(path string, issue tracker.Issue, runID, status, note string) {
	lease, ok, err := readRunLease(path)
	if err != nil {
		s.logger.Warn("failed to read run lease for completion", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "error", err)
		return
	}
	if !ok {
		return
	}
	if lease.RunID != runID {
		return
	}
	now := time.Now().UTC()
	lease.Status = status
	lease.Note = note
	lease.UpdatedAt = now
	lease.CompletedAt = &now
	if err := writeRunLease(path, lease); err != nil {
		s.logger.Warn("failed to complete run lease", "issue_id", issue.ID, "issue_identifier", issue.Identifier, "status", status, "error", err)
	}
}

func runLeaseCompletion(ctx context.Context, err error) (string, string) {
	if ctx.Err() != nil {
		return "interrupted", "Agent run interrupted by orchestrator shutdown."
	}
	if err != nil {
		return "failed", "Agent run failed: " + err.Error()
	}
	return "completed", "Agent run completed."
}

func readRunLease(path string) (runLease, bool, error) {
	body, err := os.ReadFile(runLeasePath(path))
	if os.IsNotExist(err) {
		return runLease{}, false, nil
	}
	if err != nil {
		return runLease{}, false, err
	}
	var lease runLease
	if err := json.Unmarshal(body, &lease); err != nil {
		return runLease{}, false, err
	}
	return lease, true, nil
}

func writeRunLease(path string, lease runLease) error {
	stateDir := filepath.Join(path, ".symphony")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	lease.UpdatedAt = lease.UpdatedAt.UTC()
	body, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, runLeaseFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, runLeasePath(path))
}

func runLeasePath(path string) string {
	return filepath.Join(path, ".symphony", runLeaseFileName)
}

func runLeaseMatchesIssue(lease runLease, issue tracker.Issue) bool {
	if lease.IssueID != "" && issue.ID != "" && lease.IssueID == issue.ID {
		return true
	}
	return lease.IssueIdentifier != "" && issue.Identifier != "" && lease.IssueIdentifier == issue.Identifier
}

func leaseHeartbeat(lease runLease) time.Time {
	if !lease.HeartbeatAt.IsZero() {
		return lease.HeartbeatAt
	}
	return lease.StartedAt
}
