package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	const token = "123456:example_token_not_a_real_credential"
	const prefix = `{"model":"gpt-example","tg-bot-token":"`
	setConfigFile(t, prefix+token+`","tg-chat-id":123456}`)
	t.Setenv("COMPANION_CODEX_URL", "ws://localhost:4500")
	t.Setenv("COMPANION_TELEGRAM_URL", "http://localhost:4501")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != token || cfg.Model != "gpt-example" || cfg.ChatID != 123456 {
		t.Fatal("configuration did not match the input files")
	}
	setConfigFile(t, prefix+token+`","tg-chat-id":-123456}`)
	if _, err = loadConfig(); err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("expected a chat ID validation error without the token")
	}
	setConfigFile(t, prefix+`plausible-secret-but-invalid","tg-chat-id":123456}`)
	if _, err = loadConfig(); err == nil || strings.Contains(err.Error(), "plausible-secret-but-invalid") {
		t.Fatal("expected a token validation error without the input")
	}
}

func TestPrepareDirectoriesRejectsExposedStateAndSecrets(t *testing.T) {
	root := t.TempDir()
	cfg := config{StateDir: filepath.Join(root, "workspace/state"), Workspace: filepath.Join(root, "workspace")}
	if err := prepareDirectories(cfg); err == nil {
		t.Fatal("state inside workspace permitted")
	}
	cfg.StateDir = filepath.Join(root, "independent-state")
	configPath := filepath.Join(cfg.Workspace, "secret")
	if err := os.WriteFile(configPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked-secret")
	if err := os.Symlink(configPath, linked); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPANION_CONFIG", linked)
	if err := prepareDirectories(cfg); err == nil {
		t.Fatal("a private config inside workspace permitted")
	}
	setConfigFile(t, `{}`)
	if err := prepareDirectories(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestSuperviseCancelsOnLoopFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want := errors.New("save failed")
	err := supervise(ctx, func(context.Context) error { return want }, func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})
	if !errors.Is(err, want) {
		t.Fatalf("got %v", err)
	}
}

func TestSuperviseCleanShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := supervise(ctx, func(ctx context.Context) error { return ctx.Err() }); err != nil {
		t.Fatalf("clean shutdown returned %v", err)
	}
}

func setConfigFile(t *testing.T, contents string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "setting")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMPANION_CONFIG", path)
}
