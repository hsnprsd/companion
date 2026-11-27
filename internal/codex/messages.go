package codex

import (
	"context"
	"encoding/json"
	"strings"
)

const sendMessageName = "send_message"

func sendMessageTool() params {
	return params{
		"type": "function", "name": sendMessageName,
		"description": "Queue plain text for the current Telegram chat. Use this tool for all messages to the user, including the complete answer. Success does not confirm delivery.",
		"inputSchema": params{
			"type": "object", "properties": params{"text": params{"type": "string"}},
			"required": []string{"text"}, "additionalProperties": false,
		},
	}
}

func toolCall(m message) bool {
	return len(m.ID) != 0 && m.Method == "item/tool/call"
}

func (t *turnRun) handleToolCall(ctx context.Context, m message) error {
	var call struct {
		ThreadID  string          `json:"threadId"`
		TurnID    string          `json:"turnId"`
		CallID    string          `json:"callId"`
		Namespace *string         `json:"namespace"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(m.Params, &call) != nil || t.done || call.ThreadID != t.threadID || call.TurnID != t.turnID ||
		call.CallID == "" || call.Namespace != nil || call.Tool != sendMessageName {
		return t.s.reject(ctx, m, -32601, "tool call not allowed")
	}

	var args map[string]json.RawMessage
	var text string
	if json.Unmarshal(call.Arguments, &args) != nil || len(args) != 1 || json.Unmarshal(args["text"], &text) != nil || strings.TrimSpace(text) == "" {
		return t.toolResult(ctx, m, false, "Supply a non-blank text string and no other arguments.")
	}
	if err := t.sendMessage(text); err != nil {
		return err
	}
	return t.toolResult(ctx, m, true, "queued")
}

func (t *turnRun) toolResult(ctx context.Context, m message, success bool, text string) error {
	result, err := json.Marshal(params{
		"success": success, "contentItems": []params{{"type": "inputText", "text": text}},
	})
	if err != nil {
		return err
	}
	return t.s.send(ctx, message{ID: m.ID, Result: result})
}
