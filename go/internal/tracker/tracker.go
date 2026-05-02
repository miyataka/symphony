package tracker

import (
	"context"
	"time"
)

type Issue struct {
	ID                      string
	Identifier              string
	Title                   string
	Description             string
	Priority                *int
	State                   string
	BranchName              string
	URL                     string
	RepositoryNameWithOwner string
	RepositorySSHURL        string
	RepositoryHTMLURL       string
	Labels                  []string
	BlockedBy               []Blocker
	CreatedAt               *time.Time
	UpdatedAt               *time.Time
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

func (i Issue) Env() []string {
	return []string{
		"SYMPHONY_ISSUE_ID=" + i.ID,
		"SYMPHONY_ISSUE_IDENTIFIER=" + i.Identifier,
		"SYMPHONY_ISSUE_TITLE=" + i.Title,
		"SYMPHONY_ISSUE_URL=" + i.URL,
		"SYMPHONY_ISSUE_STATE=" + i.State,
		"SYMPHONY_REPOSITORY=" + i.RepositoryNameWithOwner,
		"SYMPHONY_REPOSITORY_SSH_URL=" + i.RepositorySSHURL,
		"SYMPHONY_REPOSITORY_HTML_URL=" + i.RepositoryHTMLURL,
	}
}
