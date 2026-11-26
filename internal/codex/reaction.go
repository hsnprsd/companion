package codex

import (
	"context"
	"encoding/json"
	"slices"
)

const reactionToolName = "telegram_react"

var reactionEmoji = []string{"👍", "👎", "❤", "🔥", "👏", "😁", "🤔", "🎉", "👀", "🙏", "👌", "🤝", "🫡"}

func reactionTool() params {
	return params{
		"type": "function", "name": reactionToolName,
		"description": "Request one emoji reaction to the user's current Telegram message. Use it when a reaction is useful. To send only the reaction, finish with exactly NO_REPLY.",
		"inputSchema": params{
			"type": "object", "properties": params{"emoji": params{"type": "string", "enum": reactionEmoji}},
			"required": []string{"emoji"}, "additionalProperties": false,
		},
	}
}

func (t *turnRun) react(ctx context.Context, m message) error {
	var call struct {
		ThreadID  string                     `json:"threadId"`
		TurnID    string                     `json:"turnId"`
		CallID    string                     `json:"callId"`
		Namespace *string                    `json:"namespace"`
		Tool      string                     `json:"tool"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal(m.Params, &call) != nil || call.ThreadID != t.threadID || call.TurnID != t.turnID ||
		call.CallID == "" || call.Namespace != nil || call.Tool != reactionToolName {
		return t.s.reject(ctx, m, -32601, "server request not allowed")
	}
	var emoji string
	var success bool
	var text string
	switch {
	case len(call.Arguments) != 1 || json.Unmarshal(call.Arguments["emoji"], &emoji) != nil || !slices.Contains(reactionEmoji, emoji):
		text = "Specify only one supported emoji from the tool schema."
	case t.reaction != "":
		text = "This turn already requested a reaction."
	default:
		t.reaction = emoji
		success = true
		text = "Companion will try to send the reaction after this turn succeeds."
	}
	result, err := json.Marshal(params{
		"success": success, "contentItems": []params{{"type": "inputText", "text": text}},
	})
	if err != nil {
		return err
	}
	return t.s.send(ctx, message{ID: m.ID, Result: result})
}
