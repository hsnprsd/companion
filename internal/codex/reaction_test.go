package codex

import (
	"strings"
	"testing"
)

func TestReactionTool(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			client, conns := testRunner(t)
			p, results, _ := launch(t, client, conns, true)
			p.init()
			p.catalog()
			p.thread("thread", true)
			turn := p.turn("thread")
			call := func(id, turnID, name string, args params) {
				p.serverRequest(id, "item/tool/call", params{
					"callId": id, "threadId": "thread", "turnId": turnID,
					"namespace": nil, "tool": name, "arguments": args,
				})
			}
			result := func(id string, success bool) {
				r := p.read("")
				check(t, string(r.ID), `"`+id+`"`)
				check(t, decode[params](t, r.Result)["success"], success)
			}
			// An early call must wait until the client knows the turn ID.
			call("foreign", "other", reactionToolName, params{"emoji": "🔥"})
			p.reply(turn, turnResult("turn", "inProgress"))
			p.assertRejection(`"foreign"`, -32601)
			call("unknown", "turn", "other_tool", params{"emoji": "🔥"})
			p.assertRejection(`"unknown"`, -32601)
			call("target", "turn", reactionToolName, params{"emoji": "👍", "message_id": 123})
			result("target", false)
			call("invalid", "turn", reactionToolName, params{"emoji": "no"})
			result("invalid", false)
			call("valid", "turn", reactionToolName, params{"emoji": "👍"})
			result("valid", true)
			call("repeat", "turn", reactionToolName, params{"emoji": "🔥"})
			result("repeat", false)
			done := turnResult("turn", status, item("agentMessage", "final_answer", "NO_REPLY"))
			done["threadId"] = "thread"
			p.event("turn/completed", done)
			got := waitResult(t, results)
			if status == "completed" {
				check(t, got, outcome{emoji: "👍"})
			} else if got.emoji != "" || got.err == nil || !strings.Contains(got.err.Error(), "turn failed") {
				t.Fatalf("failed turn returned %+v", got)
			}
		})
	}
}
