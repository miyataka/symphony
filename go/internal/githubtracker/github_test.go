package githubtracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
									"repository": {"nameWithOwner": "miyataka/symphony"},
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
