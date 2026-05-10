package githubtracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/miyataka/symphony/go/internal/tracker"
	"github.com/miyataka/symphony/go/internal/workflow"
)

type Client struct {
	endpoint     string
	restEndpoint string
	token        string
	cfg          workflow.TrackerConfig
	http         *http.Client
	logger       *slog.Logger
}

func New(cfg workflow.TrackerConfig) (*Client, error) {
	return NewWithLogger(cfg, nil)
}

func NewWithLogger(cfg workflow.TrackerConfig, logger *slog.Logger) (*Client, error) {
	if cfg.Token == "" {
		return nil, errors.New("github token is required")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.github.com/graphql"
	}
	if cfg.RestEndpoint == "" {
		cfg.RestEndpoint = deriveRestEndpoint(cfg.Endpoint)
	}
	if cfg.StatusField == "" {
		cfg.StatusField = "Status"
	}
	if cfg.OwnerType == "" {
		cfg.OwnerType = "user"
	}
	cfg.AllowedRepositories = normalizeRepositoryList(cfg.AllowedRepositories)
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		endpoint:     cfg.Endpoint,
		restEndpoint: strings.TrimRight(cfg.RestEndpoint, "/"),
		token:        cfg.Token,
		cfg:          cfg,
		http:         &http.Client{Timeout: 30 * time.Second},
		logger:       logger,
	}, nil
}

func deriveRestEndpoint(graphQLEndpoint string) string {
	endpoint := strings.TrimRight(strings.TrimSpace(graphQLEndpoint), "/")
	if endpoint == "" || endpoint == "https://api.github.com/graphql" {
		return "https://api.github.com"
	}
	return strings.TrimSuffix(endpoint, "/graphql")
}

func normalizeRepositoryList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		normalized := normalize(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func (c *Client) FetchCandidateIssues(ctx context.Context) ([]tracker.Issue, error) {
	return c.FetchIssuesByStates(ctx, issueFetchStates(c.cfg))
}

func (c *Client) FetchIssueStatesByIDs(ctx context.Context, ids []string) ([]tracker.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	allStates := issueFetchStates(c.cfg)
	allStates = append(allStates, c.cfg.TerminalStates...)
	issues, err := c.FetchIssuesByStates(ctx, allStates)
	if err != nil {
		return nil, err
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	filtered := issues[:0]
	for _, issue := range issues {
		if _, ok := want[issue.ID]; ok {
			filtered = append(filtered, issue)
		}
	}
	return filtered, nil
}

func issueFetchStates(cfg workflow.TrackerConfig) []string {
	states := append([]string{}, cfg.ActiveStates...)
	states = append(states, cfg.MonitorStates...)
	return uniqueStates(states)
}

func uniqueStates(states []string) []string {
	out := make([]string, 0, len(states))
	seen := map[string]struct{}{}
	for _, state := range states {
		normalized := normalize(state)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, state)
	}
	return out
}

func (c *Client) FetchIssuesByStates(ctx context.Context, states []string) ([]tracker.Issue, error) {
	stateSet := normalizedSet(states)
	var out []tracker.Issue
	var cursor *string
	summary := newFetchSummary()

	for {
		page, err := c.fetchProjectItems(ctx, cursor)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			summary.ProjectItems++
			issue, reason, ok := c.normalizeItem(item)
			if !ok {
				summary.Skip(reason, projectItemLabel(item))
				continue
			}
			summary.Issues++
			if _, ok := stateSet[normalize(issue.State)]; !ok {
				summary.Skip("state", fmt.Sprintf("%s status=%q", issue.Identifier, issue.State))
				continue
			}
			if c.cfg.Assignee != "" && !item.Content.HasAssignee(c.cfg.Assignee) {
				summary.Skip("assignee", issue.Identifier)
				continue
			}
			if !c.repositoryAllowed(issue.RepositoryNameWithOwner) {
				summary.Skip("repository", issue.Identifier)
				continue
			}
			if c.cfg.ReadIssueDependencies {
				blockers, err := c.fetchOpenIssueBlockers(ctx, item.Content)
				if err != nil {
					return nil, err
				}
				issue.BlockedBy = blockers
			}
			out = append(out, issue)
		}
		if !page.HasNextPage {
			break
		}
		cursor = page.EndCursor
	}

	c.logFetchSummary(states, summary, len(out))

	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i], out[j]
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
	return out, nil
}

func (c *Client) fetchOpenIssueBlockers(ctx context.Context, issue issueContent) ([]tracker.Blocker, error) {
	if issue.Repository.NameWithOwner == "" || issue.Number == 0 {
		return nil, nil
	}
	owner, repo, ok := strings.Cut(issue.Repository.NameWithOwner, "/")
	if !ok || owner == "" || repo == "" {
		return nil, nil
	}

	var out []tracker.Blocker
	for page := 1; ; page++ {
		var dependencies []issueDependency
		path := fmt.Sprintf(
			"%s/repos/%s/%s/issues/%d/dependencies/blocked_by?per_page=100&page=%d",
			c.restEndpoint,
			url.PathEscape(owner),
			url.PathEscape(repo),
			issue.Number,
			page,
		)
		if err := c.restJSON(ctx, path, &dependencies); err != nil {
			return nil, err
		}
		for _, dependency := range dependencies {
			if strings.EqualFold(dependency.State, "closed") {
				continue
			}
			out = append(out, tracker.Blocker{
				ID:         dependency.NodeID,
				Identifier: dependency.Identifier(),
				State:      dependency.State,
			})
		}
		if len(dependencies) < 100 {
			break
		}
	}
	return out, nil
}

func (c *Client) restJSON(ctx context.Context, url string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github rest status %d: %s", resp.StatusCode, string(payload))
	}
	return json.Unmarshal(payload, dest)
}

type projectPage struct {
	Items       []projectItem
	HasNextPage bool
	EndCursor   *string
}

func (c *Client) fetchProjectItems(ctx context.Context, cursor *string) (projectPage, error) {
	query := organizationProjectQuery
	if c.cfg.OwnerType == "user" {
		query = userProjectQuery
	}

	variables := map[string]any{
		"login":  c.cfg.Owner,
		"number": c.cfg.ProjectNumber,
		"after":  cursor,
	}

	var response graphQLResponse
	if err := c.graphql(ctx, query, variables, &response); err != nil {
		return projectPage{}, err
	}
	if len(response.Errors) > 0 {
		return projectPage{}, fmt.Errorf("github graphql: %s", response.Errors[0].Message)
	}

	project := response.Data.Organization.Project
	if c.cfg.OwnerType == "user" {
		project = response.Data.User.Project
	}
	if project.Items.PageInfo.HasNextPage {
		return projectPage{
			Items:       project.Items.Nodes,
			HasNextPage: true,
			EndCursor:   project.Items.PageInfo.EndCursor,
		}, nil
	}
	return projectPage{Items: project.Items.Nodes}, nil
}

type fetchSummary struct {
	ProjectItems   int
	Issues         int
	Skipped        map[string]int
	SkippedExample map[string][]string
}

func newFetchSummary() fetchSummary {
	return fetchSummary{
		Skipped:        map[string]int{},
		SkippedExample: map[string][]string{},
	}
}

func (s fetchSummary) Skip(reason, example string) {
	if reason == "" {
		reason = "unknown"
	}
	s.Skipped[reason]++
	example = strings.TrimSpace(example)
	if example == "" || len(s.SkippedExample[reason]) >= 5 {
		return
	}
	s.SkippedExample[reason] = append(s.SkippedExample[reason], example)
}

func (c *Client) logFetchSummary(states []string, summary fetchSummary, matched int) {
	if c.logger == nil {
		return
	}
	c.logger.Debug(
		"github project scan completed",
		"owner", c.cfg.Owner,
		"owner_type", c.cfg.OwnerType,
		"project_number", c.cfg.ProjectNumber,
		"status_field", c.cfg.StatusField,
		"states", states,
		"allowed_repositories", c.cfg.AllowedRepositories,
		"project_items", summary.ProjectItems,
		"issue_items", summary.Issues,
		"matched_issues", matched,
		"skipped_non_issue", summary.Skipped["non_issue"],
		"skipped_missing_status", summary.Skipped["missing_status"],
		"skipped_state", summary.Skipped["state"],
		"skipped_assignee", summary.Skipped["assignee"],
		"skipped_repository", summary.Skipped["repository"],
		"examples_non_issue", strings.Join(summary.SkippedExample["non_issue"], ", "),
		"examples_missing_status", strings.Join(summary.SkippedExample["missing_status"], ", "),
		"examples_state", strings.Join(summary.SkippedExample["state"], ", "),
		"examples_assignee", strings.Join(summary.SkippedExample["assignee"], ", "),
		"examples_repository", strings.Join(summary.SkippedExample["repository"], ", "),
	)
}

func projectItemLabel(item projectItem) string {
	if item.Content.ID != "" || item.Content.Repository.NameWithOwner != "" || item.Content.Number != 0 {
		return item.Content.Identifier()
	}
	if item.Content.Typename != "" {
		return item.Content.Typename + ":" + item.ID
	}
	return item.ID
}

func (c *Client) normalizeItem(item projectItem) (tracker.Issue, string, bool) {
	if item.Content.Typename != "Issue" || item.Content.ID == "" {
		return tracker.Issue{}, "non_issue", false
	}

	fields := item.FieldValues.ByName()
	state := fields[c.cfg.StatusField].String()
	if state == "" {
		return tracker.Issue{}, "missing_status", false
	}

	var priority *int
	if c.cfg.PriorityField != "" {
		priority = fields[c.cfg.PriorityField].Priority()
	}

	createdAt := parseTimePtr(item.Content.CreatedAt)
	updatedAt := parseTimePtr(item.Content.UpdatedAt)
	labels := make([]string, 0, len(item.Content.Labels.Nodes))
	for _, label := range item.Content.Labels.Nodes {
		if label.Name != "" {
			labels = append(labels, strings.ToLower(label.Name))
		}
	}

	return tracker.Issue{
		ID:                      item.Content.ID,
		ProjectItemID:           item.ID,
		Identifier:              item.Content.Identifier(),
		Title:                   item.Content.Title,
		Description:             item.Content.Body,
		Comments:                item.Content.IssueComments(c.cfg.WorkpadMarker),
		PRReviewComments:        item.Content.PRReviewComments(c.cfg.WorkpadMarker),
		Priority:                priority,
		State:                   state,
		BranchName:              item.Content.BranchName(),
		URL:                     item.Content.URL,
		RepositoryNameWithOwner: item.Content.Repository.NameWithOwner,
		RepositorySSHURL:        item.Content.Repository.CloneSSHURL(),
		RepositoryHTMLURL:       item.Content.Repository.HTMLURL,
		Labels:                  labels,
		BlockedBy:               nil,
		PullRequests:            item.Content.PullRequests(),
		CreatedAt:               createdAt,
		UpdatedAt:               updatedAt,
	}, "", true
}

func (c *Client) UpdateIssueState(ctx context.Context, issue tracker.Issue, stateName string) error {
	if issue.ProjectItemID == "" {
		return errors.New("issue project item id is required")
	}
	metadata, err := c.fetchProjectMetadata(ctx)
	if err != nil {
		return err
	}
	optionID := metadata.statusOptionID(stateName)
	if optionID == "" {
		return fmt.Errorf("status option %q not found on field %q", stateName, c.cfg.StatusField)
	}

	var response graphQLMutationResponse
	if err := c.graphql(ctx, updateProjectItemFieldValueMutation, map[string]any{
		"projectId": metadata.ProjectID,
		"itemId":    issue.ProjectItemID,
		"fieldId":   metadata.StatusFieldID,
		"optionId":  optionID,
	}, &response); err != nil {
		return err
	}
	return response.err()
}

func (c *Client) UpsertWorkpad(ctx context.Context, issue tracker.Issue, body string) error {
	if issue.ID == "" {
		return errors.New("issue id is required")
	}
	marker := c.cfg.WorkpadMarker
	if marker == "" {
		marker = "## Codex Workpad"
	}
	if !strings.Contains(body, marker) {
		body = marker + "\n\n" + strings.TrimSpace(body)
	}

	commentID, err := c.findWorkpadCommentID(ctx, issue.ID, marker)
	if err != nil {
		return err
	}

	var response graphQLMutationResponse
	if commentID == "" {
		if err := c.graphql(ctx, addCommentMutation, map[string]any{
			"subjectId": issue.ID,
			"body":      body,
		}, &response); err != nil {
			return err
		}
		return response.err()
	}

	if err := c.graphql(ctx, updateIssueCommentMutation, map[string]any{
		"id":   commentID,
		"body": body,
	}, &response); err != nil {
		return err
	}
	return response.err()
}

func (c *Client) MergePullRequest(ctx context.Context, issue tracker.Issue, pr tracker.PullRequest, opts tracker.MergeOptions) error {
	if pr.ID == "" {
		return errors.New("pull request id is required")
	}
	method := strings.ToUpper(strings.TrimSpace(opts.Method))
	if method == "" {
		method = "SQUASH"
	}
	switch method {
	case "MERGE", "SQUASH", "REBASE":
	default:
		return fmt.Errorf("unsupported pull request merge method %q", method)
	}
	headline := strings.TrimSpace(opts.CommitHeadline)
	if headline == "" {
		headline = fmt.Sprintf("%s: %s", issue.Identifier, issue.Title)
	}
	var response graphQLMutationResponse
	if err := c.graphql(ctx, mergePullRequestMutation, map[string]any{
		"pullRequestId": pr.ID,
		"mergeMethod":   method,
		"headline":      headline,
	}, &response); err != nil {
		return err
	}
	return response.err()
}

func (c *Client) findWorkpadCommentID(ctx context.Context, issueID, marker string) (string, error) {
	var response issueCommentsResponse
	if err := c.graphql(ctx, issueCommentsQuery, map[string]any{"id": issueID}, &response); err != nil {
		return "", err
	}
	if len(response.Errors) > 0 {
		return "", fmt.Errorf("github graphql: %s", response.Errors[0].Message)
	}
	for _, comment := range response.Data.Node.Comments.Nodes {
		if strings.Contains(comment.Body, marker) {
			return comment.ID, nil
		}
	}
	return "", nil
}

type projectMetadata struct {
	ProjectID     string
	StatusFieldID string
	StatusOptions map[string]string
}

func (m projectMetadata) statusOptionID(name string) string {
	return m.StatusOptions[normalize(name)]
}

func (c *Client) fetchProjectMetadata(ctx context.Context) (projectMetadata, error) {
	query := organizationProjectMetadataQuery
	if c.cfg.OwnerType == "user" {
		query = userProjectMetadataQuery
	}
	var response projectMetadataResponse
	if err := c.graphql(ctx, query, map[string]any{
		"login":  c.cfg.Owner,
		"number": c.cfg.ProjectNumber,
	}, &response); err != nil {
		return projectMetadata{}, err
	}
	if len(response.Errors) > 0 {
		return projectMetadata{}, fmt.Errorf("github graphql: %s", response.Errors[0].Message)
	}
	project := response.Data.Organization.Project
	if c.cfg.OwnerType == "user" {
		project = response.Data.User.Project
	}
	if project.ID == "" {
		return projectMetadata{}, errors.New("project not found")
	}
	for _, field := range project.Fields.Nodes {
		if field.Typename != "ProjectV2SingleSelectField" || field.Name != c.cfg.StatusField {
			continue
		}
		options := make(map[string]string, len(field.Options))
		for _, option := range field.Options {
			options[normalize(option.Name)] = option.ID
		}
		return projectMetadata{
			ProjectID:     project.ID,
			StatusFieldID: field.ID,
			StatusOptions: options,
		}, nil
	}
	return projectMetadata{}, fmt.Errorf("status field %q not found", c.cfg.StatusField)
}

func (c *Client) repositoryAllowed(nameWithOwner string) bool {
	if len(c.cfg.AllowedRepositories) == 0 {
		return true
	}
	normalized := normalize(nameWithOwner)
	for _, allowed := range c.cfg.AllowedRepositories {
		if allowed == normalized {
			return true
		}
	}
	return false
}

func (c *Client) graphql(ctx context.Context, query string, variables map[string]any, dest any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github graphql status %d: %s", resp.StatusCode, string(payload))
	}
	return json.Unmarshal(payload, dest)
}

type graphQLResponse struct {
	Data struct {
		Organization struct {
			Project project `json:"projectV2"`
		} `json:"organization"`
		User struct {
			Project project `json:"projectV2"`
		} `json:"user"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type graphQLMutationResponse struct {
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (r graphQLMutationResponse) err() error {
	if len(r.Errors) == 0 {
		return nil
	}
	return fmt.Errorf("github graphql: %s", r.Errors[0].Message)
}

type issueCommentsResponse struct {
	Data struct {
		Node struct {
			Comments struct {
				Nodes []struct {
					ID   string `json:"id"`
					Body string `json:"body"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"node"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type projectMetadataResponse struct {
	Data struct {
		Organization struct {
			Project projectMetadataProject `json:"projectV2"`
		} `json:"organization"`
		User struct {
			Project projectMetadataProject `json:"projectV2"`
		} `json:"user"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type projectMetadataProject struct {
	ID     string `json:"id"`
	Fields struct {
		Nodes []struct {
			Typename string `json:"__typename"`
			ID       string `json:"id"`
			Name     string `json:"name"`
			Options  []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"options"`
		} `json:"nodes"`
	} `json:"fields"`
}

type project struct {
	Items struct {
		Nodes    []projectItem `json:"nodes"`
		PageInfo struct {
			HasNextPage bool    `json:"hasNextPage"`
			EndCursor   *string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"items"`
}

type projectItem struct {
	ID          string           `json:"id"`
	Content     issueContent     `json:"content"`
	FieldValues fieldValueBucket `json:"fieldValues"`
}

type issueContent struct {
	Typename   string            `json:"__typename"`
	ID         string            `json:"id"`
	Number     int               `json:"number"`
	Title      string            `json:"title"`
	Body       string            `json:"body"`
	URL        string            `json:"url"`
	State      string            `json:"state"`
	CreatedAt  string            `json:"createdAt"`
	UpdatedAt  string            `json:"updatedAt"`
	Repository repositoryContent `json:"repository"`
	Labels     struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	Comments struct {
		Nodes []issueCommentContent `json:"nodes"`
	} `json:"comments"`
	ClosedByPullRequestsReferences struct {
		Nodes []pullRequestContent `json:"nodes"`
	} `json:"closedByPullRequestsReferences"`
}

type issueCommentContent struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	URL       string `json:"url"`
	CreatedAt string `json:"createdAt"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
}

type pullRequestContent struct {
	ID                string `json:"id"`
	Number            int    `json:"number"`
	Title             string `json:"title"`
	URL               string `json:"url"`
	State             string `json:"state"`
	IsDraft           bool   `json:"isDraft"`
	ReviewDecision    string `json:"reviewDecision"`
	MergeStateStatus  string `json:"mergeStateStatus"`
	StatusCheckRollup *struct {
		State    string `json:"state"`
		Contexts struct {
			Nodes []statusCheckContent `json:"nodes"`
		} `json:"contexts"`
	} `json:"statusCheckRollup"`
	Comments struct {
		TotalCount int `json:"totalCount"`
	} `json:"comments"`
	ReviewThreads struct {
		Nodes []struct {
			IsResolved bool   `json:"isResolved"`
			Path       string `json:"path"`
			Line       int    `json:"line"`
			Comments   struct {
				Nodes []reviewThreadCommentContent `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
		TotalCount int `json:"totalCount"`
	} `json:"reviewThreads"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				CommittedDate string `json:"committedDate"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

type reviewThreadCommentContent struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	URL       string `json:"url"`
	CreatedAt string `json:"createdAt"`
	Author    struct {
		Typename string `json:"__typename"`
		Login    string `json:"login"`
	} `json:"author"`
}

type statusCheckContent struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
}

func (c statusCheckContent) Check() tracker.StatusCheck {
	name := c.Name
	if name == "" {
		name = c.Context
	}
	state := c.Conclusion
	if state == "" {
		state = c.State
	}
	if state == "" {
		state = c.Status
	}
	return tracker.StatusCheck{Name: name, State: state}
}

type issueDependency struct {
	NodeID  string `json:"node_id"`
	Number  int    `json:"number"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
}

func (d issueDependency) Identifier() string {
	if d.HTMLURL == "" || d.Number == 0 {
		return d.NodeID
	}
	parts := strings.Split(strings.TrimPrefix(d.HTMLURL, "https://github.com/"), "/")
	if len(parts) < 4 {
		return d.NodeID
	}
	return parts[0] + "/" + parts[1] + "#" + strconv.Itoa(d.Number)
}

func (i issueContent) Identifier() string {
	if i.Repository.NameWithOwner == "" || i.Number == 0 {
		return i.ID
	}
	return i.Repository.NameWithOwner + "#" + strconv.Itoa(i.Number)
}

func (i issueContent) PullRequests() []tracker.PullRequest {
	out := make([]tracker.PullRequest, 0, len(i.ClosedByPullRequestsReferences.Nodes))
	for _, pr := range i.ClosedByPullRequestsReferences.Nodes {
		unresolvedThreads := 0
		for _, thread := range pr.ReviewThreads.Nodes {
			if !thread.IsResolved {
				unresolvedThreads++
			}
		}
		checkState := ""
		var checks []tracker.StatusCheck
		if pr.StatusCheckRollup != nil {
			checkState = pr.StatusCheckRollup.State
			for _, context := range pr.StatusCheckRollup.Contexts.Nodes {
				check := context.Check()
				if check.Name != "" {
					checks = append(checks, check)
				}
			}
		}
		out = append(out, tracker.PullRequest{
			ID:                     pr.ID,
			Number:                 pr.Number,
			Title:                  pr.Title,
			URL:                    pr.URL,
			State:                  pr.State,
			IsDraft:                pr.IsDraft,
			ReviewDecision:         pr.ReviewDecision,
			MergeStateStatus:       pr.MergeStateStatus,
			StatusCheckRollupState: checkState,
			Checks:                 checks,
			CommentCount:           pr.Comments.TotalCount,
			ReviewThreadCount:      pr.ReviewThreads.TotalCount,
			UnresolvedThreadCount:  unresolvedThreads,
		})
	}
	return out
}

func (i issueContent) PRReviewComments(workpadMarker string) []tracker.PRReviewComment {
	markers := workpadMarkers(workpadMarker)
	var out []tracker.PRReviewComment
	for _, pr := range i.ClosedByPullRequestsReferences.Nodes {
		var latestCommit *time.Time
		for _, node := range pr.Commits.Nodes {
			if t := parseTimePtr(node.Commit.CommittedDate); t != nil {
				latestCommit = t
			}
		}
		for _, thread := range pr.ReviewThreads.Nodes {
			if thread.IsResolved {
				continue
			}
			for _, comment := range thread.Comments.Nodes {
				body := strings.TrimSpace(comment.Body)
				if body == "" || containsAnyMarker(body, markers) {
					continue
				}
				createdAt := parseTimePtr(comment.CreatedAt)
				isBot := comment.Author.Typename == "Bot"
				if isBot && latestCommit != nil && createdAt != nil && !createdAt.After(*latestCommit) {
					continue
				}
				out = append(out, tracker.PRReviewComment{
					ID:          comment.ID,
					PRNumber:    pr.Number,
					PRURL:       pr.URL,
					Path:        thread.Path,
					Line:        thread.Line,
					Author:      comment.Author.Login,
					AuthorIsBot: isBot,
					Body:        body,
					URL:         comment.URL,
					CreatedAt:   createdAt,
				})
			}
		}
	}
	return out
}

func (i issueContent) IssueComments(workpadMarker string) []tracker.IssueComment {
	markers := workpadMarkers(workpadMarker)
	out := make([]tracker.IssueComment, 0, len(i.Comments.Nodes))
	for _, comment := range i.Comments.Nodes {
		body := strings.TrimSpace(comment.Body)
		if body == "" || containsAnyMarker(body, markers) {
			continue
		}
		out = append(out, tracker.IssueComment{
			ID:        comment.ID,
			Author:    comment.Author.Login,
			Body:      body,
			URL:       comment.URL,
			CreatedAt: parseTimePtr(comment.CreatedAt),
		})
	}
	return out
}

func workpadMarkers(configured string) []string {
	candidates := []string{
		strings.TrimSpace(configured),
		"## Codex Workpad",
		"## Claude Workpad",
	}
	out := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, marker := range candidates {
		if marker == "" {
			continue
		}
		normalized := normalize(marker)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, marker)
	}
	return out
}

func containsAnyMarker(body string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

func (i issueContent) BranchName() string {
	if i.Repository.NameWithOwner == "" || i.Number == 0 {
		return ""
	}
	repo := strings.TrimPrefix(i.Repository.NameWithOwner, strings.Split(i.Repository.NameWithOwner, "/")[0]+"/")
	return fmt.Sprintf("issue-%d-%s", i.Number, sanitizeBranchPart(repo))
}

func (i issueContent) HasAssignee(login string) bool {
	for _, assignee := range i.Assignees.Nodes {
		if strings.EqualFold(assignee.Login, login) {
			return true
		}
	}
	return false
}

type repositoryContent struct {
	NameWithOwner string `json:"nameWithOwner"`
	SSHURL        string `json:"sshUrl"`
	HTMLURL       string `json:"url"`
}

func (r repositoryContent) CloneSSHURL() string {
	if r.SSHURL != "" {
		return r.SSHURL
	}
	if r.NameWithOwner == "" {
		return ""
	}
	return "git@github.com:" + r.NameWithOwner + ".git"
}

type fieldValueBucket struct {
	Nodes []fieldValue `json:"nodes"`
}

func (b fieldValueBucket) ByName() map[string]fieldValue {
	out := map[string]fieldValue{}
	for _, value := range b.Nodes {
		if value.Field.Name != "" {
			out[value.Field.Name] = value
		}
	}
	return out
}

type fieldValue struct {
	Typename string  `json:"__typename"`
	Name     string  `json:"name"`
	Title    string  `json:"title"`
	Text     string  `json:"text"`
	Number   float64 `json:"number"`
	Field    struct {
		Name string `json:"name"`
	} `json:"field"`
}

func (v fieldValue) String() string {
	switch v.Typename {
	case "ProjectV2ItemFieldSingleSelectValue", "ProjectV2ItemFieldIterationValue":
		if v.Name != "" {
			return v.Name
		}
		return v.Title
	case "ProjectV2ItemFieldTextValue":
		return v.Text
	case "ProjectV2ItemFieldNumberValue":
		if v.Number == float64(int(v.Number)) {
			return strconv.Itoa(int(v.Number))
		}
		return strconv.FormatFloat(v.Number, 'f', -1, 64)
	default:
		if v.Name != "" {
			return v.Name
		}
		return v.Text
	}
}

func (v fieldValue) Priority() *int {
	raw := strings.TrimSpace(v.String())
	if raw == "" {
		return nil
	}
	if n, err := strconv.Atoi(raw); err == nil {
		return &n
	}
	lower := strings.ToLower(raw)
	for idx, name := range []string{"urgent", "high", "medium", "low"} {
		if strings.Contains(lower, name) {
			n := idx + 1
			return &n
		}
	}
	return nil
}

const projectItemFields = `
id
content {
  __typename
  ... on Issue {
    id
    number
    title
    body
    url
    state
    createdAt
    updatedAt
    repository { nameWithOwner sshUrl url }
    labels(first: 25) { nodes { name } }
    assignees(first: 25) { nodes { login } }
    comments(first: 50) {
      nodes {
        id
        body
        url
        createdAt
        author { login }
      }
    }
    closedByPullRequestsReferences(first: 10) {
      nodes {
        id
        number
        title
        url
        state
        isDraft
        reviewDecision
        mergeStateStatus
        statusCheckRollup {
          state
          contexts(first: 50) {
            nodes {
              __typename
              ... on CheckRun { name status conclusion }
              ... on StatusContext { context state }
            }
          }
        }
        comments(first: 1) { totalCount }
        reviewThreads(first: 100) {
          totalCount
          nodes {
            isResolved
            path
            line
            comments(first: 20) {
              nodes {
                id
                body
                url
                createdAt
                author { __typename login }
              }
            }
          }
        }
        commits(last: 1) {
          nodes { commit { committedDate } }
        }
      }
    }
  }
}
fieldValues(first: 50) {
  nodes {
    __typename
    ... on ProjectV2ItemFieldSingleSelectValue { name field { ... on ProjectV2FieldCommon { name } } }
    ... on ProjectV2ItemFieldTextValue { text field { ... on ProjectV2FieldCommon { name } } }
    ... on ProjectV2ItemFieldNumberValue { number field { ... on ProjectV2FieldCommon { name } } }
    ... on ProjectV2ItemFieldIterationValue { title field { ... on ProjectV2FieldCommon { name } } }
  }
}`

var organizationProjectQuery = fmt.Sprintf(`
query SymphonyGitHubProject($login: String!, $number: Int!, $after: String) {
  organization(login: $login) {
    projectV2(number: $number) {
      items(first: 50, after: $after) {
        nodes { %s }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}`, projectItemFields)

var userProjectQuery = fmt.Sprintf(`
query SymphonyGitHubUserProject($login: String!, $number: Int!, $after: String) {
  user(login: $login) {
    projectV2(number: $number) {
      items(first: 50, after: $after) {
        nodes { %s }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}`, projectItemFields)

const projectMetadataFields = `
id
fields(first: 50) {
  nodes {
    __typename
    ... on ProjectV2SingleSelectField {
      id
      name
      options { id name }
    }
  }
}`

var organizationProjectMetadataQuery = fmt.Sprintf(`
query SymphonyGitHubProjectMetadata($login: String!, $number: Int!) {
  organization(login: $login) {
    projectV2(number: $number) { %s }
  }
}`, projectMetadataFields)

var userProjectMetadataQuery = fmt.Sprintf(`
query SymphonyGitHubUserProjectMetadata($login: String!, $number: Int!) {
  user(login: $login) {
    projectV2(number: $number) { %s }
  }
}`, projectMetadataFields)

const updateProjectItemFieldValueMutation = `
mutation SymphonyUpdateProjectStatus($projectId: ID!, $itemId: ID!, $fieldId: ID!, $optionId: String!) {
  updateProjectV2ItemFieldValue(input: {
    projectId: $projectId,
    itemId: $itemId,
    fieldId: $fieldId,
    value: { singleSelectOptionId: $optionId }
  }) {
    projectV2Item { id }
  }
}`

const issueCommentsQuery = `
query SymphonyIssueComments($id: ID!) {
  node(id: $id) {
    ... on Issue {
      comments(first: 100) {
        nodes { id body }
      }
    }
  }
}`

const addCommentMutation = `
mutation SymphonyAddWorkpad($subjectId: ID!, $body: String!) {
  addComment(input: { subjectId: $subjectId, body: $body }) {
    commentEdge { node { id } }
  }
}`

const updateIssueCommentMutation = `
mutation SymphonyUpdateWorkpad($id: ID!, $body: String!) {
  updateIssueComment(input: { id: $id, body: $body }) {
    issueComment { id }
  }
}`

const mergePullRequestMutation = `
mutation SymphonyMergePullRequest($pullRequestId: ID!, $mergeMethod: PullRequestMergeMethod!, $headline: String!) {
  mergePullRequest(input: {
    pullRequestId: $pullRequestId,
    mergeMethod: $mergeMethod,
    commitHeadline: $headline
  }) {
    pullRequest { id merged }
  }
}`

func normalizedSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if normalized := normalize(value); normalized != "" {
			out[normalized] = struct{}{}
		}
	}
	return out
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func parseTimePtr(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}

func sanitizeBranchPart(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			continue
		}
		builder.WriteRune('-')
	}
	return strings.Trim(builder.String(), "-")
}
