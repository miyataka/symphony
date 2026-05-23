package tracker

import (
	"context"
	"strings"
	"time"
)

type Issue struct {
	ID                      string
	ProjectItemID           string
	Identifier              string
	Title                   string
	Description             string
	Comments                []IssueComment
	PRReviewComments        []PRReviewComment
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

type IssueComment struct {
	ID                string
	Author            string
	AuthorAssociation string
	Body              string
	URL               string
	CreatedAt         *time.Time
}

type PRReviewComment struct {
	ID                string
	PRNumber          int
	PRURL             string
	Path              string
	Line              int
	Author            string
	AuthorAssociation string
	AuthorIsBot       bool
	Body              string
	URL               string
	CreatedAt         *time.Time
}

func TrustedCommentAuthorAssociation(authorAssociation string) bool {
	switch strings.ToUpper(strings.TrimSpace(authorAssociation)) {
	case "OWNER", "MEMBER":
		return true
	default:
		return false
	}
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
	Checks                 []StatusCheck
	CommentCount           int
	ReviewThreadCount      int
	UnresolvedThreadCount  int
}

type StatusCheck struct {
	Name  string
	State string
}

func (c StatusCheck) Failing() bool {
	return c.State == "FAILURE" || c.State == "ERROR"
}

type MergeOptions struct {
	Method         string
	CommitHeadline string
}

type IssueCreation struct {
	RepositoryNameWithOwner string
	Title                   string
	Body                    string
	ProjectState            string
}

func (p PullRequest) HasActionableFeedback() bool {
	return p.ReviewDecision == "CHANGES_REQUESTED" || p.UnresolvedThreadCount > 0
}

func (p PullRequest) ChecksPassing() bool {
	return p.StatusCheckRollupState == "" || p.StatusCheckRollupState == "SUCCESS"
}

func (p PullRequest) ChecksFailing() bool {
	return p.StatusCheckRollupState == "FAILURE" || p.StatusCheckRollupState == "ERROR"
}

func (p PullRequest) RequiredChecksPassing(required []string) bool {
	if len(required) == 0 {
		return p.ChecksPassing()
	}
	checks := map[string]string{}
	for _, check := range p.Checks {
		checks[normalize(check.Name)] = check.State
	}
	for _, name := range required {
		state, ok := checks[normalize(name)]
		if !ok || state != "SUCCESS" {
			return false
		}
	}
	return true
}

func (p PullRequest) RequiredChecksFailing(required []string) bool {
	if len(required) == 0 {
		return p.ChecksFailing()
	}
	requiredSet := map[string]struct{}{}
	for _, name := range required {
		requiredSet[normalize(name)] = struct{}{}
	}
	for _, check := range p.Checks {
		if _, ok := requiredSet[normalize(check.Name)]; ok && check.Failing() {
			return true
		}
	}
	return false
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

type PullRequestMerger interface {
	MergePullRequest(context.Context, Issue, PullRequest, MergeOptions) error
}

type IssueCreator interface {
	CreateIssue(context.Context, IssueCreation) (Issue, error)
}

func (i Issue) Env() []string {
	return []string{
		"SYMPHONY_ISSUE_ID=" + i.ID,
		"SYMPHONY_ISSUE_IDENTIFIER=" + i.Identifier,
		"SYMPHONY_ISSUE_TITLE=" + i.Title,
		"SYMPHONY_ISSUE_URL=" + i.URL,
		"SYMPHONY_ISSUE_STATE=" + i.State,
		"SYMPHONY_BRANCH=" + i.BranchName,
		"SYMPHONY_REPOSITORY=" + i.RepositoryNameWithOwner,
		"SYMPHONY_REPOSITORY_SSH_URL=" + i.RepositorySSHURL,
		"SYMPHONY_REPOSITORY_HTML_URL=" + i.RepositoryHTMLURL,
	}
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
