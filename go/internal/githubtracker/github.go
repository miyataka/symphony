package githubtracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/miyataka/symphony/go/internal/tracker"
	"github.com/miyataka/symphony/go/internal/workflow"
)

type Client struct {
	endpoint string
	token    string
	cfg      workflow.TrackerConfig
	http     *http.Client
}

func New(cfg workflow.TrackerConfig) (*Client, error) {
	if cfg.Token == "" {
		return nil, errors.New("github token is required")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://api.github.com/graphql"
	}
	if cfg.StatusField == "" {
		cfg.StatusField = "Status"
	}
	if cfg.OwnerType == "" {
		cfg.OwnerType = "user"
	}
	cfg.AllowedRepositories = normalizeRepositoryList(cfg.AllowedRepositories)
	return &Client{
		endpoint: cfg.Endpoint,
		token:    cfg.Token,
		cfg:      cfg,
		http:     &http.Client{Timeout: 30 * time.Second},
	}, nil
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
	return c.FetchIssuesByStates(ctx, c.cfg.ActiveStates)
}

func (c *Client) FetchIssueStatesByIDs(ctx context.Context, ids []string) ([]tracker.Issue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	allStates := append([]string{}, c.cfg.ActiveStates...)
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

func (c *Client) FetchIssuesByStates(ctx context.Context, states []string) ([]tracker.Issue, error) {
	stateSet := normalizedSet(states)
	var out []tracker.Issue
	var cursor *string

	for {
		page, err := c.fetchProjectItems(ctx, cursor)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			issue, ok := c.normalizeItem(item)
			if !ok {
				continue
			}
			if _, ok := stateSet[normalize(issue.State)]; !ok {
				continue
			}
			if c.cfg.Assignee != "" && !item.Content.HasAssignee(c.cfg.Assignee) {
				continue
			}
			if !c.repositoryAllowed(issue.RepositoryNameWithOwner) {
				continue
			}
			out = append(out, issue)
		}
		if !page.HasNextPage {
			break
		}
		cursor = page.EndCursor
	}

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

func (c *Client) normalizeItem(item projectItem) (tracker.Issue, bool) {
	if item.Content.Typename != "Issue" || item.Content.ID == "" {
		return tracker.Issue{}, false
	}

	fields := item.FieldValues.ByName()
	state := fields[c.cfg.StatusField].String()
	if state == "" {
		return tracker.Issue{}, false
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
		Identifier:              item.Content.Identifier(),
		Title:                   item.Content.Title,
		Description:             item.Content.Body,
		Priority:                priority,
		State:                   state,
		BranchName:              item.Content.BranchName(),
		URL:                     item.Content.URL,
		RepositoryNameWithOwner: item.Content.Repository.NameWithOwner,
		RepositorySSHURL:        item.Content.Repository.CloneSSHURL(),
		RepositoryHTMLURL:       item.Content.Repository.HTMLURL,
		Labels:                  labels,
		BlockedBy:               nil,
		CreatedAt:               createdAt,
		UpdatedAt:               updatedAt,
	}, true
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
}

func (i issueContent) Identifier() string {
	if i.Repository.NameWithOwner == "" || i.Number == 0 {
		return i.ID
	}
	return i.Repository.NameWithOwner + "#" + strconv.Itoa(i.Number)
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
