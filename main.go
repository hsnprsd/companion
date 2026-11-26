package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"companion/internal/codex"
	"companion/internal/engine"
	"companion/internal/telegram"
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
	app, err := engine.Open(cfg.StateDir, codex.Client{URL: cfg.CodexURL, Model: cfg.Model, ThinkingLevel: cfg.ThinkingLevel})
	if err != nil {
		return fmt.Errorf("open chat state: %w", err)
	}
	defer app.Close()

	bot := telegram.New(cfg.TelegramURL, cfg.Token, cfg.ChatID)
	return supervise(ctx,
		app.Work,
		func(ctx context.Context) error { return app.Deliver(ctx, bot.Send, bot.React) },
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
