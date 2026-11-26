package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const model = "fixture-model-v1"

type fields = map[string]any

type rpc struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func (f *fixture) codex(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		f.problem("WebSocket upgrade failed: %v", err)
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(2 << 20)
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if err := f.session(ctx, ws); err != nil {
		f.problem("Codex fixture: %v", err)
	}
}

func (f *fixture) session(ctx context.Context, ws *websocket.Conn) error {
	read := func(want string, p any) (rpc, error) {
		var m rpc
		if err := wsjson.Read(ctx, ws, &m); err != nil {
			return m, err
		}
		if m.Method != want {
			return m, fmt.Errorf("expected %s, got %q", want, m.Method)
		}
		if p != nil && json.Unmarshal(m.Params, p) != nil {
			return m, fmt.Errorf("invalid %s parameters", want)
		}
		return m, nil
	}
	reply := func(m rpc, body fields) error {
		return wsjson.Write(ctx, ws, fields{"id": m.ID, "result": body})
	}
	m, err := read("initialize", nil)
	if err != nil {
		return err
	}
	if err := reply(m, fields{"userAgent": "fixture", "codexHome": "/home/node/.codex", "platformFamily": "unix", "platformOs": "linux"}); err != nil {
		return err
	}
	if _, err := read("initialized", nil); err != nil {
		return err
	}
	m, err = read("model/list", nil)
	if err != nil {
		return err
	}
	if err := reply(m, fields{"data": []fields{{
		"model": model, "supportedReasoningEfforts": []fields{{"reasoningEffort": "low"}, {"reasoningEffort": "medium"}},
		"defaultReasoningEffort": "low",
	}}, "nextCursor": nil}); err != nil {
		return err
	}
	var start struct {
		Model        string `json:"model"`
		Provider     string `json:"modelProvider"`
		Permissions  string `json:"permissions"`
		CWD          string `json:"cwd"`
		Approval     string `json:"approvalPolicy"`
		Ephemeral    bool   `json:"ephemeral"`
		Instructions string `json:"developerInstructions"`
		Tools        []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"dynamicTools"`
	}
	m, err = read("thread/start", &start)
	if err != nil {
		return err
	}
	if start.Model != model || start.Provider != "openai" || start.Permissions != "companion-workspace" || start.CWD != "/workspace" || start.Approval != "never" || !start.Ephemeral {
		return fmt.Errorf("wrong model or thread restrictions: %s", m.Params)
	}
	for _, want := range []string{"/workspace/notes", "without waiting for a request", "one Markdown file per topic", "Handle note requests in ordinary conversation."} {
		if !strings.Contains(start.Instructions, want) {
			return fmt.Errorf("workspace note instructions missing %q", want)
		}
	}
	const thread = "fixture-thread"
	const turn = "fixture-turn"
	if err := reply(m, fields{
		"thread": fields{"id": thread, "ephemeral": true, "model": model, "environments": []fields{
			{"environmentId": "local", "cwd": "/workspace", "runtimeWorkspaceRoots": []string{"/workspace"}},
		}},
		"model": model, "modelProvider": "openai", "approvalPolicy": "never", "cwd": "/workspace",
		"activePermissionProfile": fields{"id": "companion-workspace", "extends": nil},
		"sandbox": fields{
			"type": "workspaceWrite", "networkAccess": true, "writableRoots": []string{},
			"excludeSlashTmp": true, "excludeTmpdirEnvVar": true,
		},
	}); err != nil {
		return err
	}
	var run struct {
		Thread string `json:"threadId"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
		Input  []struct {
			Text string `json:"text"`
		} `json:"input"`
	}
	m, err = read("turn/start", &run)
	if err != nil {
		return err
	}
	if run.Thread != thread || run.Model != model || run.Effort != "medium" || len(run.Input) != 1 {
		return fmt.Errorf("wrong turn model, thread, effort, or input: %s", m.Params)
	}
	var p prompt
	_, data, _ := strings.Cut(run.Input[0].Text, "\n{")
	if err := json.Unmarshal([]byte("{"+data), &p); err != nil {
		return fmt.Errorf("invalid Companion turn context: %w", err)
	}
	if p.Kind == "periodic" {
		if len(start.Tools) != 0 {
			return fmt.Errorf("unexpected dynamic tool for periodic work")
		}
	} else if len(start.Tools) != 1 || start.Tools[0].Type != "function" || start.Tools[0].Name != "telegram_react" {
		return fmt.Errorf("expected the Telegram reaction tool: %+v", start.Tools)
	}
	if err := reply(m, fields{"turn": fields{"id": turn, "status": "inProgress"}}); err != nil {
		return err
	}
	name := p.Request
	f.mu.Lock()
	f.Turns = append(f.Turns, name)
	n := f.transcript.count("turns", name)
	f.mu.Unlock()
	if name == "restart-fixture" && n == 1 {
		// Keep an actual model step active until Docker stops the app. Do not
		// send a successful result before the restarted app resumes the step.
		m, err := read("turn/interrupt", nil)
		if err != nil {
			return err
		}
		return reply(m, fields{})
	}
	if name == "hello-fixture" || name == "reaction-only-fixture" {
		if err := wsjson.Write(ctx, ws, fields{"id": 100, "method": "item/tool/call", "params": fields{
			"threadId": thread, "turnId": turn, "callId": "fixture-call", "namespace": nil,
			"tool": "telegram_react", "arguments": fields{"emoji": "👍"},
		}}); err != nil {
			return err
		}
		m, err := read("", nil)
		if err != nil {
			return err
		}
		var result struct{ Success bool }
		if string(m.ID) != "100" || json.Unmarshal(m.Result, &result) != nil || !result.Success {
			return fmt.Errorf("reaction tool failed: %+v", m)
		}
	}
	final, err := scriptedAnswer(p)
	if err != nil {
		return err
	}
	return wsjson.Write(ctx, ws, fields{"method": "turn/completed", "params": fields{
		"threadId": thread, "turn": fields{"id": turn, "status": "completed", "items": []fields{
			{"type": "agentMessage", "phase": "final_answer", "text": final},
		}},
	}})
}

type prompt struct {
	Request string `json:"user_request"`
	Kind    string `json:"kind"`
}

func scriptedAnswer(p prompt) (string, error) {
	name := p.Request
	switch name {
	case "hello-fixture":
		return "fixture conversation reply", nil
	case "reaction-only-fixture":
		return "NO_REPLY", nil
	case "delegated-fixture":
		return "fixture delegated result", nil
	case "periodic-fixture":
		if p.Kind != "periodic" {
			return "", fmt.Errorf("expected periodic task, got %q", p.Kind)
		}
		return "fixture proactive result", nil
	case "Remember that I prefer short summaries.":
		if p.Kind != "conversation" {
			return "", fmt.Errorf("expected note request as conversation, got %q", p.Kind)
		}
		return "fixture note request", nil
	case "restart-fixture":
		return "fixture resumed result", nil
	default:
		return "", fmt.Errorf("unexpected user request: %q", name)
	}
}
