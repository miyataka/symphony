package tracker

import (
	"context"
	"time"
)

type Issue struct {
	ID          string
	Identifier  string
	Title       string
	Description string
	Priority    *int
	State       string
	BranchName  string
	URL         string
	Labels      []string
	BlockedBy   []Blocker
	CreatedAt   *time.Time
	UpdatedAt   *time.Time
}

type Blocker struct {
	ID         string
	Identifier string
	State      string
}

type Tracker interface {
	FetchCandidateIssues(context.Context) ([]Issue, error)
	FetchIssuesByStates(context.Context, []string) ([]Issue, error)
	FetchIssueStatesByIDs(context.Context, []string) ([]Issue, error)
}
