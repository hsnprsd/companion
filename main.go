package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"companion/internal/codex"
	"companion/internal/engine"
	"companion/internal/telegram"
	"companion/internal/workspace"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("Companion stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if err := prepareDirectories(cfg); err != nil {
		return err
	}
	files, err := workspace.Open(cfg.Workspace, cfg.ExternalDir)
	if err != nil {
		return err
	}
	defer files.Close()
	app, err := engine.Open(cfg.StateDir, codex.Client{URL: cfg.CodexURL, Model: cfg.Model}, files)
	if err != nil {
		return fmt.Errorf("open task state: %w", err)
	}
	defer app.Close()

	bot := telegram.New(cfg.TelegramURL, cfg.Token, cfg.ChatID)
	return supervise(ctx,
		app.Work,
		func(ctx context.Context) error { return app.Deliver(ctx, bot.Send) },
		func(ctx context.Context) error { return bot.Poll(ctx, app.Offset, app.Accept) },
	)
}

func supervise(parent context.Context, loops ...func(context.Context) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	results := make(chan error, len(loops))
	for _, loop := range loops {
		go func() { results <- loop(ctx) }()
	}

	var first error
	remaining := len(loops)
	select {
	case first = <-results:
		remaining--
		if parent.Err() != nil && errors.Is(first, context.Canceled) {
			first = nil
		} else if parent.Err() == nil && first == nil {
			first = errors.New("service loop stopped unexpectedly")
		}
	case <-parent.Done():
	}
	cancel()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for range remaining {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				first = errors.Join(first, err)
			}
		case <-deadline.C:
			return errors.Join(first, errors.New("service shutdown deadline exceeded"))
		}
	}
	return first
}

func prepareDirectories(cfg config) error {
	for _, dir := range []string{cfg.StateDir, cfg.Workspace} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	workspace, err := resolved(cfg.Workspace)
	if err != nil {
		return err
	}
	state, err := resolved(cfg.StateDir)
	if err != nil {
		return err
	}
	if containsDir(workspace, state) || containsDir(state, workspace) {
		return errors.New("workspace and service state must be separate directories")
	}
	readable := []string{workspace}
	if cfg.ExternalDir != "" {
		external, err := resolved(cfg.ExternalDir)
		if err != nil {
			return err
		}
		if containsDir(external, state) || containsDir(state, external) ||
			containsDir(external, workspace) || containsDir(workspace, external) {
			return errors.New("external mounts must be separate from the workspace and service state")
		}
		readable = append(readable, external)
	}
	file, err := resolved(configPath())
	if err != nil {
		return err
	}
	for _, dir := range readable {
		if containsDir(dir, file) {
			return errors.New("private configuration must not be inside readable file roots")
		}
	}
	return nil
}

func resolved(name string) (string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func containsDir(dir, name string) bool {
	rel, err := filepath.Rel(dir, name)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
