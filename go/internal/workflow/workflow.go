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

type AgentConfig struct {
	Command                    string         `yaml:"command"`
	MaxConcurrentAgents        int            `yaml:"max_concurrent_agents"`
	MaxConcurrentAgentsByState map[string]int `yaml:"max_concurrent_agents_by_state"`
	MaxTurns                   int            `yaml:"max_turns"`
	MaxRetryBackoffMS          int            `yaml:"max_retry_backoff_ms"`
	TurnTimeoutMS              int            `yaml:"turn_timeout_ms"`
}

type ObservabilityConfig struct {
	LogJSON  bool   `yaml:"log_json"`
	LogLevel string `yaml:"log_level"`
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
			WorkpadMarker:         "## Codex Workpad",
			ReadIssueDependencies: true,
			ActiveStates:          []string{"Todo", "In Progress", "Rework"},
			MonitorStates:         []string{"Human Review", "Merging"},
			TerminalStates:        []string{"Done", "Closed", "Cancelled", "Canceled", "Duplicate"},
		},
		PullRequest: PullRequestConfig{
			MergeMethod:          "SQUASH",
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
	}
}

func (c *Config) Resolve() error {
	c.Tracker.Token = resolveEnv(c.Tracker.Token)
	c.Workspace.Root = expandPath(resolveEnv(c.Workspace.Root))
	c.Tracker.Kind = strings.ToLower(strings.TrimSpace(c.Tracker.Kind))
	c.Tracker.OwnerType = strings.ToLower(strings.TrimSpace(c.Tracker.OwnerType))
	c.Tracker.AllowedRepositories = normalizeList(c.Tracker.AllowedRepositories)
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
	if c.Tracker.WorkpadMarker == "" {
		c.Tracker.WorkpadMarker = "## Codex Workpad"
	}
	if len(c.Tracker.MonitorStates) == 0 && c.Tracker.HandoffState != "" {
		c.Tracker.MonitorStates = []string{c.Tracker.HandoffState, c.Tracker.MergingState}
	}
	if c.Polling.IntervalMS <= 0 {
		c.Polling.IntervalMS = int((30 * time.Second) / time.Millisecond)
	}
	c.PullRequest.MergeMethod = strings.ToUpper(strings.TrimSpace(c.PullRequest.MergeMethod))
	if c.PullRequest.MergeMethod == "" {
		c.PullRequest.MergeMethod = "SQUASH"
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
	c.Observability.LogLevel = strings.ToLower(strings.TrimSpace(c.Observability.LogLevel))
	if c.Observability.LogLevel == "" {
		c.Observability.LogLevel = "info"
	}
	switch c.Observability.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("observability.log_level must be debug, info, warn, or error, got %q", c.Observability.LogLevel)
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
