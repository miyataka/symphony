package githubtracker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miyataka/symphony/go/internal/tracker"
	"github.com/miyataka/symphony/go/internal/workflow"
)

func TestFetchIssuesByStatesNormalizesProjectIssues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"user": {
					"projectV2": {
						"items": {
							"nodes": [{
								"id": "PVTI_1",
								"content": {
									"__typename": "Issue",
									"id": "I_1",
									"number": 42,
									"title": "Implement thing",
									"body": "Body text",
									"url": "https://github.com/miyataka/symphony/issues/42",
									"state": "OPEN",
									"createdAt": "2026-05-01T00:00:00Z",
									"updatedAt": "2026-05-01T00:01:00Z",
									"repository": {
										"nameWithOwner": "miyataka/symphony",
										"sshUrl": "git@github.com:miyataka/symphony.git",
										"url": "https://github.com/miyataka/symphony"
									},
									"labels": {"nodes": [{"name": "Symphony"}]},
									"assignees": {"nodes": [{"login": "taka"}]}
								},
								"fieldValues": {
									"nodes": [
										{"__typename": "ProjectV2ItemFieldSingleSelectValue", "name": "Todo", "field": {"name": "Status"}},
										{"__typename": "ProjectV2ItemFieldSingleSelectValue", "name": "High", "field": {"name": "Priority"}}
									]
								}
							}],
							"pageInfo": {"hasNextPage": false, "endCursor": null}
						}
					}
				}
			}
		}`))
	}))
	defer server.Close()

	client, err := New(workflow.TrackerConfig{
		Token:          "token",
		Endpoint:       server.URL,
		Owner:          "miyataka",
		OwnerType:      "user",
		ProjectNumber:  1,
		StatusField:    "Status",
		PriorityField:  "Priority",
		ActiveStates:   []string{"Todo"},
		TerminalStates: []string{"Done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	issue := issues[0]
	if issue.Identifier != "miyataka/symphony#42" {
		t.Fatalf("unexpected identifier: %q", issue.Identifier)
	}
	if issue.RepositoryNameWithOwner != "miyataka/symphony" {
		t.Fatalf("unexpected repository: %q", issue.RepositoryNameWithOwner)
	}
	if issue.RepositorySSHURL != "git@github.com:miyataka/symphony.git" {
		t.Fatalf("unexpected repository ssh url: %q", issue.RepositorySSHURL)
	}
	if issue.RepositoryHTMLURL != "https://github.com/miyataka/symphony" {
		t.Fatalf("unexpected repository html url: %q", issue.RepositoryHTMLURL)
	}
	if issue.State != "Todo" {
		t.Fatalf("unexpected state: %q", issue.State)
	}
	if issue.Priority == nil || *issue.Priority != 2 {
		t.Fatalf("unexpected priority: %#v", issue.Priority)
	}
	if len(issue.Labels) != 1 || issue.Labels[0] != "symphony" {
		t.Fatalf("unexpected labels: %#v", issue.Labels)
	}
}

func TestUpdateIssueState(t *testing.T) {
	var sawMutation bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(payload.Query, "SymphonyGitHubUserProjectMetadata"):
			_, _ = w.Write([]byte(`{
				"data": {"user": {"projectV2": {
					"id": "PVT_1",
					"fields": {"nodes": [{
						"__typename": "ProjectV2SingleSelectField",
						"id": "FIELD_STATUS",
						"name": "Status",
						"options": [{"id": "OPT_REVIEW", "name": "Human Review"}]
					}]}
				}}}
			}`))
		case strings.Contains(payload.Query, "SymphonyUpdateProjectStatus"):
			sawMutation = true
			if payload.Variables["projectId"] != "PVT_1" {
				t.Fatalf("unexpected project id: %#v", payload.Variables)
			}
			if payload.Variables["itemId"] != "ITEM_1" {
				t.Fatalf("unexpected item id: %#v", payload.Variables)
			}
			if payload.Variables["fieldId"] != "FIELD_STATUS" {
				t.Fatalf("unexpected field id: %#v", payload.Variables)
			}
			if payload.Variables["optionId"] != "OPT_REVIEW" {
				t.Fatalf("unexpected option id: %#v", payload.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"ITEM_1"}}}}`))
		default:
			t.Fatalf("unexpected query: %s", payload.Query)
		}
	}))
	defer server.Close()

	client, err := New(workflow.TrackerConfig{
		Token:         "token",
		Endpoint:      server.URL,
		Owner:         "miyataka",
		OwnerType:     "user",
		ProjectNumber: 1,
		StatusField:   "Status",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.UpdateIssueState(context.Background(), trackerIssue("I_1", "ITEM_1"), "Human Review")
	if err != nil {
		t.Fatal(err)
	}
	if !sawMutation {
		t.Fatal("expected update mutation")
	}
}

func TestUpsertWorkpadCreatesAndUpdatesComment(t *testing.T) {
	var added, updated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(payload.Query, "SymphonyIssueComments") && !added:
			_, _ = w.Write([]byte(`{"data":{"node":{"comments":{"nodes":[]}}}}`))
		case strings.Contains(payload.Query, "SymphonyAddWorkpad"):
			added = true
			if !strings.Contains(payload.Variables["body"].(string), "## Codex Workpad") {
				t.Fatalf("missing marker in body: %#v", payload.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"addComment":{"commentEdge":{"node":{"id":"COMMENT_1"}}}}}`))
		case strings.Contains(payload.Query, "SymphonyIssueComments"):
			_, _ = w.Write([]byte(`{"data":{"node":{"comments":{"nodes":[{"id":"COMMENT_1","body":"## Codex Workpad\nold"}]}}}}`))
		case strings.Contains(payload.Query, "SymphonyUpdateWorkpad"):
			updated = true
			if payload.Variables["id"] != "COMMENT_1" {
				t.Fatalf("unexpected comment id: %#v", payload.Variables)
			}
			_, _ = w.Write([]byte(`{"data":{"updateIssueComment":{"issueComment":{"id":"COMMENT_1"}}}}`))
		default:
			t.Fatalf("unexpected query: %s", payload.Query)
		}
	}))
	defer server.Close()

	client, err := New(workflow.TrackerConfig{
		Token:         "token",
		Endpoint:      server.URL,
		Owner:         "miyataka",
		OwnerType:     "user",
		ProjectNumber: 1,
		WorkpadMarker: "## Codex Workpad",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.UpsertWorkpad(context.Background(), trackerIssue("I_1", "ITEM_1"), "first body"); err != nil {
		t.Fatal(err)
	}
	if err := client.UpsertWorkpad(context.Background(), trackerIssue("I_1", "ITEM_1"), "second body"); err != nil {
		t.Fatal(err)
	}
	if !added || !updated {
		t.Fatalf("expected add and update, added=%t updated=%t", added, updated)
	}
}

func trackerIssue(id, itemID string) tracker.Issue {
	return tracker.Issue{
		ID:            id,
		ProjectItemID: itemID,
		Identifier:    "miyataka/symphony#1",
	}
}

func TestFetchIssuesByStatesFiltersAllowedRepositories(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"user": {
					"projectV2": {
						"items": {
							"nodes": [
								{
									"id": "PVTI_1",
									"content": {
										"__typename": "Issue",
										"id": "I_1",
										"number": 1,
										"title": "Allowed",
										"body": "",
										"url": "https://github.com/miyataka/api/issues/1",
										"state": "OPEN",
										"repository": {"nameWithOwner": "miyataka/api", "url": "https://github.com/miyataka/api"},
										"labels": {"nodes": []},
										"assignees": {"nodes": []}
									},
									"fieldValues": {"nodes": [{"__typename": "ProjectV2ItemFieldSingleSelectValue", "name": "Todo", "field": {"name": "Status"}}]}
								},
								{
									"id": "PVTI_2",
									"content": {
										"__typename": "Issue",
										"id": "I_2",
										"number": 2,
										"title": "Blocked by allowlist",
										"body": "",
										"url": "https://github.com/miyataka/other/issues/2",
										"state": "OPEN",
										"repository": {"nameWithOwner": "miyataka/other", "url": "https://github.com/miyataka/other"},
										"labels": {"nodes": []},
										"assignees": {"nodes": []}
									},
									"fieldValues": {"nodes": [{"__typename": "ProjectV2ItemFieldSingleSelectValue", "name": "Todo", "field": {"name": "Status"}}]}
								}
							],
							"pageInfo": {"hasNextPage": false, "endCursor": null}
						}
					}
				}
			}
		}`))
	}))
	defer server.Close()

	client, err := New(workflow.TrackerConfig{
		Token:               "token",
		Endpoint:            server.URL,
		Owner:               "miyataka",
		OwnerType:           "user",
		ProjectNumber:       1,
		StatusField:         "Status",
		ActiveStates:        []string{"Todo"},
		TerminalStates:      []string{"Done"},
		AllowedRepositories: []string{"miyataka/api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	if issues[0].RepositoryNameWithOwner != "miyataka/api" {
		t.Fatalf("unexpected repository: %q", issues[0].RepositoryNameWithOwner)
	}
	if issues[0].RepositorySSHURL != "git@github.com:miyataka/api.git" {
		t.Fatalf("unexpected fallback ssh url: %q", issues[0].RepositorySSHURL)
	}
}

func TestFetchIssuesByStatesAddsOpenBlockers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			if r.URL.Path != "/repos/miyataka/api/issues/10/dependencies/blocked_by" {
				t.Fatalf("unexpected dependency path: %s", r.URL.String())
			}
			_, _ = w.Write([]byte(`[
				{
					"node_id": "I_BLOCKER_OPEN",
					"number": 9,
					"state": "open",
					"title": "Open blocker",
					"html_url": "https://github.com/miyataka/api/issues/9"
				},
				{
					"node_id": "I_BLOCKER_CLOSED",
					"number": 8,
					"state": "closed",
					"title": "Closed blocker",
					"html_url": "https://github.com/miyataka/api/issues/8"
				}
			]`))
			return
		}
		writeProjectIssueFixture(w, "miyataka/api", 10)
	}))
	defer server.Close()

	client, err := New(workflow.TrackerConfig{
		Token:                 "token",
		Endpoint:              server.URL,
		RestEndpoint:          server.URL,
		Owner:                 "miyataka",
		OwnerType:             "user",
		ProjectNumber:         1,
		StatusField:           "Status",
		ActiveStates:          []string{"Todo"},
		TerminalStates:        []string{"Done"},
		ReadIssueDependencies: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	if len(issues[0].BlockedBy) != 1 {
		t.Fatalf("expected one open blocker, got %#v", issues[0].BlockedBy)
	}
	blocker := issues[0].BlockedBy[0]
	if blocker.ID != "I_BLOCKER_OPEN" || blocker.Identifier != "miyataka/api#9" || blocker.State != "open" {
		t.Fatalf("unexpected blocker: %#v", blocker)
	}
}

func TestFetchIssuesByStatesCanSkipIssueDependencies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			t.Fatalf("did not expect dependency request")
		}
		writeProjectIssueFixture(w, "miyataka/api", 10)
	}))
	defer server.Close()

	client, err := New(workflow.TrackerConfig{
		Token:                 "token",
		Endpoint:              server.URL,
		RestEndpoint:          server.URL,
		Owner:                 "miyataka",
		OwnerType:             "user",
		ProjectNumber:         1,
		StatusField:           "Status",
		ActiveStates:          []string{"Todo"},
		TerminalStates:        []string{"Done"},
		ReadIssueDependencies: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	issues, err := client.FetchCandidateIssues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected one issue, got %d", len(issues))
	}
	if len(issues[0].BlockedBy) != 0 {
		t.Fatalf("expected no blockers, got %#v", issues[0].BlockedBy)
	}
}

func writeProjectIssueFixture(w http.ResponseWriter, repository string, number int) {
	_, _ = fmt.Fprintf(w, `{
		"data": {
			"user": {
				"projectV2": {
					"items": {
						"nodes": [{
							"id": "PVTI_1",
							"content": {
								"__typename": "Issue",
								"id": "I_1",
								"number": %d,
								"title": "Implement thing",
								"body": "Body text",
								"url": "https://github.com/%s/issues/%d",
								"state": "OPEN",
								"createdAt": "2026-05-01T00:00:00Z",
								"updatedAt": "2026-05-01T00:01:00Z",
								"repository": {"nameWithOwner": "%s", "url": "https://github.com/%s"},
								"labels": {"nodes": []},
								"assignees": {"nodes": []}
							},
							"fieldValues": {
								"nodes": [{"__typename": "ProjectV2ItemFieldSingleSelectValue", "name": "Todo", "field": {"name": "Status"}}]
							}
						}],
						"pageInfo": {"hasNextPage": false, "endCursor": null}
					}
				}
			}
		}
	}`, number, repository, number, repository, repository)
}
