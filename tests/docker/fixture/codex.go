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
	if err := reply(m, fields{"data": []fields{{"model": model}}, "nextCursor": nil}); err != nil {
		return err
	}
	var start struct {
		Model       string `json:"model"`
		Provider    string `json:"modelProvider"`
		Permissions string `json:"permissions"`
		CWD         string `json:"cwd"`
		Approval    string `json:"approvalPolicy"`
		Ephemeral   bool   `json:"ephemeral"`
		Tools       []struct {
			Name string `json:"name"`
		} `json:"dynamicTools"`
	}
	m, err = read("thread/start", &start)
	if err != nil {
		return err
	}
	advertised := map[string]bool{}
	for _, tool := range start.Tools {
		advertised[tool.Name] = true
	}
	if start.Model != model || start.Provider != "openai" || start.Permissions != "companion-scratch" || start.CWD != "/scratch" || start.Approval != "never" || !start.Ephemeral ||
		!advertised["read_file"] || !advertised["list_files"] || !advertised["write_file"] {
		return fmt.Errorf("wrong model, thread restrictions, or file tools: %s", m.Params)
	}
	const thread = "fixture-thread"
	const turn = "fixture-turn"
	if err := reply(m, fields{
		"thread": fields{"id": thread, "ephemeral": true, "model": model, "environments": []fields{
			{"environmentId": "local", "cwd": "/scratch", "runtimeWorkspaceRoots": []string{"/scratch"}},
		}},
		"model": model, "modelProvider": "openai", "approvalPolicy": "never", "cwd": "/scratch",
		"activePermissionProfile": fields{"id": "companion-scratch", "extends": nil},
		"sandbox": fields{
			"type": "workspaceWrite", "networkAccess": false, "writableRoots": []string{},
			"excludeSlashTmp": true, "excludeTmpdirEnvVar": true,
		},
	}); err != nil {
		return err
	}
	var run struct {
		Thread string `json:"threadId"`
		Model  string `json:"model"`
		Input  []struct {
			Text string `json:"text"`
		} `json:"input"`
	}
	m, err = read("turn/start", &run)
	if err != nil {
		return err
	}
	if run.Thread != thread || run.Model != model || len(run.Input) != 1 {
		return fmt.Errorf("wrong turn model, thread, or input: %s", m.Params)
	}
	var p prompt
	_, data, _ := strings.Cut(run.Input[0].Text, "\n{")
	if err := json.Unmarshal([]byte("{"+data), &p); err != nil {
		return fmt.Errorf("invalid Companion turn context: %w", err)
	}
	if err := reply(m, fields{"turn": fields{"id": turn, "status": "inProgress"}}); err != nil {
		return err
	}
	tool := func(name, path, content, expected string, success bool) error {
		args := fields{"path": path}
		if name == "write_file" {
			args["content"] = content
		}
		const call = "fixture-call"
		if err := wsjson.Write(ctx, ws, fields{"id": call, "method": "item/tool/call", "params": fields{
			"threadId": thread, "turnId": turn, "callId": call, "tool": name, "arguments": args,
		}}); err != nil {
			return err
		}
		var result struct {
			Items []struct {
				Text string `json:"text"`
			} `json:"contentItems"`
			Success bool `json:"success"`
		}
		var msg rpc
		if err := wsjson.Read(ctx, ws, &msg); err != nil {
			return err
		}
		if string(msg.ID) != `"`+call+`"` || len(msg.Error) != 0 || json.Unmarshal(msg.Result, &result) != nil ||
			result.Success != success || len(result.Items) != 1 || !strings.Contains(result.Items[0].Text, expected) {
			return fmt.Errorf("unexpected %s reply for %s (wanted %q, success %t): result=%s error=%s", name, path, expected, success, msg.Result, msg.Error)
		}
		return nil
	}
	name := p.Request
	for _, a := range p.Actions {
		if a.Decision == "approved" && a.Outcome == "applied" {
			name += ":applied"
			break
		}
	}
	f.mu.Lock()
	f.Turns = append(f.Turns, name)
	n := f.transcript.count("turns", name)
	f.mu.Unlock()
	if name == "write-a:applied" && n == 1 {
		// Keep an actual model step active until Docker stops the app. Do not
		// send a successful result before the restarted app resumes the step.
		m, err := read("turn/interrupt", nil)
		if err != nil {
			return err
		}
		return reply(m, fields{})
	}
	final, err := scriptedAnswer(p, name, tool)
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
	Request string            `json:"user_request"`
	Kind    string            `json:"kind"`
	Notes   map[string]string `json:"notebook"`
	Actions []struct {
		Decision string `json:"decision"`
		Outcome  string `json:"outcome"`
	} `json:"actions_and_decisions"`
}

func scriptedAnswer(p prompt, name string, tool func(name, path, content, expected string, success bool) error) (string, error) {
	switch name {
	case "hello-fixture":
		return "fixture conversation reply", nil
	case "delegated-fixture":
		return "fixture delegated result", nil
	case "periodic-fixture":
		if p.Kind != "periodic" {
			return "", fmt.Errorf("expected periodic task, got %q", p.Kind)
		}
		return "fixture proactive result", nil
	case "recall-original", "recall-edited", "recall-deleted":
		want := map[string]string{"recall-original": "amber", "recall-edited": "teal", "recall-deleted": ""}[name]
		if got, exists := p.Notes["color"]; got != want || exists != (want != "") {
			return "", fmt.Errorf("wrong durable notebook context for %s: %+v", name, p.Notes)
		}
		return "fixture " + name, nil
	case "read-alias-blocked", "read-alias-revoked":
		return "fixture " + name, tool("read_file", "@manual/guide.txt", "", "not granted", false)
	case "read-alias-granted":
		return "fixture " + name, tool("read_file", "@manual/guide.txt", "", "fixture external data", true)
	case "write-alias-blocked":
		return "fixture " + name, tool("write_file", "@manual/guide.txt", "not allowed", "read-only", false)
	case "write-a", "write-b":
		return "fixture waiting for " + name, tool("write_file", name+".txt", "exact fixture "+name+"\n", "not executed", true)
	case "write-a:applied", "write-b:applied":
		path, _, _ := strings.Cut(name, ":")
		return "fixture " + path + " done", tool("read_file", path+".txt", "", "exact fixture "+path, true)
	default:
		return "", fmt.Errorf("unexpected user request: %q", name)
	}
}
