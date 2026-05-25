# Go Codex App Server Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a standalone Go Codex app-server client package that can launch a fake or real stdio app-server, initialize, start a thread, run one turn, and emit structured events.

**Architecture:** Create `go/internal/codexappserver` as a package independent from the orchestrator. Keep JSON-RPC framing, subprocess lifecycle, and protocol event classification inside the package so a later orchestrator PR can consume a small typed API.

**Tech Stack:** Go 1.23, standard library `os/exec`, newline-delimited JSON-RPC over stdio, fake helper-process tests.

---

## File Structure

- Create `go/internal/codexappserver/client.go`
  - Public `Client`, `Options`, `Event`, `EventType`, `TokenUsage`, and errors.
  - Launch, initialize, thread start, turn start, read loop, and close behavior.
- Create `go/internal/codexappserver/client_test.go`
  - Helper-process fake app-server and unit tests.
- Modify `go/README.md`
  - Document that app-server client support exists but is not wired into the orchestrator yet.

## Task 1: RPC Client Startup

**Files:**
- Create: `go/internal/codexappserver/client.go`
- Create: `go/internal/codexappserver/client_test.go`

- [ ] **Step 1: Write failing startup test**

Create `go/internal/codexappserver/client_test.go`:

```go
package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
)

func TestStartInitializesAndStartsThread(t *testing.T) {
	workspace := t.TempDir()
	events := []Event{}
	client := New(Options{
		Command:   fakeEnvCommand("thread-start"),
		Workspace: workspace,
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if client.ThreadID() != "thread-1" {
		t.Fatalf("unexpected thread id: %q", client.ThreadID())
	}
	if len(events) == 0 || events[0].Type != EventSessionStarted {
		t.Fatalf("expected session started event, got %#v", events)
	}
}

func fakeCommand(mode string) string {
	return fmt.Sprintf("%s -test.run=TestFakeAppServer -- %s", strconv.Quote(os.Args[0]), mode)
}

func TestFakeAppServer(t *testing.T) {
	if os.Getenv("SYMPHONY_FAKE_APP_SERVER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			fmt.Println(`not-json`)
			continue
		}
		method, _ := msg["method"].(string)
		id := msg["id"]
		switch method {
		case "initialize":
			writeJSON(map[string]any{"id": id, "result": map[string]any{
				"codexHome": "/tmp/codex",
				"platformFamily": "unix",
				"platformOs": "darwin",
				"userAgent": "fake",
			}})
		case "initialized":
		case "thread/start":
			if mode != "thread-start" && mode != "turn-complete" {
				writeJSON(map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": "unexpected thread start"}})
				continue
			}
			writeJSON(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "thread-1"}}})
		default:
			writeJSON(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": "unknown method " + method}})
		}
	}
	os.Exit(0)
}

func writeJSON(value any) {
	b, _ := json.Marshal(value)
	fmt.Println(string(b))
}

func fakeEnvCommand(mode string) string {
	return "SYMPHONY_FAKE_APP_SERVER=1 " + fakeCommand(mode)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd go
go test ./internal/codexappserver -run TestStartInitializesAndStartsThread -count=1
```

Expected: build failure because `New`, `Options`, `Event`, and `EventSessionStarted` do not exist.

- [ ] **Step 3: Implement startup client**

Create `go/internal/codexappserver/client.go` with:

```go
package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

var ErrNotStarted = errors.New("codex app-server client is not started")

type EventType string

const (
	EventSessionStarted EventType = "session_started"
	EventNotification   EventType = "notification"
	EventMalformed      EventType = "malformed"
	EventInputRequired  EventType = "input_required"
	EventTurnCompleted  EventType = "turn_completed"
)

type TokenUsage struct {
	InputTokens     int64
	OutputTokens    int64
	TotalTokens     int64
	CachedInputTokens int64
}

type Event struct {
	Type     EventType
	Time     time.Time
	ThreadID string
	TurnID   string
	Method   string
	Message  string
	Usage    *TokenUsage
	Raw      string
}

type Options struct {
	Command   string
	Workspace string
	OnEvent   func(Event)
}

type Client struct {
	opts     Options
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	lines    *bufio.Scanner
	mu       sync.Mutex
	nextID   int64
	threadID string
}

func New(opts Options) *Client {
	return &Client{opts: opts, nextID: 1}
}

func (c *Client) ThreadID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.threadID
}

func (c *Client) Start(ctx context.Context) error {
	if c.opts.Command == "" {
		return errors.New("codex app-server command is required")
	}
	cmd := exec.CommandContext(ctx, "bash", "-lc", c.opts.Command)
	cmd.Dir = c.opts.Workspace
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	c.cmd = cmd
	c.stdin = stdin
	c.lines = bufio.NewScanner(stdout)
	c.lines.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	if _, err := c.call(ctx, "initialize", map[string]any{
		"capabilities": map[string]any{"experimentalApi": true},
		"clientInfo": map[string]any{
			"name": "symphony-go",
			"title": "Symphony Go",
			"version": "0.1.0",
		},
	}); err != nil {
		return err
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	result, err := c.call(ctx, "thread/start", map[string]any{
		"cwd": c.opts.Workspace,
		"approvalPolicy": "never",
		"sandbox": "workspace-write",
	})
	if err != nil {
		return err
	}
	threadID, ok := nestedString(result, "thread", "id")
	if !ok {
		return fmt.Errorf("thread/start response missing thread.id")
	}
	c.mu.Lock()
	c.threadID = threadID
	c.mu.Unlock()
	c.emit(Event{Type: EventSessionStarted, ThreadID: threadID})
	return nil
}

func (c *Client) Close() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params any) (map[string]any, error) {
	id := c.nextRequestID()
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for c.lines.Scan() {
		raw := c.lines.Text()
		var msg map[string]any
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			return nil, fmt.Errorf("%s response malformed: %w", method, err)
		}
		if msgID, ok := asInt64(msg["id"]); ok && msgID == id {
			if errPayload, ok := msg["error"]; ok {
				return nil, fmt.Errorf("%s failed: %v", method, errPayload)
			}
			result, _ := msg["result"].(map[string]any)
			if result == nil {
				result = map[string]any{}
			}
			return result, nil
		}
		c.handleStreamMessage(raw, msg)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	if err := c.lines.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (c *Client) notify(method string, params any) error {
	return c.write(map[string]any{"method": method, "params": params})
}

func (c *Client) write(msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(c.stdin, string(b))
	return err
}

func (c *Client) nextRequestID() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	return id
}

func (c *Client) emit(event Event) {
	event.Time = time.Now().UTC()
	if c.opts.OnEvent != nil {
		c.opts.OnEvent(event)
	}
}

func (c *Client) handleStreamMessage(raw string, msg map[string]any) {
	method, _ := msg["method"].(string)
	c.emit(Event{Type: EventNotification, Method: method, Raw: raw})
}

func nestedString(root map[string]any, first, second string) (string, bool) {
	child, _ := root[first].(map[string]any)
	value, ok := child[second].(string)
	return value, ok
}

func asInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:

```bash
cd go
go test ./internal/codexappserver -run TestStartInitializesAndStartsThread -count=1
```

Expected: PASS.

## Task 2: Turn Completion and Token Usage

**Files:**
- Modify: `go/internal/codexappserver/client.go`
- Modify: `go/internal/codexappserver/client_test.go`

- [ ] **Step 1: Add failing turn test**

Append to `client_test.go`:

```go
func TestRunTurnSendsTextInputAndCompletes(t *testing.T) {
	workspace := t.TempDir()
	events := []Event{}
	client := New(Options{
		Command:   fakeEnvCommand("turn-complete"),
		Workspace: workspace,
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	result, err := client.RunTurn(context.Background(), "hello from symphony")
	if err != nil {
		t.Fatal(err)
	}
	if result.TurnID != "turn-1" {
		t.Fatalf("unexpected turn id: %#v", result)
	}
	var completed bool
	var sawUsage bool
	for _, event := range events {
		if event.Type == EventTurnCompleted && event.TurnID == "turn-1" {
			completed = true
		}
		if event.Usage != nil && event.Usage.TotalTokens == 42 {
			sawUsage = true
		}
	}
	if !completed || !sawUsage {
		t.Fatalf("expected completed and usage events, got %#v", events)
	}
}
```

Extend the fake server's switch:

```go
case "turn/start":
	writeJSON(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
	writeJSON(map[string]any{
		"method": "thread/tokenUsage/updated",
		"params": map[string]any{
			"threadId": "thread-1",
			"turnId": "turn-1",
			"tokenUsage": map[string]any{
				"total": map[string]any{
					"inputTokens": 30,
					"outputTokens": 12,
					"cachedInputTokens": 5,
					"reasoningOutputTokens": 0,
					"totalTokens": 42,
				},
				"last": map[string]any{
					"inputTokens": 30,
					"outputTokens": 12,
					"cachedInputTokens": 5,
					"reasoningOutputTokens": 0,
					"totalTokens": 42,
				},
			},
		},
	})
	writeJSON(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1"}})
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd go
go test ./internal/codexappserver -run TestRunTurnSendsTextInputAndCompletes -count=1
```

Expected: build failure because `RunTurn` and result type do not exist.

- [ ] **Step 3: Implement `RunTurn`**

Add to `client.go`:

```go
type TurnResult struct {
	ThreadID string
	TurnID   string
}

func (c *Client) RunTurn(ctx context.Context, prompt string) (TurnResult, error) {
	threadID := c.ThreadID()
	if threadID == "" {
		return TurnResult{}, ErrNotStarted
	}
	result, err := c.call(ctx, "turn/start", map[string]any{
		"threadId": threadID,
		"cwd": c.opts.Workspace,
		"approvalPolicy": "never",
		"sandboxPolicy": map[string]any{
			"mode": "workspace-write",
		},
		"input": []map[string]any{{
			"type": "text",
			"text": prompt,
		}},
	})
	if err != nil {
		return TurnResult{}, err
	}
	turnID, ok := nestedString(result, "turn", "id")
	if !ok {
		return TurnResult{}, fmt.Errorf("turn/start response missing turn.id")
	}
	for c.lines.Scan() {
		raw := c.lines.Text()
		var msg map[string]any
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			c.emit(Event{Type: EventMalformed, ThreadID: threadID, TurnID: turnID, Raw: raw, Message: err.Error()})
			continue
		}
		method, _ := msg["method"].(string)
		if method == "turn/completed" {
			c.emit(Event{Type: EventTurnCompleted, ThreadID: threadID, TurnID: turnID, Method: method, Raw: raw})
			return TurnResult{ThreadID: threadID, TurnID: turnID}, nil
		}
		c.handleStreamMessage(raw, msg)
		select {
		case <-ctx.Done():
			return TurnResult{}, ctx.Err()
		default:
		}
	}
	if err := c.lines.Err(); err != nil {
		return TurnResult{}, err
	}
	return TurnResult{}, io.EOF
}
```

Update `handleStreamMessage`:

```go
func (c *Client) handleStreamMessage(raw string, msg map[string]any) {
	method, _ := msg["method"].(string)
	params, _ := msg["params"].(map[string]any)
	event := Event{
		Type:     EventNotification,
		Method:   method,
		Raw:      raw,
		ThreadID: stringValue(params["threadId"]),
		TurnID:   stringValue(params["turnId"]),
	}
	if method == "thread/tokenUsage/updated" {
		event.Usage = parseTokenUsage(params)
	}
	c.emit(event)
}
```

Add helpers:

```go
func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func parseTokenUsage(params map[string]any) *TokenUsage {
	tokenUsage, _ := params["tokenUsage"].(map[string]any)
	total, _ := tokenUsage["total"].(map[string]any)
	if total == nil {
		return nil
	}
	return &TokenUsage{
		InputTokens: int64Number(total["inputTokens"]),
		OutputTokens: int64Number(total["outputTokens"]),
		CachedInputTokens: int64Number(total["cachedInputTokens"]),
		TotalTokens: int64Number(total["totalTokens"]),
	}
}

func int64Number(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	default:
		return 0
	}
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
cd go
go test ./internal/codexappserver -count=1
```

Expected: PASS.

## Task 3: Input Required and Malformed Stream Handling

**Files:**
- Modify: `go/internal/codexappserver/client.go`
- Modify: `go/internal/codexappserver/client_test.go`

- [ ] **Step 1: Add failing error-path tests**

Append to `client_test.go`:

```go
func TestRunTurnReturnsInputRequiredForServerRequest(t *testing.T) {
	workspace := t.TempDir()
	events := []Event{}
	client := New(Options{
		Command:   fakeEnvCommand("input-required"),
		Workspace: workspace,
		OnEvent: func(event Event) { events = append(events, event) },
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err := client.RunTurn(context.Background(), "needs approval")
	if !errors.Is(err, ErrInputRequired) {
		t.Fatalf("expected ErrInputRequired, got %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Type != EventInputRequired {
		t.Fatalf("expected input required event, got %#v", events)
	}
}

func TestRunTurnEmitsMalformedAndContinues(t *testing.T) {
	workspace := t.TempDir()
	events := []Event{}
	client := New(Options{
		Command:   fakeEnvCommand("malformed-then-complete"),
		Workspace: workspace,
		OnEvent: func(event Event) { events = append(events, event) },
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.RunTurn(context.Background(), "malformed tolerated"); err != nil {
		t.Fatal(err)
	}
	var malformed bool
	for _, event := range events {
		if event.Type == EventMalformed {
			malformed = true
		}
	}
	if !malformed {
		t.Fatalf("expected malformed event, got %#v", events)
	}
}
```

Add `errors` to imports. Extend fake `turn/start` branch:

```go
case "input-required":
	writeJSON(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
	writeJSON(map[string]any{"id": 99, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1"}})
case "malformed-then-complete":
	writeJSON(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
	fmt.Println("{not-json")
	writeJSON(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1"}})
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
cd go
go test ./internal/codexappserver -run 'TestRunTurn(ReturnsInputRequired|EmitsMalformed)' -count=1
```

Expected: build failure for `ErrInputRequired` or behavior failure because server requests are treated as notifications.

- [ ] **Step 3: Implement request classification**

Add to `client.go`:

```go
var ErrInputRequired = errors.New("codex app-server input required")

func isServerRequest(msg map[string]any) bool {
	_, hasID := msg["id"]
	method, _ := msg["method"].(string)
	return hasID && method != ""
}
```

In `RunTurn`, after decoding `msg` and before `turn/completed`:

```go
if isServerRequest(msg) {
	method, _ := msg["method"].(string)
	c.emit(Event{Type: EventInputRequired, ThreadID: threadID, TurnID: turnID, Method: method, Raw: raw})
	return TurnResult{}, ErrInputRequired
}
```

- [ ] **Step 4: Run package tests**

Run:

```bash
cd go
go test ./internal/codexappserver -count=1
```

Expected: PASS.

## Task 4: Documentation and Full Verification

**Files:**
- Modify: `go/README.md`

- [ ] **Step 1: Document package status**

Add under `## Current limitations` in `go/README.md`:

```markdown
- A standalone Go Codex app-server client package exists under
  `internal/codexappserver`, but the orchestrator still uses `agent.command`.
  App-server execution will be wired behind an opt-in runner in a later change.
```

- [ ] **Step 2: Run Go verification**

Run:

```bash
cd go
make all
```

Expected: fmt, vet, tests, and build pass.

- [ ] **Step 3: Run Elixir repository validation**

Run:

```bash
make -C elixir MIX='mise exec -- mix' all
```

Expected: build, format check, lint, coverage, and dialyzer pass.

- [ ] **Step 4: Commit implementation**

Run:

```bash
git add go/internal/codexappserver go/README.md
git commit -m "feat(go): add Codex app-server client"
```

Expected: one implementation commit after the design and plan commits.
