# Symphony Go: Claude Code CLI 対応 (`agent.kind`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Symphony Go の WORKFLOW front matter に `agent.kind: claude-code` プロファイルを追加し、Claude Code CLI を最小設定で動かせるようにする。同時に `workpadBody` のマーカーハードコードを修正する。

**Architecture:** `AgentConfig.Kind` フィールドを追加して `Resolve()` で正規化・検証を行い、kind に応じて `agent.command` と `tracker.workpad_marker` の既定値を導出する。`orchestrator.workpadBody` を `Service` のメソッド化して `cfg.Tracker.WorkpadMarker` を本文先頭に出力する。`agent.kind` 未指定時の挙動は完全な後方互換を維持する。

**Tech Stack:** Go 1.23+, `gopkg.in/yaml.v3`, `testing` 標準ライブラリ。

**Spec:** `docs/superpowers/specs/2026-05-08-claude-code-cli-agent-kind-design.md`

---

## File Structure

| Path | Role | Status |
|---|---|---|
| `go/internal/workflow/workflow.go` | `AgentConfig` に `Kind` フィールドを追加。`Resolve()` で kind 正規化・検証・既定値導出 | Modify |
| `go/internal/workflow/workflow_test.go` | kind パース・正規化・無効値・既定値導出・上書き優先のテスト | Modify |
| `go/internal/orchestrator/orchestrator.go` | `workpadBody` を `Service` のメソッド化、6 callsite 更新 | Modify |
| `go/internal/orchestrator/orchestrator_test.go` | 設定 marker が body 先頭に反映されることのテスト | Modify |
| `go/README.md` | `agent.kind` 節の追加と Claude Code 最小設定例 | Modify |

---

## Task 1: `AgentConfig.Kind` フィールドの追加と正規化・検証

**Files:**
- Modify: `go/internal/workflow/workflow.go` (AgentConfig struct, Resolve())
- Modify: `go/internal/workflow/workflow_test.go` (新規テスト追加)

- [ ] **Step 1.1: 失敗テストを追加（kind=claude-code パースと正規化）**

`go/internal/workflow/workflow_test.go` の末尾に以下を追加:

```go
func TestParseConfigAcceptsClaudeCodeKind(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": " Claude-Code ",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Kind != "claude-code" {
		t.Fatalf("expected normalized kind \"claude-code\", got %q", cfg.Agent.Kind)
	}
}

func TestParseConfigDefaultsKindToCodex(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Kind != "codex" {
		t.Fatalf("expected default kind \"codex\", got %q", cfg.Agent.Kind)
	}
}

func TestParseConfigRejectsUnknownAgentKind(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	_, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": "gemini",
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown agent kind")
	}
	if !strings.Contains(err.Error(), "agent.kind") {
		t.Fatalf("expected error to mention agent.kind, got %v", err)
	}
}
```

`workflow_test.go` の import に `"strings"` が無ければ追加。

- [ ] **Step 1.2: テスト失敗を確認**

Run: `cd go && go test ./internal/workflow/ -run 'TestParseConfigAcceptsClaudeCodeKind|TestParseConfigDefaultsKindToCodex|TestParseConfigRejectsUnknownAgentKind' -v`

Expected: 3 件すべて FAIL（`Kind` フィールド未定義によるコンパイルエラー、または kind 取得不能による失敗）。

- [ ] **Step 1.3: `AgentConfig` に `Kind` フィールドを追加**

`go/internal/workflow/workflow.go` の AgentConfig (現状 81-88 行付近):

```go
type AgentConfig struct {
	Kind                       string         `yaml:"kind"`
	Command                    string         `yaml:"command"`
	MaxConcurrentAgents        int            `yaml:"max_concurrent_agents"`
	MaxConcurrentAgentsByState map[string]int `yaml:"max_concurrent_agents_by_state"`
	MaxTurns                   int            `yaml:"max_turns"`
	MaxRetryBackoffMS          int            `yaml:"max_retry_backoff_ms"`
	TurnTimeoutMS              int            `yaml:"turn_timeout_ms"`
}
```

- [ ] **Step 1.4: `Resolve()` に kind 正規化と検証を追加**

`go/internal/workflow/workflow.go` の `Resolve()` 内、Tracker 系の正規化ブロックの直後（現状の `c.Tracker.OwnerType = ...` の直後、`if c.Tracker.OwnerType == ""` ブロックより前か後ろのいずれかで、いずれにしても **`if c.Tracker.WorkpadMarker == ""` の前**）に追加:

```go
	c.Agent.Kind = strings.ToLower(strings.TrimSpace(c.Agent.Kind))
	if c.Agent.Kind == "" {
		c.Agent.Kind = "codex"
	}
	switch c.Agent.Kind {
	case "codex", "claude-code":
	default:
		return fmt.Errorf("agent.kind must be \"codex\" or \"claude-code\", got %q", c.Agent.Kind)
	}
```

注意: Task 2 で `WorkpadMarker` の既定値が `c.Agent.Kind` を参照するので、kind 正規化はその参照より前で確実に完了している必要がある。具体的には `c.Tracker.AllowedRepositories = normalizeList(...)` の直後あたりに置くと、`WorkpadMarker` の default 分岐 (246 行付近) より十分早く実行される。

- [ ] **Step 1.5: テスト成功を確認**

Run: `cd go && go test ./internal/workflow/ -run 'TestParseConfigAcceptsClaudeCodeKind|TestParseConfigDefaultsKindToCodex|TestParseConfigRejectsUnknownAgentKind' -v`

Expected: 3 件すべて PASS。

- [ ] **Step 1.6: workflow パッケージ全体の回帰確認**

Run: `cd go && go test ./internal/workflow/...`

Expected: PASS（既存テストへの影響なし）。

- [ ] **Step 1.7: コミット**

```bash
git add go/internal/workflow/workflow.go go/internal/workflow/workflow_test.go
git commit -m "$(cat <<'EOF'
Add agent.kind field with codex/claude-code validation

Introduces AgentConfig.Kind ("codex" | "claude-code") with
normalization and validation in Resolve(). Empty value defaults to
"codex" preserving backward compatibility.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `tracker.workpad_marker` を kind に応じて既定値設定

**Files:**
- Modify: `go/internal/workflow/workflow.go` (defaultConfig, Resolve)
- Modify: `go/internal/workflow/workflow_test.go`

- [ ] **Step 2.1: 失敗テストを追加（kind→marker 既定値および上書き優先）**

`go/internal/workflow/workflow_test.go` の末尾に追加:

```go
func TestParseConfigDefaultsClaudeCodeWorkpadMarker(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": "claude-code",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracker.WorkpadMarker != "## Claude Workpad" {
		t.Fatalf("expected default marker \"## Claude Workpad\", got %q", cfg.Tracker.WorkpadMarker)
	}
}

func TestParseConfigDefaultsCodexWorkpadMarker(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracker.WorkpadMarker != "## Codex Workpad" {
		t.Fatalf("expected default marker \"## Codex Workpad\", got %q", cfg.Tracker.WorkpadMarker)
	}
}

func TestParseConfigPreservesExplicitWorkpadMarker(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
			"workpad_marker": "## Custom Workpad",
		},
		"agent": map[string]any{
			"kind": "claude-code",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tracker.WorkpadMarker != "## Custom Workpad" {
		t.Fatalf("expected user-provided marker preserved, got %q", cfg.Tracker.WorkpadMarker)
	}
}
```

- [ ] **Step 2.2: テスト失敗を確認**

Run: `cd go && go test ./internal/workflow/ -run 'TestParseConfigDefaultsClaudeCodeWorkpadMarker|TestParseConfigDefaultsCodexWorkpadMarker|TestParseConfigPreservesExplicitWorkpadMarker' -v`

Expected: `TestParseConfigDefaultsClaudeCodeWorkpadMarker` が FAIL（claude 用デフォルト未実装、`## Codex Workpad` が入る）。他 2 件は PASS。

- [ ] **Step 2.3: `defaultConfig()` から `WorkpadMarker` 初期化を削除**

`go/internal/workflow/workflow.go` の `defaultConfig()` (172 行付近) の TrackerConfig literal から以下を削除:

```go
WorkpadMarker:         "## Codex Workpad",
```

- [ ] **Step 2.4: `Resolve()` で kind 由来の marker 既定値設定**

`go/internal/workflow/workflow.go` の `Resolve()` 内、現状の以下のブロック（246-248 行付近）:

```go
	if c.Tracker.WorkpadMarker == "" {
		c.Tracker.WorkpadMarker = "## Codex Workpad"
	}
```

を以下に置き換える（kind 正規化が先に実行されている前提で、kind 正規化ブロックの後ろに位置を移動するか、kind 正規化を `WorkpadMarker` 既定値より前に置く）:

```go
	if strings.TrimSpace(c.Tracker.WorkpadMarker) == "" {
		switch c.Agent.Kind {
		case "claude-code":
			c.Tracker.WorkpadMarker = "## Claude Workpad"
		default:
			c.Tracker.WorkpadMarker = "## Codex Workpad"
		}
	}
```

注意: `c.Agent.Kind` の正規化ブロック（Step 1.4 で追加）が `WorkpadMarker` 既定値設定より前に実行される位置関係に並べる。`Resolve()` の上から順に並べ、`Agent.Kind` 正規化を `WorkpadMarker` 既定値分岐より前に置く。

- [ ] **Step 2.5: テスト成功を確認**

Run: `cd go && go test ./internal/workflow/ -run 'TestParseConfigDefaultsClaudeCodeWorkpadMarker|TestParseConfigDefaultsCodexWorkpadMarker|TestParseConfigPreservesExplicitWorkpadMarker' -v`

Expected: 3 件すべて PASS。

- [ ] **Step 2.6: 全 workflow テストの回帰確認**

Run: `cd go && go test ./internal/workflow/...`

Expected: PASS。

- [ ] **Step 2.7: コミット**

```bash
git add go/internal/workflow/workflow.go go/internal/workflow/workflow_test.go
git commit -m "$(cat <<'EOF'
Derive workpad_marker default from agent.kind

Move WorkpadMarker default into Resolve() and pick "## Claude Workpad"
when agent.kind is "claude-code"; "## Codex Workpad" otherwise. User-
provided tracker.workpad_marker still wins.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: `agent.command` の Claude Code 既定値

**Files:**
- Modify: `go/internal/workflow/workflow.go` (Resolve)
- Modify: `go/internal/workflow/workflow_test.go`

- [ ] **Step 3.1: 失敗テストを追加（claude-code → command 既定値）**

`go/internal/workflow/workflow_test.go` の末尾に追加:

```go
func TestParseConfigDefaultsClaudeCodeCommand(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind": "claude-code",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := `cat "$SYMPHONY_PROMPT_FILE" | claude -p --dangerously-skip-permissions`
	if cfg.Agent.Command != expected {
		t.Fatalf("expected default claude-code command\n  want: %q\n  got:  %q", expected, cfg.Agent.Command)
	}
}

func TestParseConfigPreservesExplicitClaudeCodeCommand(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
		"agent": map[string]any{
			"kind":    "claude-code",
			"command": "claude --print < $SYMPHONY_PROMPT_FILE",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Command != "claude --print < $SYMPHONY_PROMPT_FILE" {
		t.Fatalf("user command not preserved, got %q", cfg.Agent.Command)
	}
}

func TestParseConfigCodexLeavesCommandEmpty(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	cfg, err := ParseConfig(map[string]any{
		"tracker": map[string]any{
			"owner":          "miyataka",
			"project_number": 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Command != "" {
		t.Fatalf("expected codex default to leave command empty, got %q", cfg.Agent.Command)
	}
}
```

- [ ] **Step 3.2: テスト失敗を確認**

Run: `cd go && go test ./internal/workflow/ -run 'TestParseConfigDefaultsClaudeCodeCommand|TestParseConfigPreservesExplicitClaudeCodeCommand|TestParseConfigCodexLeavesCommandEmpty' -v`

Expected: `TestParseConfigDefaultsClaudeCodeCommand` が FAIL（command が空のまま）。他 2 件は PASS。

- [ ] **Step 3.3: `Resolve()` で claude-code 既定 command 設定**

`go/internal/workflow/workflow.go` の `Resolve()` 内、`Agent.Kind` 正規化ブロックの直後に追加:

```go
	if strings.TrimSpace(c.Agent.Command) == "" && c.Agent.Kind == "claude-code" {
		c.Agent.Command = `cat "$SYMPHONY_PROMPT_FILE" | claude -p --dangerously-skip-permissions`
	}
```

注意: codex の場合は既定値を生成しない（既存の orchestrator 側の「空 command 時はスキップ」挙動を維持するため）。

- [ ] **Step 3.4: テスト成功を確認**

Run: `cd go && go test ./internal/workflow/ -run 'TestParseConfigDefaultsClaudeCodeCommand|TestParseConfigPreservesExplicitClaudeCodeCommand|TestParseConfigCodexLeavesCommandEmpty' -v`

Expected: 3 件すべて PASS。

- [ ] **Step 3.5: 全 workflow テストの回帰確認**

Run: `cd go && go test ./internal/workflow/...`

Expected: PASS。

- [ ] **Step 3.6: コミット**

```bash
git add go/internal/workflow/workflow.go go/internal/workflow/workflow_test.go
git commit -m "$(cat <<'EOF'
Default agent.command for claude-code kind

When agent.kind is "claude-code" and agent.command is empty, set the
default to pipe SYMPHONY_PROMPT_FILE into claude -p with permission
prompts disabled. codex kind keeps the existing empty default.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: `workpadBody` のメソッド化と marker 反映

**Files:**
- Modify: `go/internal/orchestrator/orchestrator.go` (workpadBody + 6 callers)
- Modify: `go/internal/orchestrator/orchestrator_test.go`

- [ ] **Step 4.1: 失敗テストを追加（設定 marker が body に反映）**

`go/internal/orchestrator/orchestrator_test.go` の末尾に追加:

```go
func TestApplyReviewStatePolicyUsesConfiguredWorkpadMarker(t *testing.T) {
	cfg := testConfig()
	cfg.Tracker.WorkpadMarker = "## Claude Workpad"
	recorder := &recordingTracker{}
	service := New(Options{
		Config:  cfg,
		Tracker: recorder,
	})
	handled := service.applyReviewStatePolicy(context.Background(), tracker.Issue{
		ID:         "I_1",
		Identifier: "repo#1",
		Title:      "Issue",
		State:      "Human Review",
		PullRequests: []tracker.PullRequest{{
			ReviewDecision: "CHANGES_REQUESTED",
		}},
	})
	if !handled {
		t.Fatal("expected policy to handle issue")
	}
	if recorder.workpad == "" {
		t.Fatal("expected workpad update")
	}
	if !strings.HasPrefix(recorder.workpad, "## Claude Workpad") {
		t.Fatalf("expected workpad body to start with configured marker, got: %q", recorder.workpad)
	}
}
```

`orchestrator_test.go` の import に `"strings"` が無ければ追加。

- [ ] **Step 4.2: テスト失敗を確認**

Run: `cd go && go test ./internal/orchestrator/ -run TestApplyReviewStatePolicyUsesConfiguredWorkpadMarker -v`

Expected: FAIL（body が `## Codex Workpad` で始まる）。

- [ ] **Step 4.3: `workpadBody` を `Service` のメソッドに変更**

`go/internal/orchestrator/orchestrator.go` の `workpadBody` 定義 (720 行付近) を以下に変更:

```go
func (s *Service) workpadBody(issue tracker.Issue, status, workspacePath, note string) string {
	marker := strings.TrimSpace(s.cfg.Tracker.WorkpadMarker)
	if marker == "" {
		marker = "## Codex Workpad"
	}
	lines := []string{
		marker,
		"",
		"### Status",
		"",
		"- Issue: " + issue.Identifier,
		"- State: " + status,
		"- Repository: " + issue.RepositoryNameWithOwner,
	}
	// 以降は現行ロジックそのまま
```

ファイルに `import "strings"` が既にあることを確認（既存 `strings.TrimSpace` 等で利用済みのはず。なければ追加）。

- [ ] **Step 4.4: 6 箇所の呼び出しを `s.workpadBody(...)` に置換**

`go/internal/orchestrator/orchestrator.go` 内の `workpadBody(...)` 呼び出しを以下のように置換:

| 行 | 現状 | 修正後 |
|---|---|---|
| 176 | `s.upsertWorkpad(ctx, issue, workpadBody(issue, state, "", note))` | `s.upsertWorkpad(ctx, issue, s.workpadBody(issue, state, "", note))` |
| 266 | `s.upsertWorkpad(ctx, issue, workpadBody(issue, "Running", path, "Workspace prepared and agent execution started."))` | `s.upsertWorkpad(ctx, issue, s.workpadBody(issue, "Running", path, "Workspace prepared and agent execution started."))` |
| 281 | `s.upsertWorkpad(ctx, issue, workpadBody(issue, "Running", path, fmt.Sprintf("Completed turn %d.", turn)))` | `s.upsertWorkpad(ctx, issue, s.workpadBody(issue, "Running", path, fmt.Sprintf("Completed turn %d.", turn)))` |
| 315 | `s.upsertWorkpad(ctx, refreshed, workpadBody(refreshed, "Human Review", "", "Agent run completed and issue is ready for review."))` | `s.upsertWorkpad(ctx, refreshed, s.workpadBody(refreshed, "Human Review", "", "Agent run completed and issue is ready for review."))` |
| 560 | `s.upsertWorkpad(ctx, issue, workpadBody(issue, issue.State, "", "Linked PR is ready, but automatic merge failed: "+err.Error()))` | `s.upsertWorkpad(ctx, issue, s.workpadBody(issue, issue.State, "", "Linked PR is ready, but automatic merge failed: "+err.Error()))` |
| 564 | `s.upsertWorkpad(ctx, issue, workpadBody(issue, issue.State, "", "Linked PR is ready and automatic merge was submitted."))` | `s.upsertWorkpad(ctx, issue, s.workpadBody(issue, issue.State, "", "Linked PR is ready and automatic merge was submitted."))` |

`sed` ではなく Edit ツールで個別に行うこと（呼び出し元のコンテキストが類似しており unique 識別が必要なため）。

- [ ] **Step 4.5: ビルド成功を確認**

Run: `cd go && go build ./...`

Expected: 成功（コンパイルエラーなし）。

- [ ] **Step 4.6: 失敗テストを再実行し成功を確認**

Run: `cd go && go test ./internal/orchestrator/ -run TestApplyReviewStatePolicyUsesConfiguredWorkpadMarker -v`

Expected: PASS。

- [ ] **Step 4.7: 全 orchestrator テストの回帰確認**

Run: `cd go && go test ./internal/orchestrator/...`

Expected: PASS（既存の `## Codex Workpad` 期待テストも `testConfig()` の codex 既定値経由でそのまま通る）。

- [ ] **Step 4.8: 全パッケージの回帰確認**

Run: `cd go && go test ./...`

Expected: PASS（githubtracker テストも含む）。

- [ ] **Step 4.9: コミット**

```bash
git add go/internal/orchestrator/orchestrator.go go/internal/orchestrator/orchestrator_test.go
git commit -m "$(cat <<'EOF'
Use configured workpad marker in workpadBody

Make workpadBody a Service method and emit
cfg.Tracker.WorkpadMarker as the body's first line. Previously the
marker was hardcoded to "## Codex Workpad" which could diverge from a
user-customized tracker.workpad_marker.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: README に `agent.kind` 節を追加

**Files:**
- Modify: `go/README.md`

- [ ] **Step 5.1: README の Configuration 節の直前または直後に `Agent kinds` 節を追加**

`go/README.md` の Configuration 節 (現状 64 行付近の "## Configuration" 見出し) と現状の minimal example の間、または Configuration 節の冒頭に挿入する形で、以下を追加:

```markdown
## Agent kinds

Symphony selects per-agent defaults from `agent.kind` in the workflow front matter:

| `agent.kind` | Default `agent.command` | Default `tracker.workpad_marker` |
|---|---|---|
| `codex` (or omitted) | (none — must be set explicitly) | `## Codex Workpad` |
| `claude-code` | `cat "$SYMPHONY_PROMPT_FILE" \| claude -p --dangerously-skip-permissions` | `## Claude Workpad` |

Both fields can be overridden per workflow. `agent.kind` is normalized to lower case and an unknown value is rejected at workflow load time.

`claude-code` requires the [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) installed and on `PATH`. The default command runs Claude Code with `--dangerously-skip-permissions` because Symphony already isolates each issue inside a per-issue workspace; if you need stricter sandboxing, set `agent.command` explicitly.

Minimal `claude-code` example:

\`\`\`yaml
agent:
  kind: claude-code
  max_concurrent_agents: 4
  max_turns: 20
\`\`\`
```

(上記コードブロック内の `\`` は実ファイルでは通常のバッククォートにする。Markdown のネスト記法のためエスケープしている。)

- [ ] **Step 5.2: ビルドと全テストの最終確認**

Run: `cd go && go build ./... && go test ./...`

Expected: PASS。

- [ ] **Step 5.3: コミット**

```bash
git add go/README.md
git commit -m "$(cat <<'EOF'
Document agent.kind and claude-code defaults

Add an Agent kinds section to go/README.md describing the codex and
claude-code profiles, their defaults, and a minimal claude-code
workflow example.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## 仕上げ確認

- [ ] **Final: 全パッケージのテストとビルドが通ること**

Run: `cd go && go vet ./... && go test ./...`

Expected: PASS。

- [ ] **Final: spec のすべての要求が実装されていること**

`docs/superpowers/specs/2026-05-08-claude-code-cli-agent-kind-design.md` を再読し、以下を確認:

- `AgentConfig.Kind` 追加済み (Task 1)
- 正規化と無効値拒否 (Task 1)
- `WorkpadMarker` の kind 由来既定値 (Task 2)
- `Agent.Command` の claude-code 既定値（codex は空のまま） (Task 3)
- `defaultConfig()` の `WorkpadMarker` 削除 (Task 2)
- `workpadBody` のメソッド化と marker 反映 (Task 4)
- 6 callsite 更新 (Task 4)
- README 更新 (Task 5)
- 既存テスト後方互換 (Task 1〜4 の回帰確認ステップ)
