# Go App Server Session Runner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reuse one Codex app-server client and thread across all turns in a single Go orchestrator issue run.

**Architecture:** Keep command execution unchanged. Add a small per-run agent runner abstraction inside `go/internal/orchestrator` so app-server runtime can start once, run multiple turns, and close once when the issue run exits.

**Tech Stack:** Go orchestrator, existing `internal/codexappserver` client, helper-process fake app-server tests.

---

### Task 1: Failing Reuse Test

**Files:**
- Modify: `go/internal/orchestrator/orchestrator_test.go`

- [ ] **Step 1: Add a test for issue-run session reuse**

Add a test that configures `agent.runtime: app-server`, runs an issue with two active turns, and uses a fake app-server command that records:

```text
initialize
thread/start
turn/start
turn/start
```

The test should fail against the current implementation because it starts app-server once per turn.

- [ ] **Step 2: Run the failing test**

Run:

```sh
cd go && go test ./internal/orchestrator -run TestRunIssueReusesAppServerSessionAcrossTurns -count=1
```

Expected before implementation: failure showing more than one `thread/start` or process start.

### Task 2: Runner Implementation

**Files:**
- Modify: `go/internal/orchestrator/orchestrator.go`
- Modify: `go/internal/orchestrator/fallback.go`

- [ ] **Step 1: Add runner interface**

Add an internal interface:

```go
type agentTurnRunner interface {
	RunTurn(context.Context, string, tracker.Issue, int) error
	Close() error
}
```

- [ ] **Step 2: Move command execution behind command runner**

Keep the existing command prompt file and process-group behavior in a command runner.

- [ ] **Step 3: Add app-server runner**

The app-server runner should start the `codexappserver.Client` lazily on the first turn, reuse it for later turns, and close it at the end of the issue run.

- [ ] **Step 4: Wire runIssue to create one runner per active profile**

Create the primary runner before the turn loop. If fallback switches profile, close the old runner and create a command fallback runner.

### Task 3: Verification and Docs

**Files:**
- Modify: `go/README.md`

- [ ] **Step 1: Update limitation text**

Document that app-server runtime keeps one synchronous app-server session per issue run.

- [ ] **Step 2: Run focused verification**

Run:

```sh
cd go && go test ./internal/orchestrator ./internal/codexappserver -count=1
```

- [ ] **Step 3: Run full verification**

Run:

```sh
make -C go all
cd go && go test -race ./...
make -C elixir MIX='mise exec -- mix' all
```
