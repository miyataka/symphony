package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/miyataka/symphony/go/internal/githubtracker"
	"github.com/miyataka/symphony/go/internal/orchestrator"
	"github.com/miyataka/symphony/go/internal/workflow"
)

func main() {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "run":
			run(os.Args[2:])
			return
		case "setup-github-project":
			setupGitHubProject(os.Args[2:])
			return
		case "help", "-h", "--help":
			printUsage()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
			printUsage()
			os.Exit(2)
		}
	}
	run(os.Args[1:])
}

func run(args []string) {
	var workflowPath string
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.StringVar(&workflowPath, "workflow", "WORKFLOW.md", "path to workflow markdown file")
	fs.Parse(args)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	def, cfg, err := loadWorkflow(workflowPath)
	if err != nil {
		logger.Error("failed to load workflow", "path", workflowPath, "error", err)
		os.Exit(1)
	}

	tracker, err := githubtracker.New(cfg.Tracker)
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

	if err := service.Run(ctx); err != nil && ctx.Err() == nil {
		logger.Error("symphony stopped with error", "error", err)
		os.Exit(1)
	}
}

func setupGitHubProject(args []string) {
	var workflowPath string
	fs := flag.NewFlagSet("setup-github-project", flag.ExitOnError)
	fs.StringVar(&workflowPath, "workflow", "WORKFLOW.md", "path to workflow markdown file")
	fs.Parse(args)

	cfg, err := loadWorkflowForSetup(workflowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load workflow %q: %v\n", workflowPath, err)
		os.Exit(1)
	}
	printGitHubProjectSetup(cfg)
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

func printGitHubProjectSetup(cfg workflow.Config) {
	owner := cfg.Tracker.Owner
	if cfg.Tracker.OwnerType == "user" && owner == "@me" {
		owner = "@me"
	}
	states := append([]string{}, cfg.Tracker.ActiveStates...)
	states = append(states, cfg.Tracker.MonitorStates...)
	states = append(states, cfg.Tracker.TerminalStates...)
	states = uniqueStrings(states)

	fmt.Println("# GitHub Project setup")
	fmt.Println("gh auth refresh -s project")
	fmt.Printf("gh project view %d --owner %q --format json\n", cfg.Tracker.ProjectNumber, owner)
	fmt.Printf("gh project field-list %d --owner %q\n", cfg.Tracker.ProjectNumber, owner)
	fmt.Println()
	fmt.Println("# If the Status field does not exist, create it with the configured workflow states:")
	fmt.Printf("gh project field-create %d --owner %q --name %q --data-type SINGLE_SELECT --single-select-options %q\n",
		cfg.Tracker.ProjectNumber,
		owner,
		cfg.Tracker.StatusField,
		strings.Join(states, ","),
	)
	if cfg.Tracker.PriorityField != "" {
		fmt.Println()
		fmt.Println("# Optional priority field:")
		fmt.Printf("gh project field-create %d --owner %q --name %q --data-type SINGLE_SELECT --single-select-options %q\n",
			cfg.Tracker.ProjectNumber,
			owner,
			cfg.Tracker.PriorityField,
			"Urgent,High,Medium,Low",
		)
	}
	if len(cfg.Tracker.AllowedRepositories) > 0 {
		fmt.Println()
		fmt.Println("# Add issues from the allowed repositories as project items when needed:")
		for _, repo := range cfg.Tracker.AllowedRepositories {
			fmt.Printf("# gh issue list --repo %s --state open --json url --jq '.[].url' | xargs -I{} gh project item-add %d --owner %q --url {}\n", repo, cfg.Tracker.ProjectNumber, owner)
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
  symphony [--workflow WORKFLOW.md]
  symphony run [--workflow WORKFLOW.md]
  symphony setup-github-project [--workflow WORKFLOW.md]
`)
}
