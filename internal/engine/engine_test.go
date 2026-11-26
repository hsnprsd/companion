package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type runnerFunc func(context.Context, string) (string, error)

func (r runnerFunc) Run(ctx context.Context, prompt string, _ bool, _ func(string) error) (string, string, error) {
	text, err := r(ctx, prompt)
	return text, "", err
}

var echoRunner runnerFunc = func(context.Context, string) (string, error) { return "done", nil }

func openEngine(t *testing.T, dir string, r Runner) *Engine {
	t.Helper()
	e, err := Open(dir, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeEngine(t, e) })
	return e
}

func closeEngine(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Close(); err != nil {
		t.Error(err)
	}
}

func acceptUpdate(t *testing.T, e *Engine, id int64, text string) {
	t.Helper()
	if err := e.Accept(id, 0, text); err != nil {
		t.Fatal(err)
	}
}

func jobFor(t *testing.T, e *Engine) func() error {
	t.Helper()
	job, err := e.nextWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func runJob(t *testing.T, e *Engine) {
	t.Helper()
	job := jobFor(t, e)
	if job == nil {
		t.Fatal("no work is ready")
	}
	if err := job(); err != nil {
		t.Fatal(err)
	}
}

func stored(t *testing.T, e *Engine) state {
	t.Helper()
	s, err := load(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func outboxText(s state) string {
	var text strings.Builder
	for _, d := range s.Outbox {
		text.WriteString(d.Text + "\n")
	}
	return text.String()
}

func startLoop(t *testing.T, loop func(context.Context) error) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop(ctx) }()
	stop := func() {
		t.Helper()
		cancel()
		if err := receive(t, done); !errors.Is(err, context.Canceled) {
			t.Errorf("loop shutdown = %v, want context.Canceled", err)
		}
	}
	stopped := false
	finish := func() {
		if !stopped {
			stopped = true
			stop()
		}
	}
	t.Cleanup(finish)
	return finish
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for loop")
		var zero T
		return zero
	}
}

func TestAcceptCursorAndStateLock(t *testing.T) {
	dir := t.TempDir()
	e := openEngine(t, dir, echoRunner)
	if other, err := Open(dir, echoRunner, nil); err == nil {
		closeEngine(t, other)
		t.Fatal("second engine opened the same directory")
	}
	acceptUpdate(t, e, 100, "original")
	acceptUpdate(t, e, 100, "duplicate")
	acceptUpdate(t, e, 102, "")
	s := stored(t, e)
	if s.Offset != 103 || len(s.Pending) != 1 || s.Pending[0].Text != "original" || len(s.History) != 0 || len(s.Outbox) != 0 {
		t.Fatalf("cursor and effects were not saved together: %+v", s)
	}
	closeEngine(t, e)
	if err := e.Accept(103, 0, "after close"); err == nil {
		t.Fatal("closed engine accepted an update")
	}
	e = openEngine(t, dir, echoRunner)
	if got := e.Offset(); got != 103 {
		t.Fatalf("offset after reopen = %d, want 103", got)
	}
	acceptUpdate(t, e, 101, "duplicate after reopen")
	now := s.LastUpdate.Add(cursorLifetime)
	e.now = func() time.Time { return now }
	if got := e.Offset(); got != 0 {
		t.Fatalf("expired offset = %d, want 0", got)
	}
	acceptUpdate(t, e, 8, "new sequence")
	if s := stored(t, e); s.Offset != 9 || !s.LastUpdate.Equal(now) || len(s.Pending) != 2 {
		t.Fatalf("new sequence not saved: %+v", s)
	}
}

func TestOpenRejectsOldSnapshotWithoutChangingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	const old = `{"version":2,"tasks":{},"notebook":{}}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if e, err := Open(dir, echoRunner, nil); err == nil {
		closeEngine(t, e)
		t.Fatal("opened a version 2 snapshot")
	} else if !strings.Contains(err.Error(), "unsupported engine state version 2") {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != old {
		t.Fatalf("version 2 state changed: %s, %v", data, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	e := openEngine(t, dir, echoRunner)
	acceptUpdate(t, e, 1, "start fresh")
	if s := stored(t, e); s.Version != stateVersion || len(s.Pending) != 1 {
		t.Fatalf("did not create a new snapshot: %+v", s)
	}
}

func TestAcceptDoesNotAcknowledgeFailedSave(t *testing.T) {
	e := openEngine(t, t.TempDir(), echoRunner)
	if err := os.Mkdir(filepath.Join(e.dir, "state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := e.Accept(7, 0, "must not run")
	if err == nil || e.Offset() != 0 {
		t.Fatalf("failed Accept returned %v, offset %d", err, e.Offset())
	}
	if got := e.Work(context.Background()); got != err {
		t.Fatalf("Work returned %v after failed save, want %v", got, err)
	}
	if got := e.Deliver(context.Background(), func(context.Context, string) error {
		t.Error("sent a reply for an unsaved update")
		return nil
	}, noReaction(t)); got != err {
		t.Fatalf("Deliver returned %v after failed save, want %v", got, err)
	}
}

func TestNoteRequestsUseConversation(t *testing.T) {
	const remember = "Please remember that I prefer short summaries."
	const forget = "Forget my preference for short summaries."
	var prompts []string
	r := runnerFunc(func(_ context.Context, prompt string) (string, error) {
		prompts = append(prompts, prompt)
		return "fixture reply", nil
	})
	dir := t.TempDir()
	e := openEngine(t, dir, r)
	acceptUpdate(t, e, 1, remember)
	runJob(t, e)
	closeEngine(t, e)
	e = openEngine(t, dir, r)
	acceptUpdate(t, e, 2, forget)
	runJob(t, e)
	for i, want := range []string{remember, forget} {
		var input struct {
			Request string `json:"user_request"`
			History []turn `json:"conversation_history"`
		}
		decodePrompt(t, prompts[i], &input)
		if input.Request != want {
			t.Fatalf("step %d received the wrong note request context: %+v", i, input)
		}
		if len(input.History) != i || i == 1 && (input.History[0].User != remember || input.History[0].Assistant != "fixture reply") {
			t.Fatalf("step %d received the wrong conversation history: %+v", i, input.History)
		}
	}
}

func decodePrompt(t *testing.T, prompt string, dst any) {
	t.Helper()
	input, ok := strings.CutPrefix(prompt, stepInstructions+"\n")
	if !ok {
		t.Fatal("model prompt has no step instructions")
	}
	if err := json.Unmarshal([]byte(input), dst); err != nil {
		t.Fatal(err)
	}
}
