package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Definition struct {
	Config         map[string]any
	PromptTemplate string
}

type Config struct {
	Tracker       TrackerConfig       `yaml:"tracker"`
	PullRequest   PullRequestConfig   `yaml:"pull_request"`
	Polling       PollingConfig       `yaml:"polling"`
	Workspace     WorkspaceConfig     `yaml:"workspace"`
	Hooks         HooksConfig         `yaml:"hooks"`
	Agent         AgentConfig         `yaml:"agent"`
	Observability ObservabilityConfig `yaml:"observability"`
	LoopMonitor   LoopMonitorConfig   `yaml:"loop_monitor"`
}

type TrackerConfig struct {
	Kind                  string   `yaml:"kind"`
	Token                 string   `yaml:"token"`
	Endpoint              string   `yaml:"endpoint"`
	RestEndpoint          string   `yaml:"rest_endpoint"`
	Owner                 string   `yaml:"owner"`
	OwnerType             string   `yaml:"owner_type"`
	ProjectNumber         int      `yaml:"project_number"`
	StatusField           string   `yaml:"status_field"`
	PriorityField         string   `yaml:"priority_field"`
	Assignee              string   `yaml:"assignee"`
	AllowedRepositories   []string `yaml:"allowed_repositories"`
	StartState            string   `yaml:"start_state"`
	HandoffState          string   `yaml:"handoff_state"`
	ReworkState           string   `yaml:"rework_state"`
	MergingState          string   `yaml:"merging_state"`
	DoneState             string   `yaml:"done_state"`
	WorkpadMarker         string   `yaml:"workpad_marker"`
	ReadIssueDependencies bool     `yaml:"read_issue_dependencies"`
	BacklogStates         []string `yaml:"backlog_states"`
	ActiveStates          []string `yaml:"active_states"`
	MonitorStates         []string `yaml:"monitor_states"`
	TerminalStates        []string `yaml:"terminal_states"`
}

type PollingConfig struct {
	IntervalMS int `yaml:"interval_ms"`
}

type PullRequestConfig struct {
	AutoMerge            bool     `yaml:"auto_merge"`
	MergeMethod          string   `yaml:"merge_method"`
	AllowDraft           bool     `yaml:"allow_draft"`
	RequireApproval      bool     `yaml:"require_approval"`
	RequirePassingChecks bool     `yaml:"require_passing_checks"`
	RequiredCheckNames   []string `yaml:"required_check_names"`
}

type WorkspaceConfig struct {
	Root                  string `yaml:"root"`
	CleanupOrphans        bool   `yaml:"cleanup_orphans"`
	CleanupStaleAfterDays int    `yaml:"cleanup_stale_after_days"`
}

type HooksConfig struct {
	AfterCreate  string `yaml:"after_create"`
	BeforeRun    string `yaml:"before_run"`
	AfterRun     string `yaml:"after_run"`
	BeforeRemove string `yaml:"before_remove"`
	TimeoutMS    int    `yaml:"timeout_ms"`
}

type AgentFallbackConfig struct {
	Enabled *bool    `yaml:"enabled"`
	Kind    string   `yaml:"kind"`
	Command string   `yaml:"command"`
	On      []string `yaml:"on"`
}

type AgentConfig struct {
	Kind                       string              `yaml:"kind"`
	Command                    string              `yaml:"command"`
	Fallback                   AgentFallbackConfig `yaml:"fallback"`
	FallbackKinds              []string            `yaml:"fallback_kinds"`
	FallbackOn                 []string            `yaml:"fallback_on"`
	MaxConcurrentAgents        int                 `yaml:"max_concurrent_agents"`
	MaxConcurrentAgentsByState map[string]int      `yaml:"max_concurrent_agents_by_state"`
	MaxTurns                   int                 `yaml:"max_turns"`
	MaxRetryBackoffMS          int                 `yaml:"max_retry_backoff_ms"`
	TurnTimeoutMS              int                 `yaml:"turn_timeout_ms"`
}

type ObservabilityConfig struct {
	LogJSON          bool   `yaml:"log_json"`
	LogLevel         string `yaml:"log_level"`
	LogFile          string `yaml:"log_file"`
	DashboardEnabled bool   `yaml:"dashboard_enabled"`
	RefreshMS        int    `yaml:"refresh_ms"`
	RenderIntervalMS int    `yaml:"render_interval_ms"`
}

type LoopMonitorConfig struct {
	Enabled       bool   `yaml:"enabled"`
	IntervalMS    int    `yaml:"interval_ms"`
	MaxRuntimeMS  int    `yaml:"max_runtime_ms"`
	MinTurns      int    `yaml:"min_turns"`
	SubIssueState string `yaml:"sub_issue_state"`
}

// ErrFrontMatterNotMap is returned when YAML front matter decodes to a non-object value.
var ErrFrontMatterNotMap = errors.New("workflow front matter must decode to a map")

func LoadFile(path string) (Definition, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, err
	}
	return Parse(b)
}

func Parse(content []byte) (Definition, error) {
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) == 0 || string(bytes.TrimSpace(lines[0])) != "---" {
		return Definition{Config: map[string]any{}, PromptTemplate: strings.TrimSpace(string(content))}, nil
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if string(bytes.TrimSpace(lines[i])) == "---" {
			end = i
			break
		}
	}
	var frontMatterLines [][]byte
	promptLines := [][]byte{}
	if end == -1 {
		frontMatterLines = lines[1:]
	} else {
		frontMatterLines = lines[1:end]
		promptLines = lines[end+1:]
	}

	cfg, err := parseFrontMatterMap(bytes.Join(frontMatterLines, []byte("\n")))
	if err != nil {
		return Definition{}, fmt.Errorf("parse workflow front matter: %w", err)
	}

	return Definition{
		Config:         cfg,
		PromptTemplate: strings.TrimSpace(string(bytes.Join(promptLines, []byte("\n")))),
	}, nil
}

func parseFrontMatterMap(content []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(content)) == 0 {
		return map[string]any{}, nil
	}
	var decoded any
	if err := yaml.Unmarshal(content, &decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		return map[string]any{}, nil
	}
	cfg, ok := decoded.(map[string]any)
	if !ok {
		return nil, ErrFrontMatterNotMap
	}
	return cfg, nil
}

func ParseConfig(raw map[string]any) (Config, error) {
	cfg := defaultConfig()
	b, err := yaml.Marshal(raw)
	if err != nil {
		return Config{}, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Resolve(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		Tracker: TrackerConfig{
			Kind:                  "github",
			Token:                 "$GITHUB_TOKEN",
			Endpoint:              "https://api.github.com/graphql",
			RestEndpoint:          "https://api.github.com",
			OwnerType:             "user",
			StatusField:           "Status",
			PriorityField:         "Priority",
			StartState:            "In Progress",
			HandoffState:          "Human Review",
			ReworkState:           "Rework",
			MergingState:          "Merging",
			DoneState:             "Done",
			ReadIssueDependencies: true,
			BacklogStates:         []string{"Backlog"},
			ActiveStates:          []string{"Todo", "In Progress", "Rework"},
			MonitorStates:         []string{"Human Review", "Merging"},
			TerminalStates:        []string{"Done", "Closed", "Cancelled", "Canceled", "Duplicate"},
		},
		PullRequest: PullRequestConfig{
			MergeMethod:          "MERGE",
			RequireApproval:      true,
			RequirePassingChecks: true,
		},
		Polling: PollingConfig{IntervalMS: int((30 * time.Second) / time.Millisecond)},
		Workspace: WorkspaceConfig{
			Root: filepath.Join(os.TempDir(), "symphony-workspaces"),
		},
		Hooks: HooksConfig{TimeoutMS: int((60 * time.Second) / time.Millisecond)},
		Agent: AgentConfig{
			MaxConcurrentAgents:        4,
			MaxConcurrentAgentsByState: map[string]int{},
			MaxTurns:                   20,
			MaxRetryBackoffMS:          int((5 * time.Minute) / time.Millisecond),
			TurnTimeoutMS:              int((60 * time.Minute) / time.Millisecond),
		},
		LoopMonitor: LoopMonitorConfig{
			Enabled:       true,
			IntervalMS:    int(time.Hour / time.Millisecond),
			MaxRuntimeMS:  int((6 * time.Hour) / time.Millisecond),
			MinTurns:      3,
			SubIssueState: "Backlog",
		},
		Observability: ObservabilityConfig{
			DashboardEnabled: true,
			RefreshMS:        int(time.Second / time.Millisecond),
			RenderIntervalMS: 16,
		},
	}
}

func (c *Config) Resolve() error {
	c.Tracker.Token = resolveEnv(c.Tracker.Token)
	c.Workspace.Root = expandPath(resolveEnv(c.Workspace.Root))
	c.Tracker.Kind = strings.ToLower(strings.TrimSpace(c.Tracker.Kind))
	c.Tracker.OwnerType = strings.ToLower(strings.TrimSpace(c.Tracker.OwnerType))
	c.Tracker.AllowedRepositories = normalizeList(c.Tracker.AllowedRepositories)
	c.Agent.Kind = strings.ToLower(strings.TrimSpace(c.Agent.Kind))
	if c.Agent.Kind == "" {
		c.Agent.Kind = "codex"
	}
	switch c.Agent.Kind {
	case "codex", "claude-code":
	default:
		return fmt.Errorf("agent.kind must be \"codex\" or \"claude-code\", got %q", c.Agent.Kind)
	}
	if strings.TrimSpace(c.Agent.Command) == "" {
		c.Agent.Command = defaultAgentCommand(c.Agent.Kind)
	}
	if err := c.resolveAgentFallback(); err != nil {
		return err
	}
	if c.Tracker.OwnerType == "" {
		c.Tracker.OwnerType = "user"
	}
	if c.Tracker.Endpoint == "" {
		c.Tracker.Endpoint = "https://api.github.com/graphql"
	}
	if c.Tracker.RestEndpoint == "" {
		c.Tracker.RestEndpoint = deriveRestEndpoint(c.Tracker.Endpoint)
	}
	if c.Tracker.StatusField == "" {
		c.Tracker.StatusField = "Status"
	}
	if c.Tracker.StartState == "" {
		c.Tracker.StartState = "In Progress"
	}
	if c.Tracker.HandoffState == "" {
		c.Tracker.HandoffState = "Human Review"
	}
	if c.Tracker.ReworkState == "" {
		c.Tracker.ReworkState = "Rework"
	}
	if c.Tracker.MergingState == "" {
		c.Tracker.MergingState = "Merging"
	}
	if c.Tracker.DoneState == "" {
		c.Tracker.DoneState = "Done"
	}
	if strings.TrimSpace(c.Tracker.WorkpadMarker) == "" {
		switch c.Agent.Kind {
		case "claude-code":
			c.Tracker.WorkpadMarker = "## Claude Workpad"
		default:
			c.Tracker.WorkpadMarker = "## Codex Workpad"
		}
	}
	if len(c.Tracker.MonitorStates) == 0 && c.Tracker.HandoffState != "" {
		c.Tracker.MonitorStates = []string{c.Tracker.HandoffState, c.Tracker.MergingState}
	}
	if c.Polling.IntervalMS <= 0 {
		c.Polling.IntervalMS = int((30 * time.Second) / time.Millisecond)
	}
	c.PullRequest.MergeMethod = strings.ToUpper(strings.TrimSpace(c.PullRequest.MergeMethod))
	if c.PullRequest.MergeMethod == "" {
		c.PullRequest.MergeMethod = "MERGE"
	}
	switch c.PullRequest.MergeMethod {
	case "MERGE", "SQUASH", "REBASE":
	default:
		return fmt.Errorf("pull_request.merge_method must be MERGE, SQUASH, or REBASE, got %q", c.PullRequest.MergeMethod)
	}
	c.PullRequest.RequiredCheckNames = trimList(c.PullRequest.RequiredCheckNames)
	if c.Workspace.CleanupStaleAfterDays < 0 {
		return errors.New("workspace.cleanup_stale_after_days must be >= 0")
	}
	if c.Agent.MaxConcurrentAgents <= 0 {
		c.Agent.MaxConcurrentAgents = 4
	}
	if c.Agent.MaxTurns <= 0 {
		c.Agent.MaxTurns = 20
	}
	if c.Agent.MaxRetryBackoffMS <= 0 {
		c.Agent.MaxRetryBackoffMS = int((5 * time.Minute) / time.Millisecond)
	}
	if c.Agent.TurnTimeoutMS <= 0 {
		c.Agent.TurnTimeoutMS = int((60 * time.Minute) / time.Millisecond)
	}
	if c.Hooks.TimeoutMS <= 0 {
		c.Hooks.TimeoutMS = int((60 * time.Second) / time.Millisecond)
	}
	if c.LoopMonitor.IntervalMS <= 0 {
		c.LoopMonitor.IntervalMS = int(time.Hour / time.Millisecond)
	}
	if c.LoopMonitor.MaxRuntimeMS <= 0 {
		c.LoopMonitor.MaxRuntimeMS = int((6 * time.Hour) / time.Millisecond)
	}
	if c.LoopMonitor.MinTurns <= 0 {
		c.LoopMonitor.MinTurns = 3
	}
	c.LoopMonitor.SubIssueState = strings.TrimSpace(c.LoopMonitor.SubIssueState)
	if c.LoopMonitor.SubIssueState == "" {
		c.LoopMonitor.SubIssueState = "Backlog"
	}
	c.Observability.LogLevel = strings.ToLower(strings.TrimSpace(c.Observability.LogLevel))
	if c.Observability.LogLevel == "" {
		c.Observability.LogLevel = "info"
	}
	switch c.Observability.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("observability.log_level must be debug, info, warn, or error, got %q", c.Observability.LogLevel)
	}
	c.Observability.LogFile = expandPath(resolveEnv(c.Observability.LogFile))
	if c.Observability.RefreshMS <= 0 {
		return errors.New("observability.refresh_ms must be > 0")
	}
	if c.Observability.RenderIntervalMS <= 0 {
		return errors.New("observability.render_interval_ms must be > 0")
	}

	switch c.Tracker.Kind {
	case "github":
	default:
		return fmt.Errorf("unsupported tracker.kind %q", c.Tracker.Kind)
	}
	if c.Tracker.Token == "" {
		return errors.New("tracker.token is required; set it directly or use $GITHUB_TOKEN")
	}
	if c.Tracker.Owner == "" {
		return errors.New("tracker.owner is required")
	}
	if c.Tracker.OwnerType != "user" && c.Tracker.OwnerType != "organization" {
		return fmt.Errorf("tracker.owner_type must be user or organization, got %q", c.Tracker.OwnerType)
	}
	if c.Tracker.ProjectNumber <= 0 {
		return errors.New("tracker.project_number is required")
	}
	return nil
}

func (c *Config) resolveAgentFallback() error {
	fallback := &c.Agent.Fallback
	if len(c.Agent.FallbackKinds) > 0 && strings.TrimSpace(fallback.Kind) == "" {
		fallback.Kind = c.Agent.FallbackKinds[0]
	}
	if len(c.Agent.FallbackOn) > 0 && len(fallback.On) == 0 {
		fallback.On = c.Agent.FallbackOn
	}

	fallback.Kind = strings.ToLower(strings.TrimSpace(fallback.Kind))
	fallback.Command = strings.TrimSpace(fallback.Command)
	fallback.On = normalizeFallbackReasons(fallback.On)

	configured := fallback.Kind != "" ||
		fallback.Command != "" ||
		len(fallback.On) > 0 ||
		len(c.Agent.FallbackKinds) > 0 ||
		len(c.Agent.FallbackOn) > 0
	enabled := c.Agent.Kind == "claude-code" || configured
	if fallback.Enabled != nil {
		enabled = *fallback.Enabled
	}
	fallback.Enabled = boolPtr(enabled)
	if !enabled {
		fallback.Kind = ""
		fallback.Command = ""
		fallback.On = nil
		return nil
	}

	if fallback.Kind == "" && c.Agent.Kind == "claude-code" {
		fallback.Kind = "codex"
	}
	switch fallback.Kind {
	case "codex", "claude-code":
	default:
		return fmt.Errorf("agent.fallback.kind must be \"codex\" or \"claude-code\", got %q", fallback.Kind)
	}
	if fallback.Kind == c.Agent.Kind {
		return fmt.Errorf("agent.fallback.kind must differ from agent.kind, got %q", fallback.Kind)
	}
	if len(fallback.On) == 0 && c.Agent.Kind == "claude-code" && fallback.Kind == "codex" {
		fallback.On = []string{"claude_limit"}
	}
	for _, reason := range fallback.On {
		if reason != "claude_limit" {
			return fmt.Errorf("agent.fallback.on supports only \"claude_limit\", got %q", reason)
		}
	}
	if len(fallback.On) == 0 {
		return errors.New("agent.fallback.on must include at least one reason")
	}
	if fallback.Command == "" {
		fallback.Command = defaultAgentCommand(fallback.Kind)
	}
	return nil
}

func defaultAgentCommand(kind string) string {
	switch kind {
	case "codex":
		return `mkdir -p .tmp
TMPDIR="$PWD/.tmp" TMP="$PWD/.tmp" TEMP="$PWD/.tmp" codex exec --sandbox workspace-write --skip-git-repo-check < "$SYMPHONY_PROMPT_FILE"`
	case "claude-code":
		return `cat "$SYMPHONY_PROMPT_FILE" | claude -p --dangerously-skip-permissions`
	default:
		return ""
	}
}

func normalizeFallbackReasons(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
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

func boolPtr(value bool) *bool {
	return &value
}

func deriveRestEndpoint(graphQLEndpoint string) string {
	endpoint := strings.TrimRight(strings.TrimSpace(graphQLEndpoint), "/")
	if endpoint == "" || endpoint == "https://api.github.com/graphql" {
		return "https://api.github.com"
	}
	return strings.TrimSuffix(endpoint, "/graphql")
}

func normalizeList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
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

func trimList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func (c Config) PollInterval() time.Duration {
	return time.Duration(c.Polling.IntervalMS) * time.Millisecond
}

func (c Config) HookTimeout() time.Duration {
	return time.Duration(c.Hooks.TimeoutMS) * time.Millisecond
}

func (c Config) TurnTimeout() time.Duration {
	return time.Duration(c.Agent.TurnTimeoutMS) * time.Millisecond
}

func (c Config) LoopMonitorInterval() time.Duration {
	return time.Duration(c.LoopMonitor.IntervalMS) * time.Millisecond
}

func (c Config) LoopMonitorMaxRuntime() time.Duration {
	return time.Duration(c.LoopMonitor.MaxRuntimeMS) * time.Millisecond
}

func (c Config) MaxRetryBackoff() time.Duration {
	return time.Duration(c.Agent.MaxRetryBackoffMS) * time.Millisecond
}

func resolveEnv(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "$") && len(value) > 1 {
		return os.Getenv(strings.TrimPrefix(value, "$"))
	}
	return os.ExpandEnv(value)
}

func expandPath(path string) string {
	if path == "" {
		return path
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
