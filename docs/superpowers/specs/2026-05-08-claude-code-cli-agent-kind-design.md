# Symphony Go: Claude Code CLI 対応 (`agent.kind` プロファイル化)

**Date:** 2026-05-08
**Status:** Draft (awaiting implementation plan)
**Scope:** `go/` ディレクトリのみ。Elixir 実装は対象外。

## 背景

Symphony Go (`go/`) は WORKFLOW front matter の `agent.command` で任意のシェルコマンドを起動するため、技術的にはどんなコーディングエージェントでも実行できる。しかし以下の点で Codex (OpenAI Codex CLI) 前提になっており、Claude Code CLI を素直に動かすにはユーザが複数の設定を手動で揃える必要がある:

- `tracker.workpad_marker` のデフォルトが `## Codex Workpad` 固定
- `orchestrator.workpadBody()` が `## Codex Workpad` をハードコードしており、`workpad_marker` をユーザが上書きしても本文に反映されない（潜在バグ）
- `agent.command` のデフォルトがなく、Claude Code 用の慣用呼び出しを毎回ユーザが書く必要がある
- README / `WORKFLOW.github.md` のドキュメントが Codex 前提

このギャップを `agent.kind` というビルトインプロファイルで埋める。

## ゴール

1. `agent.kind: claude-code` を 1 行加えるだけで Claude Code CLI が回るようにする。
2. `agent.kind` 未指定時の挙動は現行 (Codex) と完全互換。
3. `tracker.workpad_marker` 上書き時に本文と検出マーカーが不整合になる既存バグを解消する。

## 非ゴール

- Codex app-server JSON-RPC プロトコル相当のネイティブ統合（`agent.command` 経由の起動を前提とする）
- Elixir 実装への波及
- `agent.kind` 以外の新エージェント追加（gemini-cli など）。将来の拡張余地は残すが、本スコープでは codex / claude-code の二択のみ実装

## 設定スキーマ変更

`go/internal/workflow/workflow.go` の `AgentConfig` に `Kind` を追加する:

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

### 受け付ける値

| 入力 | 正規化後 | 意味 |
|---|---|---|
| 未指定 / `""` / 空白のみ | `codex` | 既存挙動を維持 |
| `codex` / `Codex` / ` CODEX ` | `codex` | 大文字小文字・前後空白は吸収 |
| `claude-code` / `Claude-Code` | `claude-code` | 同上 |
| その他の文字列 | (エラー) | `Resolve()` で `agent.kind must be "codex" or "claude-code", got %q` |

### kind から導出されるデフォルト

`Resolve()` で以下の順序で確定する:

1. `Agent.Kind` を正規化 (`strings.ToLower(strings.TrimSpace(...))`)、空なら `codex`、未知値ならエラー。
2. ユーザが `Agent.Command` を明示していなければ kind 由来のデフォルトをセット。
3. ユーザが `Tracker.WorkpadMarker` を明示していなければ kind 由来のデフォルトをセット。

| kind | `agent.command` の既定 | `tracker.workpad_marker` の既定 |
|---|---|---|
| `codex` | (空のまま — 既存どおりユーザが明示する必要あり) | `## Codex Workpad` |
| `claude-code` | `cat "$SYMPHONY_PROMPT_FILE" \| claude -p --dangerously-skip-permissions` | `## Claude Workpad` |

優先順位: **ユーザー明示 > kind 由来デフォルト**。`agent.kind: claude-code` でも `agent.command` を書けばそれが勝つ。

「明示」の判定:
- `Agent.Command` については `strings.TrimSpace(Agent.Command) == ""` を「未指定」と扱う
- `Tracker.WorkpadMarker` については `strings.TrimSpace(Tracker.WorkpadMarker) == ""` を「未指定」と扱う

注意: `codex` の `agent.command` デフォルトは現状提供しない（既存ユーザーが明示している前提を崩さないため、また orchestrator.go:324 の空 command 時のスキップ挙動を保持するため）。Claude Code 側のみデフォルトを生成する。

注意: `Agent.Kind` は `Resolve()` 内で正規化（lowercase + trim）し、その正規化済み値を `c.Agent.Kind` に書き戻す。orchestrator や他コードからは正規化済み前提で参照できる。

### 既定値ロジック移動

現状 `defaultConfig()` (workflow.go:172) と `Resolve()` (workflow.go:246) の二箇所で `WorkpadMarker = "## Codex Workpad"` を設定している。これを `Resolve()` 内で `Agent.Kind` を見て分岐する形に統一する。`defaultConfig()` 側の `WorkpadMarker` 初期化は削除する（`Resolve()` で確実にセットされるため）。

## `workpadBody` のハードコード修正

`go/internal/orchestrator/orchestrator.go:720` の `workpadBody()` は free function で `## Codex Workpad` をハードコードしている。このため `tracker.workpad_marker` を上書きしても、Symphony が書き込むコメント本文の先頭は変わらない。`workpad_marker` をマーカー検出に使う一方で本文は別文字列となるため、上書き後にコメントが見つからず重複コメントを作る潜在バグ。

修正方針:

- `workpadBody` を orchestrator のメソッドにする (`func (s *Service) workpadBody(...) string`)
- 本文先頭の `## Codex Workpad` を `s.cfg.Tracker.WorkpadMarker` に置き換える
- 6 箇所の呼び出し (`orchestrator.go` 内) を `s.workpadBody(...)` に書き換える

呼び出し箇所 (現状):

- orchestrator.go:176
- orchestrator.go:266
- orchestrator.go:281
- orchestrator.go:315
- orchestrator.go:560
- orchestrator.go:564

## 触らないもの

- `hooks` (after_create / before_run / after_run / before_remove): エージェント非依存
- `SYMPHONY_*` 環境変数群: そのまま再利用
- `max_turns` / `max_concurrent_agents` の意味: Symphony 側オーケストレーションループ回数。kind に依存しない
- `agent.command` 経由の prompt 受け渡し方式: 現状の `$SYMPHONY_PROMPT_FILE` のまま
- Codex 側の挙動: 後方互換を完全維持

## ドキュメント

`go/README.md` に追記する内容:

1. `agent.kind` 節を新設し、`codex` / `claude-code` 両方の最小設定例を併記。
2. kind 未指定時は codex 互換であることを明記。
3. `claude-code` のデフォルトコマンドおよび workpad マーカーをドキュメント化。
4. `claude` バイナリは Claude Code CLI を指し、PATH に通っている前提。

`WORKFLOW.github.md` は codex のままにし、別ファイル (`WORKFLOW.claude.md` など) は作らない（README に最小例を載せれば十分なため）。

## テスト

`go/internal/workflow/workflow_test.go`:

- `agent.kind: claude-code` のパース成功と正規化（大文字混在・空白）
- `agent.kind: invalid` のエラー（メッセージ含む）
- `agent.kind` 未指定時、`codex` に正規化されること
- `agent.kind: claude-code` で `agent.command` 未指定なら `cat "$SYMPHONY_PROMPT_FILE" | claude -p --dangerously-skip-permissions` がセットされること
- `agent.kind: claude-code` で `agent.command` が明示されていればユーザ値が保持されること
- `agent.kind: claude-code` で `tracker.workpad_marker` 未指定なら `## Claude Workpad` がセットされること
- `agent.kind: claude-code` で `tracker.workpad_marker` が明示されていればユーザ値が保持されること
- `agent.kind` 未指定で `tracker.workpad_marker` 未指定なら従来通り `## Codex Workpad` がセットされること

`go/internal/orchestrator/orchestrator_test.go` (既存):

- `## Codex Workpad` を期待する既存テストはそのまま通ること（kind 未指定の codex デフォルト）
- 新規: `tracker.workpad_marker: "## Claude Workpad"` を設定したサービスで、`workpadBody` 出力本文が `## Claude Workpad` から始まること
- 新規: 上記 marker 設定でコメント upsert を行ったとき検出と本文が一致し、重複コメントを作らないこと（既存 upsert テストの marker 差し替えで再利用可能なら拡張）

`go/internal/githubtracker/github_test.go`:

- 既存テストの `## Codex Workpad` 期待を維持（後方互換確認）

## 後方互換性

- 既存ユーザの WORKFLOW (kind 未指定) は変更不要で従来通り動く。
- 既存の `tracker.workpad_marker` 上書き設定は kind に関係なくユーザ値が勝つ。
- `defaultConfig()` から `WorkpadMarker` の初期化を削除するが、`Resolve()` 完了後の状態は同じ (`## Codex Workpad`) になるため、外部から見た挙動は不変。

## 想定リスク

- `claude` コマンドが PATH に存在しない環境で `agent.kind: claude-code` を設定した場合、エージェントターン実行時のシェル起動失敗として現れる。Symphony は既存のリトライ／失敗ハンドリングで処理する。事前検証 (which claude) は今回スコープ外。
- `--dangerously-skip-permissions` を既定で有効にしている。Symphony はワークスペースをサンドボックスとして扱う設計なのでこの方針は妥当だが、README に明示する。

## 実装順序（plan 化のための目安）

1. `AgentConfig.Kind` 追加と `Resolve()` の正規化・検証
2. kind 由来デフォルト適用ロジック
3. `defaultConfig()` の `WorkpadMarker` 削除と `Resolve()` への統一
4. `workpadBody` メソッド化と全呼び出し置換
5. `workflow_test.go` 拡張
6. `orchestrator_test.go` 拡張
7. README 更新

各ステップは独立してコンパイル・テスト可能。
