package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const stateVersion = 1

const (
	ready     = "ready"
	running   = "running"
	waiting   = "waiting for approval"
	applying  = "applying action"
	blocked   = "blocked"
	complete  = "complete"
	scheduled = "scheduled"
	stopped   = "stopped"
)

const (
	pending  = "pending"
	approved = "approved"
	denied   = "denied"
	void     = "void"

	applied   = "applied"
	notRun    = "not executed"
	absent    = "not applied"
	uncertain = "uncertain"
)

type task struct {
	ID           int64         `json:"id"`
	Kind         string        `json:"kind"`
	Text         string        `json:"text"`
	Status       string        `json:"status"`
	Revision     int64         `json:"revision"`
	Every        time.Duration `json:"every,omitempty"`
	NextRun      time.Time     `json:"next_run,omitempty"`
	ActionIDs    []int64       `json:"action_ids,omitempty"`
	LastActionID int64         `json:"last_action_id,omitempty"`
	Progress     string        `json:"progress,omitempty"`
	LastResult   string        `json:"last_result,omitempty"`
	Problem      string        `json:"problem,omitempty"`
	Unsafe       bool          `json:"uncertain_action,omitempty"`
}

type approval struct {
	ID       int64  `json:"id"`
	TaskID   int64  `json:"task_id"`
	Action   Action `json:"action"`
	Decision string `json:"decision"`
	Outcome  string `json:"outcome,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type turn struct {
	TaskID    int64  `json:"task_id"`
	User      string `json:"user"`
	Assistant string `json:"assistant,omitempty"`
}

type delivery struct {
	Text      string    `json:"text"`
	Sent      int       `json:"sent_bytes,omitempty"`
	Sending   bool      `json:"sending,omitempty"`
	Failures  int       `json:"failures,omitempty"`
	NotBefore time.Time `json:"not_before,omitempty"`
	Notice    bool      `json:"notice,omitempty"`
}

type incident struct {
	Delivery delivery `json:"delivery"`
	Reason   string   `json:"reason"`
}

type state struct {
	Version    int                 `json:"version"`
	Offset     int64               `json:"offset"`
	LastUpdate time.Time           `json:"last_update,omitempty"`
	TaskSeq    int64               `json:"task_sequence"`
	ActionSeq  int64               `json:"action_sequence"`
	Tasks      map[int64]*task     `json:"tasks"`
	Approvals  map[int64]*approval `json:"approvals"`
	History    []turn              `json:"history"`
	Notes      map[string]string   `json:"notebook"`
	Grants     map[string]bool     `json:"grants"`
	Outbox     []delivery          `json:"outbox"`
	Incidents  []incident          `json:"delivery_incidents,omitempty"`
}

func load(dir string) (state, error) {
	s := state{
		Version:   stateVersion,
		Tasks:     make(map[int64]*task),
		Approvals: make(map[int64]*approval),
		Notes:     make(map[string]string),
		Grants:    make(map[string]bool),
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return state{}, err
	}
	s = state{}
	if err := json.Unmarshal(data, &s); err != nil {
		return state{}, err
	}
	if s.Version != stateVersion || s.Tasks == nil || s.Approvals == nil || s.Notes == nil || s.Grants == nil {
		return state{}, errors.New("unsupported or incomplete engine state")
	}
	for id, a := range s.Approvals {
		if a == nil || a.ID != id || s.Tasks[a.TaskID] == nil {
			return state{}, fmt.Errorf("invalid approval %d in engine state", id)
		}
	}
	for id, t := range s.Tasks {
		if t == nil || t.ID != id {
			return state{}, fmt.Errorf("invalid task %d in engine state", id)
		}
		for _, actionID := range t.ActionIDs {
			if a := s.Approvals[actionID]; a == nil || a.TaskID != id {
				return state{}, fmt.Errorf("invalid action %d for task %d in engine state", actionID, id)
			}
		}
		if t.Status == applying {
			if a := s.lastAction(t); a == nil || a.Decision != approved || a.Outcome != "" {
				return state{}, fmt.Errorf("task %d has no approved action to recover", id)
			}
		}
	}
	for i, d := range s.Outbox {
		if d.Sent < 0 || d.Sent >= len(d.Text) || !utf8.RuneStart(d.Text[d.Sent]) || d.Failures < 0 || d.Failures > 7 || i != 0 && d.Sending {
			return state{}, fmt.Errorf("invalid outbox entry %d in engine state", i)
		}
	}
	return s, nil
}

func ensureStateDir(dir string) error {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = fmt.Errorf("%s is not a directory", dir)
		}
		return err
	}
	parent := filepath.Dir(dir)
	if err := ensureStateDir(parent); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return syncDir(parent)
}

func save(dir string, data []byte) error {
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "state.json")); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// commit updates the cursor and all effects of an update in one transaction.
// Stop both loops after a storage failure. A failed directory sync, for
// example, can leave the outcome of the rename uncertain until Open.
func (e *Engine) commit(change func(*state)) error {
	if e.failed != nil {
		return e.failed
	}
	change(&e.state)
	data, err := json.Marshal(&e.state)
	if err == nil {
		err = save(e.dir, data)
	}
	if err != nil {
		e.failed = fmt.Errorf("save engine state: %w", err)
		if e.active != nil {
			e.active.cancel()
		}
	} else {
		e.offset, e.lastUpdate = e.state.Offset, e.state.LastUpdate
	}
	wake(e.workWake)
	wake(e.sendWake)
	return e.failed
}

func (s *state) say(text string) {
	if text != "" {
		s.Outbox = append(s.Outbox, delivery{Text: strings.ToValidUTF8(text, "\uFFFD")})
	}
}

func (s *state) lastAction(t *task) *approval {
	if n := len(t.ActionIDs); n != 0 {
		return s.Approvals[t.ActionIDs[n-1]]
	}
	return nil
}
