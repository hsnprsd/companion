package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type fields = map[string]any

const fixtureModel = "fixture-model-v1"

func (f *fixture) model(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string
		Messages []struct {
			Role      string
			Content   json.RawMessage
			ToolCalls []struct {
				ID string
			} `json:"tool_calls"`
		}
		Tools []struct {
			Function struct{ Name string }
		}
	}
	bad := func(reason string) {
		f.problem("model fixture: %s", reason)
		http.Error(w, reason, http.StatusBadRequest)
	}
	if decode(r, &req) != nil || req.Model != fixtureModel || len(req.Messages) < 2 {
		bad("invalid model request")
		return
	}
	found := false
	for _, tool := range req.Tools {
		found = found || tool.Function.Name == "send_message"
	}
	if !found {
		bad("send_message tool is missing")
		return
	}
	if !strings.Contains(string(req.Messages[0].Content), "/workspace/notes") {
		bad("notes instructions are missing")
		return
	}
	var prompt struct {
		Request string `json:"user_request"`
	}
	stage := ""
	for _, msg := range req.Messages {
		if msg.Role == "user" {
			var text string
			if json.Unmarshal(msg.Content, &text) != nil {
				var parts []struct{ Text string }
				if json.Unmarshal(msg.Content, &parts) == nil && len(parts) == 1 {
					text = parts[0].Text
				}
			}
			instructions, data, _ := strings.Cut(text, "\n{")
			if !strings.Contains(instructions, "Use send_message for all user-visible text") || json.Unmarshal([]byte("{"+data), &prompt) != nil {
				bad("invalid Companion prompt")
				return
			}
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) == 1 {
			stage = msg.ToolCalls[0].ID
		}
	}
	name := prompt.Request
	if stage == "" {
		f.mu.Lock()
		f.Turns = append(f.Turns, name)
		n := f.transcript.count("turns", name)
		f.mu.Unlock()
		if name == "restart-fixture" && n == 1 {
			<-r.Context().Done()
			return
		}
	}
	last := string(req.Messages[len(req.Messages)-1].Content)
	send := func(id, text string) { toolChunk(w, id, "send_message", fields{"text": text}) }
	if stage == "answer" {
		if !strings.Contains(last, "queued") {
			bad("send_message did not confirm queueing")
			return
		}
		completion(w, fields{"content": "Do not deliver this ordinary assistant text."}, "stop")
		return
	}
	switch name {
	case "hello-fixture":
		send("answer", "fixture conversation reply")
	case "restart-fixture":
		send("answer", "fixture resumed result")
	case "Remember that I prefer short summaries.":
		if stage == "" {
			toolChunk(w, "write_note", "bash", fields{"command": "set -eu; test \"$PWD\" = /workspace; test ! -e /app/state; test ! -e /etc/companion/companion.json; mkdir -p notes; echo short > notes/preference.md; echo note-written"})
		} else if stage == "write_note" && strings.Contains(last, "note-written") {
			send("answer", "fixture note request")
		} else {
			bad("workspace write did not succeed")
		}
	case "slow-fixture":
		switch {
		case stage == "":
			send("progress", "I will inspect the fixture files.")
		case stage == "progress" && strings.Contains(last, "queued"):
			select {
			case <-time.After(5 * time.Second):
				toolChunk(w, "read_note", "bash", fields{"command": "set -eu; test \"$(cat notes/preference.md)\" = short; echo note-read"})
			case <-r.Context().Done():
			}
		case stage == "read_note" && strings.Contains(last, "note-read"):
			send("answer", "fixture full answer")
		default:
			bad("progress or workspace persistence check failed")
		}
	default:
		bad("unexpected user request")
	}
}

func toolChunk(w http.ResponseWriter, id, name string, args fields) {
	arguments, _ := json.Marshal(args)
	completion(w, fields{"tool_calls": []fields{{"index": 0, "id": id, "type": "function", "function": fields{"name": name, "arguments": string(arguments)}}}}, "tool_calls")
}

func completion(w http.ResponseWriter, delta fields, stop string) {
	w.Header().Set("Content-Type", "text/event-stream")
	delta["role"] = "assistant"
	for _, choice := range []fields{{"index": 0, "delta": delta, "finish_reason": nil}, {"index": 0, "delta": fields{}, "finish_reason": stop}} {
		data, _ := json.Marshal(fields{"id": "fixture-completion", "object": "chat.completion.chunk", "model": fixtureModel, "choices": []fields{choice}})
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}
