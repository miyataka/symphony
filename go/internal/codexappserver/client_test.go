package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
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

func TestRunTurnReturnsInputRequiredForServerRequest(t *testing.T) {
	workspace := t.TempDir()
	events := []Event{}
	client := New(Options{
		Command:   fakeEnvCommand("input-required"),
		Workspace: workspace,
		OnEvent: func(event Event) {
			events = append(events, event)
		},
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

func TestRunTurnRespondsToUnsupportedDynamicToolCall(t *testing.T) {
	workspace := t.TempDir()
	events := []Event{}
	client := New(Options{
		Command:   fakeEnvCommand("unsupported-tool-call"),
		Workspace: workspace,
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	result, err := client.RunTurn(context.Background(), "tool call should continue")
	if err != nil {
		t.Fatal(err)
	}
	if result.TurnID != "turn-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	var sawUnsupported bool
	for _, event := range events {
		if event.Type == EventToolCallUnsupported && event.Method == "item/tool/call" {
			sawUnsupported = true
		}
	}
	if !sawUnsupported {
		t.Fatalf("expected unsupported tool call event, got %#v", events)
	}
}

func TestRunTurnEmitsMalformedAndContinues(t *testing.T) {
	workspace := t.TempDir()
	events := []Event{}
	client := New(Options{
		Command:   fakeEnvCommand("malformed-then-complete"),
		Workspace: workspace,
		OnEvent: func(event Event) {
			events = append(events, event)
		},
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

func TestRunTurnReportsExitBeforeCompletion(t *testing.T) {
	workspace := t.TempDir()
	client := New(Options{
		Command:   fakeEnvCommand("exit-before-complete"),
		Workspace: workspace,
	})
	if err := client.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err := client.RunTurn(context.Background(), "exit before completion")
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
	if !strings.Contains(err.Error(), "before turn completion") {
		t.Fatalf("expected contextual error, got %v", err)
	}
}

func fakeCommand(mode string) string {
	return fmt.Sprintf("%s -test.run=TestFakeAppServer -- %s", strconv.Quote(os.Args[0]), mode)
}

func fakeEnvCommand(mode string) string {
	return "SYMPHONY_FAKE_APP_SERVER=1 " + fakeCommand(mode)
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
				"codexHome":      "/tmp/codex",
				"platformFamily": "unix",
				"platformOs":     "darwin",
				"userAgent":      "fake",
			}})
		case "initialized":
		case "thread/start":
			if mode != "thread-start" && mode != "turn-complete" && mode != "input-required" && mode != "unsupported-tool-call" && mode != "malformed-then-complete" && mode != "exit-before-complete" {
				writeJSON(map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": "unexpected thread start"}})
				continue
			}
			writeJSON(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "thread-1"}}})
		case "turn/start":
			params, _ := msg["params"].(map[string]any)
			if mode == "turn-complete" && !turnInputContains(params, "hello from symphony") {
				writeJSON(map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": "missing prompt text"}})
				continue
			}
			writeJSON(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
			if mode == "input-required" {
				writeJSON(map[string]any{"id": 99, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1"}})
				continue
			}
			if mode == "unsupported-tool-call" {
				writeJSON(map[string]any{"id": 99, "method": "item/tool/call", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "tool": "linear_graphql"}})
				if !scanner.Scan() {
					os.Exit(1)
				}
				var response map[string]any
				if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
					os.Exit(1)
				}
				if response["id"] != float64(99) {
					os.Exit(1)
				}
				result, _ := response["result"].(map[string]any)
				if result["success"] != false {
					os.Exit(1)
				}
				writeJSON(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1"}})
				continue
			}
			if mode == "malformed-then-complete" {
				fmt.Println("{not-json")
				writeJSON(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1"}})
				continue
			}
			if mode == "exit-before-complete" {
				os.Exit(0)
			}
			writeJSON(map[string]any{
				"method": "thread/tokenUsage/updated",
				"params": map[string]any{
					"threadId": "thread-1",
					"turnId":   "turn-1",
					"tokenUsage": map[string]any{
						"total": map[string]any{
							"inputTokens":           30,
							"outputTokens":          12,
							"cachedInputTokens":     5,
							"reasoningOutputTokens": 0,
							"totalTokens":           42,
						},
						"last": map[string]any{
							"inputTokens":           30,
							"outputTokens":          12,
							"cachedInputTokens":     5,
							"reasoningOutputTokens": 0,
							"totalTokens":           42,
						},
					},
				},
			})
			writeJSON(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1"}})
		default:
			writeJSON(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": "unknown method " + method}})
		}
	}
	os.Exit(0)
}

func turnInputContains(params map[string]any, want string) bool {
	input, _ := params["input"].([]any)
	for _, item := range input {
		typed, _ := item.(map[string]any)
		if typed["type"] == "text" && typed["text"] == want {
			return true
		}
	}
	return false
}

func writeJSON(value any) {
	b, _ := json.Marshal(value)
	fmt.Println(string(b))
}
