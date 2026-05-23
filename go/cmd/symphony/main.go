package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/miyataka/symphony/go/internal/githubtracker"
	"github.com/miyataka/symphony/go/internal/orchestrator"
	"github.com/miyataka/symphony/go/internal/statusdashboard"
	"github.com/miyataka/symphony/go/internal/workflow"
)

func main() {
	command, args, help := parseCommand(os.Args[1:])
	if help {
		printUsage()
		return
	}
	switch command {
	case "run":
		run(args)
	case "setup-github-project":
		setupGitHubProject(args)
	}
}

func run(args []string) {
	workflowPath, err := parseWorkflowPath("run", args)
	if err != nil {
		handleArgumentError(err)
	}

	logger := newLogger(false, "info", os.Stdout)

	def, cfg, err := loadWorkflow(workflowPath)
	if err != nil {
		logger.Error("failed to load workflow", "path", workflowPath, "error", err)
		os.Exit(1)
	}

	logWriter, closeLog, err := openRunLogWriter(cfg.Observability.LogFile, cfg.Observability.DashboardEnabled)
	if err != nil {
		logger.Error("failed to open observability log file", "path", cfg.Observability.LogFile, "error", err)
		os.Exit(1)
	}
	defer closeLog()
	logger = newLogger(cfg.Observability.LogJSON, cfg.Observability.LogLevel, logWriter)
	if cfg.Observability.LogFile != "" {
		logger.Info("observability log file enabled", "path", cfg.Observability.LogFile)
	}

	tracker, err := githubtracker.NewWithLogger(cfg.Tracker, logger.With("component", "githubtracker"))
	if err != nil {
		logger.Error("failed to initialize tracker", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	service := orchestrator.New(orchestrator.Options{
		Config:         cfg,
		PromptTemplate: def.PromptTemplate,
		Tracker:        tracker,
		Logger:         logger,
	})

	if cfg.Observability.DashboardEnabled {
		go func() {
			runner := statusdashboard.Runner{
				Writer:          os.Stdout,
				RefreshInterval: time.Duration(cfg.Observability.RefreshMS) * time.Millisecond,
				RenderInterval:  time.Duration(cfg.Observability.RenderIntervalMS) * time.Millisecond,
				Options:         statusdashboard.Options{Width: dashboardWidth(), Color: true},
				Snapshot:        service.Snapshot,
			}
			if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("status dashboard stopped", "error", err)
			}
		}()
	}

	if err := service.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("symphony stopped with error", "error", err)
		os.Exit(1)
	}
}

func newLogger(logJSON bool, levelName string, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slogLevel(levelName)}
	if logJSON {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func openLogWriter(path string) (io.Writer, func() error, error) {
	return openRunLogWriter(path, false)
}

func openRunLogWriter(path string, dashboardEnabled bool) (io.Writer, func() error, error) {
	console := io.Writer(os.Stdout)
	if dashboardEnabled {
		console = os.Stderr
	}
	if strings.TrimSpace(path) == "" {
		return console, func() error { return nil }, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("create log directory %q: %w", dir, err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file %q: %w", path, err)
	}
	return io.MultiWriter(console, file), file.Close, nil
}

func dashboardWidth() int {
	columns, err := strconv.Atoi(strings.TrimSpace(os.Getenv("COLUMNS")))
	if err == nil && columns > 0 {
		return columns
	}
	return 100
}

func slogLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func setupGitHubProject(args []string) {
	workflowPath, err := parseWorkflowPath("setup-github-project", args)
	if err != nil {
		handleArgumentError(err)
	}

	cfg, err := loadWorkflowForSetup(workflowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load workflow %q: %v\n", workflowPath, err)
		os.Exit(1)
	}
	printGitHubProjectSetup(os.Stdout, cfg)
}

func parseCommand(args []string) (string, []string, bool) {
	if len(args) == 0 {
		return "run", args, false
	}
	switch args[0] {
	case "run":
		return "run", args[1:], false
	case "setup-github-project":
		return "setup-github-project", args[1:], false
	case "help", "-h", "--help":
		return "", nil, true
	default:
		return "run", args, false
	}
}

func parseWorkflowPath(command string, args []string) (string, error) {
	var workflowPath string
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&workflowPath, "workflow", "WORKFLOW.md", "path to workflow markdown file")
	if err := fs.Parse(args); err != nil {
		return "", err
	}

	workflowFlagSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "workflow" {
			workflowFlagSet = true
		}
	})
	positionals := fs.Args()
	switch len(positionals) {
	case 0:
		return workflowPath, nil
	case 1:
		if workflowFlagSet {
			return "", fmt.Errorf("workflow path specified by both --workflow and positional argument")
		}
		return positionals[0], nil
	default:
		return "", fmt.Errorf("expected at most one workflow path, got %d", len(positionals))
	}
}

func handleArgumentError(err error) {
	if errors.Is(err, flag.ErrHelp) {
		printUsage()
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "%v\n\n", err)
	printUsage()
	os.Exit(2)
}

func loadWorkflow(path string) (workflow.Definition, workflow.Config, error) {
	def, err := workflow.LoadFile(path)
	if err != nil {
		return workflow.Definition{}, workflow.Config{}, err
	}
	cfg, err := workflow.ParseConfig(def.Config)
	if err != nil {
		return workflow.Definition{}, workflow.Config{}, err
	}
	return def, cfg, nil
}

func loadWorkflowForSetup(path string) (workflow.Config, error) {
	def, err := workflow.LoadFile(path)
	if err != nil {
		return workflow.Config{}, err
	}
	raw := def.Config
	if raw == nil {
		raw = map[string]any{}
	}
	trackerRaw, ok := raw["tracker"].(map[string]any)
	if !ok {
		trackerRaw = map[string]any{}
		raw["tracker"] = trackerRaw
	}
	trackerRaw["token"] = "setup-token-placeholder"
	return workflow.ParseConfig(raw)
}

func printGitHubProjectSetup(w io.Writer, cfg workflow.Config) {
	owner := cfg.Tracker.Owner
	if cfg.Tracker.OwnerType == "user" && owner == "@me" {
		owner = "@me"
	}
	states := append([]string{}, cfg.Tracker.BacklogStates...)
	states = append(states, cfg.Tracker.ActiveStates...)
	states = append(states, cfg.Tracker.MonitorStates...)
	states = append(states, cfg.Tracker.TerminalStates...)
	states = uniqueStrings(states)

	fmt.Fprintln(w, "# GitHub Project setup")
	fmt.Fprintln(w, "gh auth refresh -s project")
	fmt.Fprintf(w, "gh project view %d --owner %q --format json\n", cfg.Tracker.ProjectNumber, owner)
	fmt.Fprintf(w, "gh project field-list %d --owner %q\n", cfg.Tracker.ProjectNumber, owner)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "# If the Status field does not exist, create it with the configured workflow states:")
	fmt.Fprintf(w, "gh project field-create %d --owner %q --name %q --data-type SINGLE_SELECT --single-select-options %q\n",
		cfg.Tracker.ProjectNumber,
		owner,
		cfg.Tracker.StatusField,
		strings.Join(states, ","),
	)
	if cfg.Tracker.PriorityField != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "# Optional priority field:")
		fmt.Fprintf(w, "gh project field-create %d --owner %q --name %q --data-type SINGLE_SELECT --single-select-options %q\n",
			cfg.Tracker.ProjectNumber,
			owner,
			cfg.Tracker.PriorityField,
			"Urgent,High,Medium,Low",
		)
	}
	if len(cfg.Tracker.AllowedRepositories) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "# Add issues from the allowed repositories as project items when needed:")
		for _, repo := range cfg.Tracker.AllowedRepositories {
			fmt.Fprintf(w, "# gh issue list --repo %s --state open --json url --jq '.[].url' | xargs -I{} gh project item-add %d --owner %q --url {}\n", repo, cfg.Tracker.ProjectNumber, owner)
		}
	}
}

func uniqueStrings(values []string) []string {
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

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  symphony [--workflow WORKFLOW.md] [WORKFLOW.md]
  symphony run [--workflow WORKFLOW.md] [WORKFLOW.md]
  symphony setup-github-project [--workflow WORKFLOW.md] [WORKFLOW.md]
`)
}
