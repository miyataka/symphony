package tracker

import (
	"context"
	"time"
)

type Issue struct {
	ID                      string
	ProjectItemID           string
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
	PullRequests            []PullRequest
	CreatedAt               *time.Time
	UpdatedAt               *time.Time
}

type Blocker struct {
	ID         string
	Identifier string
	State      string
}

type PullRequest struct {
	ID                     string
	Number                 int
	Title                  string
	URL                    string
	State                  string
	IsDraft                bool
	ReviewDecision         string
	MergeStateStatus       string
	StatusCheckRollupState string
	CommentCount           int
	ReviewThreadCount      int
	UnresolvedThreadCount  int
}

func (p PullRequest) HasActionableFeedback() bool {
	return p.ReviewDecision == "CHANGES_REQUESTED" || p.UnresolvedThreadCount > 0
}

func (p PullRequest) ChecksPassing() bool {
	return p.StatusCheckRollupState == "" || p.StatusCheckRollupState == "SUCCESS"
}

func (p PullRequest) IsMerged() bool {
	return p.State == "MERGED"
}

func (p PullRequest) ReadyForMerge() bool {
	return p.State == "OPEN" &&
		!p.IsDraft &&
		p.ReviewDecision == "APPROVED" &&
		p.ChecksPassing() &&
		!p.HasActionableFeedback()
}

type Tracker interface {
	FetchCandidateIssues(context.Context) ([]Issue, error)
	FetchIssuesByStates(context.Context, []string) ([]Issue, error)
	FetchIssueStatesByIDs(context.Context, []string) ([]Issue, error)
}

type Writeback interface {
	UpdateIssueState(context.Context, Issue, string) error
	UpsertWorkpad(context.Context, Issue, string) error
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
