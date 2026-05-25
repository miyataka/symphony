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

var (
	ErrNotStarted    = errors.New("codex app-server client is not started")
	ErrInputRequired = errors.New("codex app-server input required")
)

type EventType string

const (
	EventSessionStarted EventType = "session_started"
	EventNotification   EventType = "notification"
	EventMalformed      EventType = "malformed"
	EventInputRequired  EventType = "input_required"
	EventTurnCompleted  EventType = "turn_completed"
)

type TokenUsage struct {
	InputTokens       int64
	OutputTokens      int64
	TotalTokens       int64
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

type TurnResult struct {
	ThreadID string
	TurnID   string
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
			"name":    "symphony-go",
			"title":   "Symphony Go",
			"version": "0.1.0",
		},
	}); err != nil {
		_ = c.Close()
		return err
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		_ = c.Close()
		return err
	}
	result, err := c.call(ctx, "thread/start", map[string]any{
		"cwd":            c.opts.Workspace,
		"approvalPolicy": "never",
		"sandbox":        "workspace-write",
	})
	if err != nil {
		_ = c.Close()
		return err
	}
	threadID, ok := nestedString(result, "thread", "id")
	if !ok {
		_ = c.Close()
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

func (c *Client) RunTurn(ctx context.Context, prompt string) (TurnResult, error) {
	threadID := c.ThreadID()
	if threadID == "" {
		return TurnResult{}, ErrNotStarted
	}
	result, err := c.call(ctx, "turn/start", map[string]any{
		"threadId":       threadID,
		"cwd":            c.opts.Workspace,
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
		if isServerRequest(msg) {
			c.emit(Event{Type: EventInputRequired, ThreadID: threadID, TurnID: turnID, Method: method, Raw: raw})
			return TurnResult{}, ErrInputRequired
		}
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
		InputTokens:       int64Number(total["inputTokens"]),
		OutputTokens:      int64Number(total["outputTokens"]),
		CachedInputTokens: int64Number(total["cachedInputTokens"]),
		TotalTokens:       int64Number(total["totalTokens"]),
	}
}

func isServerRequest(msg map[string]any) bool {
	_, hasID := msg["id"]
	method, _ := msg["method"].(string)
	return hasID && method != ""
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
