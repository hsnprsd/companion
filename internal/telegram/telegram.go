// Package telegram exchanges messages, reactions, and typing indicators in one private chat.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Client struct {
	url    string
	chatID int64
	http   *http.Client
}

func New(baseURL, token string, chatID int64) *Client {
	return &Client{
		url:    strings.TrimRight(baseURL, "/") + "/bot" + token + "/",
		chatID: chatID,
		http: &http.Client{
			Timeout: 40 * time.Second,
			// The request path contains the token. Do not follow redirects.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

type update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		ID   int64  `json:"message_id"`
		Text string `json:"text"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	} `json:"message"`
}

type APIError struct {
	code       int
	uncertain  bool
	retryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.code == 0 {
		return "Telegram connection or response failed"
	}
	return fmt.Sprintf("Telegram request failed (code %d)", e.code)
}

func (e *APIError) Uncertain() bool           { return e.uncertain }
func (e *APIError) RetryAfter() time.Duration { return e.retryAfter }
func (e *APIError) Permanent() bool {
	return e.code >= 400 && e.code < 500 && e.code != 408 && e.code != 429
}

func (c *Client) request(ctx context.Context, method string, params any, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return errors.New("encode Telegram request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid Telegram endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		// net/http errors can contain the full URL. Do not propagate them.
		return &APIError{uncertain: true}
	}
	defer res.Body.Close()

	var response struct {
		OK         bool            `json:"ok"`
		Code       int             `json:"error_code"`
		Result     json.RawMessage `json:"result"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&response)
	if err != nil || !response.OK || res.StatusCode != http.StatusOK {
		code := res.StatusCode
		if response.Code >= 400 && response.Code <= 599 {
			code = response.Code
		}
		return &APIError{code: code, uncertain: code == 200 || code == 408 || code >= 500,
			retryAfter: time.Duration(max(0, min(response.Parameters.RetryAfter, 600))) * time.Second}
	}
	if result != nil && json.Unmarshal(response.Result, result) != nil {
		return &APIError{uncertain: true}
	}
	return nil
}

// Poll accepts updates in order. Accept must save the cursor and any work it
// creates before returning. Ignored updates pass no message ID or text.
func (c *Client) Poll(ctx context.Context, offset func() int64, accept func(updateID, messageID int64, text string) error) error {
	backoff := time.Second
	for ctx.Err() == nil {
		var updates []update
		err := c.request(ctx, "getUpdates", map[string]any{
			"offset": offset(), "timeout": 25, "limit": 25, "allowed_updates": []string{"message"},
		}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			delay := backoff
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				if apiErr.Permanent() && apiErr.code != http.StatusConflict {
					return err
				}
				delay = max(delay, apiErr.retryAfter)
			}
			slog.Warn("Telegram polling failed", "error", err, "retry_in", delay)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		sort.SliceStable(updates, func(i, j int) bool { return updates[i].ID < updates[j].ID })
		for _, u := range updates {
			if ctx.Err() != nil {
				return nil
			}
			if u.ID <= 0 || u.ID < offset() {
				continue
			}
			var messageID int64
			var text string
			if u.Message != nil && u.Message.Chat.ID == c.chatID && u.Message.Chat.Type == "private" && u.Message.Text != "" {
				messageID, text = max(0, u.Message.ID), u.Message.Text
			}
			if err := accept(u.ID, messageID, text); err != nil {
				return fmt.Errorf("save Telegram update: %w", err)
			}
		}
	}
	return nil
}

// Send sends one plain-text message. The durable outbox splits longer text
// before calling Send so it can track delivery of each part.
func (c *Client) Send(ctx context.Context, text string) error {
	var result struct {
		MessageID *int64 `json:"message_id"`
	}
	if err := c.request(ctx, "sendMessage", map[string]any{
		"chat_id": c.chatID, "text": text, "link_preview_options": map[string]bool{"is_disabled": true},
	}, &result); err != nil {
		return err
	}
	if result.MessageID == nil {
		return &APIError{uncertain: true}
	}
	return nil
}

// Typing shows a short-lived typing indicator in the configured chat.
func (c *Client) Typing(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return c.requestTrue(ctx, "sendChatAction", map[string]any{"chat_id": c.chatID, "action": "typing"})
}

// React sets the bot's reaction to one message in the configured chat.
func (c *Client) React(ctx context.Context, messageID int64, emoji string) error {
	if messageID <= 0 || emoji == "" {
		return &APIError{code: http.StatusBadRequest}
	}
	return c.requestTrue(ctx, "setMessageReaction", map[string]any{
		"chat_id": c.chatID, "message_id": messageID,
		"reaction": []map[string]string{{"type": "emoji", "emoji": emoji}},
	})
}

func (c *Client) requestTrue(ctx context.Context, method string, params any) error {
	var result bool
	if err := c.request(ctx, method, params, &result); err != nil {
		return err
	}
	if !result {
		return &APIError{uncertain: true}
	}
	return nil
}
