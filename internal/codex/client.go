// Package codex runs one Companion step on a fresh Codex app-server thread.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	provider          = "openai"
	permissionProfile = "companion-workspace"
	workspaceDir      = "/workspace"
	codexHome         = "/home/node/.codex"
)

// Client uses the Codex 0.172.0 app-server protocol. Use a private runner URL.
// Set CODEX_WS_TOKEN_FILE if the listener requires a bearer token.
type Client struct {
	URL   string
	Model string
}

type params = map[string]any

func (c Client) restrictions() params {
	return params{
		"model": c.Model, "approvalPolicy": "never", "approvalsReviewer": "user",
		"permissions": permissionProfile, "cwd": workspaceDir, "runtimeWorkspaceRoots": []string{workspaceDir},
	}
}

// Run sends prompt to a fresh ephemeral thread and returns its final assistant
// message. The runner handles its native tools. Run does not retry a turn.
func (c Client) Run(ctx context.Context, prompt string) (string, error) {
	if c.Model == "" || strings.TrimSpace(c.Model) != c.Model {
		return "", errors.New("codex: specify an exact model ID")
	}
	s, err := connect(ctx, c.URL)
	if err != nil {
		return "", err
	}
	defer s.close()

	var init struct {
		CodexHome  string `json:"codexHome"`
		PlatformOS string `json:"platformOs"`
	}
	if err := s.request(ctx, "initialize", params{
		"clientInfo":   params{"name": "companion", "title": "Companion", "version": "0.1.0"},
		"capabilities": params{"experimentalApi": true, "requestAttestation": false},
	}, &init, nil); err != nil {
		return "", err
	}
	if init.CodexHome != codexHome || init.PlatformOS != "linux" {
		return "", errors.New("codex: unexpected runner platform or CODEX_HOME")
	}
	if err := s.send(ctx, message{Method: "initialized"}); err != nil {
		return "", err
	}
	if err := c.checkCatalog(ctx, s); err != nil {
		return "", err
	}

	start := c.restrictions()
	start["ephemeral"] = true
	start["modelProvider"] = provider
	start["allowProviderModelFallback"] = false
	// Responses Lite uses the native standalone search extension.
	start["config"] = params{"web_search": "live", "features": params{"standalone_web_search": true}}
	var started struct {
		Thread struct {
			ID           string  `json:"id"`
			Ephemeral    bool    `json:"ephemeral"`
			Model        *string `json:"model"`
			Environments []struct {
				ID    string   `json:"environmentId"`
				CWD   string   `json:"cwd"`
				Roots []string `json:"runtimeWorkspaceRoots"`
			} `json:"environments"`
		} `json:"thread"`
		CWD            string `json:"cwd"`
		Model          string `json:"model"`
		ModelProvider  string `json:"modelProvider"`
		ApprovalPolicy string `json:"approvalPolicy"`
		Sandbox        struct {
			Type                string   `json:"type"`
			NetworkAccess       *bool    `json:"networkAccess"`
			WritableRoots       []string `json:"writableRoots"`
			ExcludeTmpdirEnvVar bool     `json:"excludeTmpdirEnvVar"`
			ExcludeSlashTmp     bool     `json:"excludeSlashTmp"`
		} `json:"sandbox"`
		Profile struct {
			ID      string  `json:"id"`
			Extends *string `json:"extends"`
		} `json:"activePermissionProfile"`
	}
	if err := s.request(ctx, "thread/start", start, &started, nil); err != nil {
		return "", err
	}
	if started.Model != c.Model || (started.Thread.Model != nil && *started.Thread.Model != c.Model) || started.ModelProvider != provider {
		return "", fmt.Errorf("codex: app-server did not select configured model %q", c.Model)
	}
	if started.Thread.ID == "" || !started.Thread.Ephemeral || len(started.Thread.Environments) != 1 ||
		started.Thread.Environments[0].ID != "local" || started.Thread.Environments[0].CWD != workspaceDir ||
		len(started.Thread.Environments[0].Roots) != 1 || started.Thread.Environments[0].Roots[0] != workspaceDir ||
		started.CWD != workspaceDir || started.ApprovalPolicy != "never" || started.Profile.ID != permissionProfile || started.Profile.Extends != nil ||
		started.Sandbox.Type != "workspaceWrite" || started.Sandbox.NetworkAccess == nil || *started.Sandbox.NetworkAccess ||
		!started.Sandbox.ExcludeSlashTmp || !started.Sandbox.ExcludeTmpdirEnvVar ||
		(len(started.Sandbox.WritableRoots) != 0 && (len(started.Sandbox.WritableRoots) != 1 || started.Sandbox.WritableRoots[0] != workspaceDir)) {
		return "", errors.New("codex: app-server did not confirm restricted ephemeral thread settings")
	}

	t := turnRun{s: s, threadID: started.Thread.ID, model: c.Model}
	turn := c.restrictions()
	turn["threadId"] = t.threadID
	turn["input"] = []params{{"type": "text", "text": prompt, "text_elements": []any{}}}
	return t.run(ctx, turn)
}

func (c Client) checkCatalog(ctx context.Context, s *session) error {
	p := params{"modelProvider": provider, "includeHidden": true}
	seen := make(map[string]bool)
	for {
		var page struct {
			Data []struct {
				Model string `json:"model"`
			} `json:"data"`
			Next *string `json:"nextCursor"`
		}
		if err := s.request(ctx, "model/list", p, &page, nil); err != nil {
			return err
		}
		for _, entry := range page.Data {
			if entry.Model == c.Model {
				return nil
			}
		}
		if page.Next == nil {
			return fmt.Errorf("codex: configured model %q is not in the app-server catalog", c.Model)
		}
		if *page.Next == "" || seen[*page.Next] {
			return errors.New("codex: invalid model catalog cursor")
		}
		seen[*page.Next] = true
		p["cursor"] = *page.Next
	}
}

type turnRun struct {
	s                *session
	threadID, turnID string
	model            string
	final, last      string
	done             bool
}

type threadItem struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Phase string `json:"phase"`
}

type turnState struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Items  []threadItem `json:"items"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (t *turnRun) run(ctx context.Context, p params) (string, error) {
	defer func() {
		if !t.done && t.turnID != "" {
			t.interrupt(ctx)
		}
	}()
	var started struct {
		Turn turnState `json:"turn"`
	}
	var pending []message
	size := 0
	if err := t.s.request(ctx, "turn/start", p, &started, func(m message) error {
		if !turnEvent(m) {
			if len(m.ID) != 0 {
				return t.s.reject(ctx, m, -32601, "server request not allowed")
			}
			return nil
		}
		// Match early events after the server returns the turn ID.
		size += len(m.Params)
		if len(pending) >= 256 || size > maxMessageBytes {
			return errors.New("codex: too many events before turn/start reply")
		}
		pending = append(pending, m)
		return nil
	}); err != nil {
		return "", err
	}
	t.turnID = started.Turn.ID
	if t.turnID == "" {
		return "", errors.New("codex: missing turn ID")
	}
	if err := t.updateTurn(started.Turn); err != nil {
		return "", err
	}
	for _, m := range pending {
		if err := t.dispatch(ctx, m); err != nil {
			return "", err
		}
	}
	for !t.done {
		m, err := t.s.receive(ctx)
		if err != nil {
			return "", err
		}
		if err := t.dispatch(ctx, m); err != nil {
			return "", err
		}
	}
	if t.final != "" {
		return t.final, nil
	}
	if t.last != "" {
		return t.last, nil
	}
	return "", errors.New("codex: turn completed without an assistant answer")
}

func turnEvent(m message) bool {
	if len(m.ID) != 0 {
		return false
	}
	switch m.Method {
	case "item/completed", "turn/completed", "model/rerouted", "thread/settings/updated":
		return true
	}
	return false
}

func (t *turnRun) dispatch(ctx context.Context, m message) error {
	if m.Method == "" {
		return nil
	}
	if len(m.ID) != 0 {
		return t.s.reject(ctx, m, -32601, "server request not allowed")
	}
	if !turnEvent(m) {
		return nil
	}
	var e struct {
		ThreadID string     `json:"threadId"`
		TurnID   string     `json:"turnId"`
		Turn     turnState  `json:"turn"`
		Item     threadItem `json:"item"`
		Settings struct {
			Model string `json:"model"`
		} `json:"threadSettings"`
	}
	if json.Unmarshal(m.Params, &e) != nil {
		return fmt.Errorf("codex: invalid %s notification", m.Method)
	}
	if e.ThreadID != t.threadID {
		return nil
	}
	if m.Method == "turn/completed" {
		if e.Turn.ID == t.turnID {
			return t.updateTurn(e.Turn)
		}
		return nil
	}
	if m.Method == "thread/settings/updated" {
		if e.Settings.Model != t.model {
			return errors.New("codex: thread changed from the configured model")
		}
		return nil
	}
	if e.TurnID != t.turnID {
		return nil
	}
	switch m.Method {
	case "model/rerouted":
		return errors.New("codex: turn was rerouted from the configured model")
	case "item/completed":
		t.item(e.Item)
	}
	return nil
}

func (t *turnRun) updateTurn(state turnState) error {
	switch state.Status {
	case "inProgress":
	case "completed":
		t.done = true
	case "failed", "interrupted":
		t.done = true
		if state.Error != nil {
			return fmt.Errorf("codex: turn %s: %s", state.Status, state.Error.Message)
		}
		return fmt.Errorf("codex: turn %s", state.Status)
	default:
		return fmt.Errorf("codex: unexpected turn status %q", state.Status)
	}
	if t.done {
		for _, item := range state.Items {
			t.item(item)
		}
	}
	return nil
}

func (t *turnRun) item(item threadItem) {
	if item.Type != "agentMessage" || strings.TrimSpace(item.Text) == "" {
		return
	}
	switch item.Phase {
	case "final_answer":
		t.final = item.Text
	case "": // Older models may omit phase.
		t.last = item.Text
	}
}

func (t *turnRun) interrupt(ctx context.Context) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	var out params
	_ = t.s.request(cleanup, "turn/interrupt", params{"threadId": t.threadID, "turnId": t.turnID}, &out, nil)
}
