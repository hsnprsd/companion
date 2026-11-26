package engine

import (
	"fmt"
	"maps"
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
/forget KEY — Delete a note.`

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

func (e *Engine) acceptText(s *state, messageID int64, text string) {
	cmd, args := word(text)
	if !strings.HasPrefix(cmd, "/") {
		t := s.newTask("conversation", text, messageID)
		s.History = append(s.History, turn{TaskID: t.ID, User: text})
		return
	}
	usage := func(args string) { s.say("Use " + cmd + args + ".") }
	switch cmd {
	case "/help", "/tasks", "/notes":
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
		}
	case "/work":
		if args == "" {
			usage(" TEXT")
			return
		}
		t := s.newTask("work", args, messageID)
		s.say(fmt.Sprintf("Task %d is ready.", t.ID))
	case "/every":
		interval, text := word(args)
		d, err := time.ParseDuration(interval)
		if err != nil || d < time.Second || text == "" {
			usage(" DURATION TEXT (for example, /every 24h Check the workspace)")
			return
		}
		t := s.newTask("periodic", text, 0)
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
		s.taskCommand(cmd, id, rest, messageID)
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
	default:
		s.say("Unknown command. Use /help to see the commands.")
	}
}

func (s *state) newTask(kind, text string, messageID int64) *task {
	s.TaskSeq++
	t := &task{ID: s.TaskSeq, Kind: kind, Text: text, Status: ready, Revision: 1, MessageID: messageID}
	s.Tasks[t.ID] = t
	return t
}

func (s *state) taskCommand(cmd string, id int64, text string, messageID int64) {
	t := s.Tasks[id]
	if t == nil {
		s.say(fmt.Sprintf("Task %d was not found.", id))
		return
	}
	if cmd == "/task" {
		s.showTask(t)
		return
	}
	switch cmd {
	case "/change":
		t.Text, t.Status, t.Problem = text, ready, ""
		if t.Every == 0 {
			t.MessageID = messageID
		}
	case "/stop":
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
}

func (s *state) showNotes() {
	if len(s.Notes) == 0 {
		s.say("The notebook is empty.")
		return
	}
	var text strings.Builder
	for _, key := range slices.Sorted(maps.Keys(s.Notes)) {
		fmt.Fprintf(&text, "%s: %s\n", key, s.Notes[key])
	}
	s.say(strings.TrimSuffix(text.String(), "\n"))
}

func displayTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
