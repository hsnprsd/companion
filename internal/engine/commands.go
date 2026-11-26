package engine

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const helpText = `Send a message to talk. Use these commands to delegate work or inspect state:
/help — Show commands.
/work TEXT — Start one task.
/every DURATION TEXT — Run work each interval (for example, /every 24h Check the workspace). The first run waits one interval. Minimum: 1s.
/tasks — List tasks.
/task ID — Show a task and its latest action, if any.
/change ID TEXT — Replace a task's instructions and start a new run.
/stop ID — Stop a task or schedule.
/retry ID — Resume a blocked task when it is safe to retry.
/notes — Show the notebook.
/note KEY TEXT — Add or replace a note.
/forget KEY — Delete a note.
/access — Show file access.
/grant NAME — Grant reads from a mounted alias.
/revoke NAME — Revoke access to an alias.
/approve ID — Approve the exact proposed action.
/deny ID — Deny a proposed action.`

func word(text string) (head, rest string) {
	text = strings.TrimLeftFunc(text, unicode.IsSpace)
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		return text[:i], strings.TrimLeftFunc(text[i:], unicode.IsSpace)
	}
	return text, ""
}

func positiveID(text string) (int64, bool) {
	id, err := strconv.ParseInt(text, 10, 64)
	return id, err == nil && id > 0 && text == strconv.FormatInt(id, 10)
}

func validAlias(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (e *Engine) acceptText(s *state, text string) {
	cmd, args := word(text)
	if !strings.HasPrefix(cmd, "/") {
		t := s.newTask("conversation", text)
		s.History = append(s.History, turn{TaskID: t.ID, User: text})
		return
	}
	usage := func(args string) { s.say("Use " + cmd + args + ".") }
	switch cmd {
	case "/help", "/tasks", "/notes", "/access":
		if args != "" {
			usage("")
			return
		}
		switch cmd {
		case "/help":
			s.say(helpText)
		case "/tasks":
			s.listTasks()
		case "/notes":
			s.showNotes()
		case "/access":
			e.showAccess(s)
		}
	case "/work":
		if args == "" {
			usage(" TEXT")
			return
		}
		t := s.newTask("work", args)
		s.say(fmt.Sprintf("Task %d is ready.", t.ID))
	case "/every":
		interval, text := word(args)
		d, err := time.ParseDuration(interval)
		if err != nil || d < time.Second || text == "" {
			usage(" DURATION TEXT (for example, /every 24h Check the workspace)")
			return
		}
		t := s.newTask("periodic", text)
		t.Every, t.NextRun, t.Status = d, e.now().Add(d), scheduled
		s.say(fmt.Sprintf("Task %d runs every %s. Its first run is due at %s.", t.ID, d, displayTime(t.NextRun)))
	case "/task", "/change", "/stop", "/retry":
		idText, rest := word(args)
		id, valid := positiveID(idText)
		if !valid || (cmd == "/change") != (rest != "") {
			if cmd == "/change" {
				usage(" ID TEXT")
			} else {
				usage(" ID")
			}
			return
		}
		s.taskCommand(cmd, id, rest)
	case "/note", "/forget":
		key, value := word(args)
		if key == "" || (cmd == "/note") != (value != "") {
			if cmd == "/note" {
				usage(" KEY TEXT")
			} else {
				usage(" KEY")
			}
			return
		}
		if cmd == "/note" {
			s.Notes[key] = value
			s.say(fmt.Sprintf("Saved note %q.", key))
		} else {
			delete(s.Notes, key)
			s.say(fmt.Sprintf("Removed note %q.", key))
		}
	case "/grant", "/revoke":
		name, rest := word(args)
		if !validAlias(name) || rest != "" {
			usage(" NAME")
			return
		}
		if cmd == "/grant" {
			if !e.aliases[name] {
				s.say(fmt.Sprintf("Access %q is not available. Use /access to see available grants.", name))
				return
			}
			s.Grants[name] = true
			s.say(fmt.Sprintf("Granted read access to @%s/. Writes are not permitted.", name))
		} else {
			delete(s.Grants, name)
			s.say(fmt.Sprintf("Revoked access to @%s/.", name))
		}
	case "/approve", "/deny":
		id, valid := positiveID(args)
		if !valid || text != fmt.Sprintf("%s %d", cmd, id) {
			usage(" ID exactly as shown in the approval request")
			return
		}
		s.decide(cmd, id)
	default:
		s.say("Unknown command. Use /help to see the commands.")
	}
}

func (s *state) newTask(kind, text string) *task {
	s.TaskSeq++
	t := &task{ID: s.TaskSeq, Kind: kind, Text: text, Status: ready, Revision: 1}
	s.Tasks[t.ID] = t
	return t
}

func (s *state) decide(cmd string, id int64) {
	a := s.Approvals[id]
	if a == nil || a.Decision != pending {
		s.say(fmt.Sprintf("Approval %d is not pending.", id))
		return
	}
	t := s.Tasks[a.TaskID]
	if t.Status != waiting || s.lastAction(t) != a {
		s.say(fmt.Sprintf("Approval %d is not pending.", id))
		return
	}
	t.Status = ready
	if cmd == "/approve" {
		a.Decision = approved
		s.say(fmt.Sprintf("Approved action %d for task %d. Companion will check access again before it starts the action.", id, t.ID))
	} else {
		a.Decision, a.Outcome = denied, notRun
		a.Detail = "The user denied this action."
		s.say(fmt.Sprintf("Denied action %d for task %d.", id, t.ID))
	}
}

func (s *state) taskCommand(cmd string, id int64, text string) {
	t := s.Tasks[id]
	if t == nil {
		s.say(fmt.Sprintf("Task %d was not found.", id))
		return
	}
	if cmd == "/task" {
		s.showTask(t)
		return
	}
	if t.Status == applying {
		s.say(fmt.Sprintf("Task %d already started an approved action. Wait for the result before you change or stop the task.", id))
		return
	}
	if t.Unsafe && cmd != "/stop" {
		s.say(fmt.Sprintf("Task %d has an uncertain action and cannot resume. Use /task %d to see the action, then check the file before you start new work.", id, id))
		return
	}
	switch cmd {
	case "/change":
		s.voidAction(t)
		t.Text, t.Status = text, ready
		// Keep completed actions in the next prompt, including exact input,
		// so a change cannot hide prior effects from the runner.
		t.Progress, t.Problem = "", ""
	case "/stop":
		s.voidAction(t)
		t.Status = stopped
	case "/retry":
		if t.Status != blocked {
			s.say(fmt.Sprintf("Task %d is not blocked.", id))
			return
		}
		t.Status, t.Problem = ready, ""
	}
	t.Revision++
	s.say(fmt.Sprintf("Task %d: %s.", id, t.Status))
}

func (s *state) voidAction(t *task) {
	if a := s.lastAction(t); a != nil && a.Outcome == "" && (a.Decision == pending || a.Decision == approved) {
		// Preserve an approval decision even if the task stopped before use.
		if a.Decision == pending {
			a.Decision = void
		}
		a.Outcome, a.Detail = notRun, "The task changed or stopped before execution."
	}
}

func (s *state) listTasks() {
	var ids []int64
	for _, t := range s.Tasks {
		if t.Kind != "conversation" || t.Status != complete {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		s.say("No tasks.")
		return
	}
	slices.Sort(ids)
	var text strings.Builder
	for _, id := range ids {
		t := s.Tasks[id]
		fmt.Fprintf(&text, "%d — %s (%s)", t.ID, t.Status, t.Kind)
		if t.Status == scheduled {
			fmt.Fprintf(&text, ", due %s", displayTime(t.NextRun))
		}
		text.WriteByte('\n')
	}
	s.say(strings.TrimSuffix(text.String(), "\n"))
}

func (s *state) showTask(t *task) {
	var text strings.Builder
	fmt.Fprintf(&text, "Task %d — %s (%s)\nInstructions: %s", t.ID, t.Status, t.Kind, t.Text)
	if t.Every != 0 {
		fmt.Fprintf(&text, "\nInterval: %s", t.Every)
		if t.Status == scheduled {
			fmt.Fprintf(&text, "\nNext run: %s", displayTime(t.NextRun))
		}
	}
	if t.Problem != "" {
		fmt.Fprintf(&text, "\nProblem: %s", t.Problem)
	}
	if t.LastResult != "" {
		fmt.Fprintf(&text, "\nLast result: %s", t.LastResult)
	}
	s.say(text.String())
	if a := s.Approvals[t.LastActionID]; a != nil {
		if a.Decision == pending {
			// Keep a pending proposal in its own complete Telegram message.
			s.say(proposalText(a))
		} else {
			s.say(fmt.Sprintf("Task %d, action %d\n%s\nDecision: %s\nOutcome: %s\n%s", t.ID, a.ID, actionJSON(a.Action), a.Decision, a.Outcome, a.Detail))
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (s *state) showNotes() {
	if len(s.Notes) == 0 {
		s.say("The notebook is empty.")
		return
	}
	var text strings.Builder
	for _, key := range sortedKeys(s.Notes) {
		fmt.Fprintf(&text, "%s: %s\n", key, s.Notes[key])
	}
	s.say(strings.TrimSuffix(text.String(), "\n"))
}

func (e *Engine) showAccess(s *state) {
	var text strings.Builder
	text.WriteString("Workspace reads are allowed. Each workspace write needs approval. External aliases are read-only.")
	if len(e.aliases) == 0 {
		text.WriteString(" No external aliases are mounted.")
	}
	for _, name := range sortedKeys(e.aliases) {
		access := "not granted"
		if s.Grants[name] {
			access = "granted"
		}
		fmt.Fprintf(&text, "\n@%s/ — %s", name, access)
	}
	s.say(text.String())
}

func displayTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
