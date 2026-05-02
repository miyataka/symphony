package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/miyataka/symphony/go/internal/githubtracker"
	"github.com/miyataka/symphony/go/internal/orchestrator"
	"github.com/miyataka/symphony/go/internal/workflow"
)

func main() {
	var workflowPath string
	flag.StringVar(&workflowPath, "workflow", "WORKFLOW.md", "path to workflow markdown file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	def, err := workflow.LoadFile(workflowPath)
	if err != nil {
		logger.Error("failed to load workflow", "path", workflowPath, "error", err)
		os.Exit(1)
	}

	cfg, err := workflow.ParseConfig(def.Config)
	if err != nil {
		logger.Error("failed to parse workflow config", "error", err)
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
