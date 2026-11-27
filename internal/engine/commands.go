package engine

import (
	"fmt"
	"strings"
)

const helpText = `Send a message to ask a question, request work, or manage notes.
/help — Show commands.
/status — Check whether Companion is working.`

func (e *Engine) acceptText(s *state, text string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		s.Pending = append(s.Pending, request{Text: text})
		return
	}
	cmd := strings.Fields(trimmed)[0]
	switch cmd {
	case "/help", "/status":
		if trimmed != cmd {
			s.say("Use " + cmd + ".")
			return
		}
		if cmd == "/help" {
			s.say(helpText)
		} else {
			e.showStatus(s)
		}
	default:
		s.say("Unknown command. Use /help to see the commands.")
	}
}

func (e *Engine) showStatus(s *state) {
	queued := len(s.Pending)
	text := "Companion is idle."
	if e.active != nil {
		text = "Companion is working on a request."
		queued--
	} else if queued > 0 {
		text = "Companion is ready to start work."
	}
	if queued > 0 {
		text += fmt.Sprintf("\nRequests in queue: %d.", queued)
	}
	s.say(text)
}
