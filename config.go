package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
)

type config struct {
	Model         string
	ThinkingLevel string
	Token         string
	ChatID        int64
	TelegramURL   string
	CodexURL      string
	StateDir      string
}

var (
	botTokenPattern = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,100}$`)
	modelPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

func loadConfig() (config, error) {
	c := config{
		TelegramURL: envDefault("COMPANION_TELEGRAM_URL", "https://api.telegram.org"),
		CodexURL:    envDefault("COMPANION_CODEX_URL", "ws://127.0.0.1:4500"),
		StateDir:    envDefault("COMPANION_STATE_DIR", "./state"),
	}
	data, err := readSmallFile(configPath(), 4096)
	if err != nil {
		return config{}, fmt.Errorf("read Companion configuration: %w", err)
	}
	file := struct {
		Model         string  `json:"model"`
		ThinkingLevel *string `json:"thinking-level"`
		Token         *string `json:"tg-bot-token"`
		ChatID        *int64  `json:"tg-chat-id"`
	}{ThinkingLevel: new("medium")} // An explicit JSON null clears this default.
	if json.Unmarshal(data, &file) != nil {
		return config{}, errors.New("invalid Companion configuration; see companion.example.json")
	}
	if !modelPattern.MatchString(file.Model) {
		return config{}, errors.New("model must contain a valid model ID")
	}
	if file.ThinkingLevel != nil {
		level := *file.ThinkingLevel
		if level == "" || strings.TrimSpace(level) != level {
			return config{}, errors.New("thinking-level must be a non-empty string with no leading or trailing whitespace, or null")
		}
		c.ThinkingLevel = level
	}
	if file.Token == nil {
		return config{}, errors.New("tg-bot-token is required")
	}
	if !botTokenPattern.MatchString(*file.Token) {
		return config{}, errors.New("invalid tg-bot-token")
	}
	if file.ChatID == nil {
		return config{}, errors.New("tg-chat-id is required")
	}
	if *file.ChatID <= 0 {
		return config{}, errors.New("tg-chat-id must be a positive integer for a private chat")
	}
	c.Model, c.Token, c.ChatID = file.Model, *file.Token, *file.ChatID

	if !validEndpoint(c.TelegramURL, "https", "http") {
		return config{}, errors.New("COMPANION_TELEGRAM_URL must be an HTTP(S) base URL without credentials, query or fragment")
	}
	if !validEndpoint(c.CodexURL, "ws", "wss") {
		return config{}, errors.New("COMPANION_CODEX_URL must be a WS(S) URL without credentials, query or fragment")
	}
	return c, nil
}

func validEndpoint(raw, a, b string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == a || u.Scheme == b) && u.Hostname() != "" &&
		u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func configPath() string { return envDefault("COMPANION_CONFIG", "companion.json") }

func envDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func readSmallFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(data)) > max {
		err = errors.New("file exceeds the size limit")
	}
	return data, err
}
