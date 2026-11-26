package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
)

type toolSpec struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

var tools = []toolSpec{
	{"function", "read_file", "Read a file in the Companion workspace.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1}},"required":["path"],"additionalProperties":false}`)},
	{"function", "list_files", "List entries at a path in the Companion workspace. Use . for the workspace root.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1}},"required":["path"],"additionalProperties":false}`)},
	{"function", "write_file", "Write text to a file through Companion's workspace permission checks.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"content":{"type":"string"}},"required":["path","content"],"additionalProperties":false}`)},
}

func allowedTool(name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

type toolRequest struct {
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	CallID    string          `json:"callId"`
	Namespace *string         `json:"namespace"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	ContentItems []toolText `json:"contentItems"`
	Success      bool       `json:"success"`
}

// Check the advertised schema before the callback. The caller must enforce
// access and approval rules, including safe path resolution.
func validArguments(tool string, args json.RawMessage) bool {
	d := json.NewDecoder(bytes.NewReader(args))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	var path, content *string
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return false
		}
		switch {
		case key == "path" && path == nil:
			if d.Decode(&path) != nil || path == nil || *path == "" {
				return false
			}
		case key == "content" && tool == "write_file" && content == nil:
			if d.Decode(&content) != nil || content == nil {
				return false
			}
		default:
			return false
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return false
	}
	_, err := d.Token()
	return err == io.EOF && path != nil && (tool != "write_file" || content != nil)
}

func (t *turnRun) toolCall(ctx context.Context, m message) error {
	var req toolRequest
	if json.Unmarshal(m.Params, &req) != nil || req.CallID == "" || req.ThreadID != t.threadID || req.TurnID != t.turnID {
		return t.s.reject(ctx, m, -32602, "invalid tool call or wrong thread or turn")
	}
	if req.Namespace != nil || !allowedTool(req.Tool) {
		return t.s.reject(ctx, m, -32601, "tool not allowed")
	}
	if !validArguments(req.Tool, req.Arguments) {
		return t.s.reject(ctx, m, -32602, "invalid tool arguments")
	}
	if t.calls[req.CallID] {
		return t.s.reject(ctx, m, -32602, "duplicate tool call")
	}
	t.calls[req.CallID] = true
	if ctx.Err() != nil {
		return ctx.Err()
	}
	text, success := "Tool callback is unavailable.", false
	if t.call != nil {
		var err error
		text, err = t.call(ctx, req.Tool, req.Arguments)
		success = err == nil
		if err != nil {
			text = err.Error()
		}
	}
	result, err := json.Marshal(toolResult{[]toolText{{"inputText", text}}, success})
	if err != nil {
		return err
	}
	return t.s.send(ctx, message{ID: m.ID, Result: result})
}
