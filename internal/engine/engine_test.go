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

func (r runnerFunc) Run(ctx context.Context, prompt string, _ bool) (string, string, error) {
	text, err := r(ctx, prompt)
	return text, "", err
}

var echoRunner runnerFunc = func(context.Context, string) (string, error) { return "done", nil }

func openEngine(t *testing.T, dir string, r Runner) *Engine {
	t.Helper()
	e, err := Open(dir, r)
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
	job, _, err := e.nextWork(context.Background())
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
	if other, err := Open(dir, echoRunner); err == nil {
		closeEngine(t, other)
		t.Fatal("second engine opened the same directory")
	}
	acceptUpdate(t, e, 100, "/work original")
	acceptUpdate(t, e, 100, "/work duplicate")
	acceptUpdate(t, e, 102, "")
	s := stored(t, e)
	if s.Offset != 103 || len(s.Tasks) != 1 || s.Tasks[1].Text != "original" || len(s.Outbox) != 1 {
		t.Fatalf("cursor and effects were not saved together: %+v", s)
	}
	closeEngine(t, e)
	if err := e.Accept(103, 0, "/work after close"); err == nil {
		t.Fatal("closed engine accepted an update")
	}
	e = openEngine(t, dir, echoRunner)
	if got := e.Offset(); got != 103 {
		t.Fatalf("offset after reopen = %d, want 103", got)
	}
	acceptUpdate(t, e, 101, "/work duplicate after reopen")
	now := s.LastUpdate.Add(cursorLifetime)
	e.now = func() time.Time { return now }
	if got := e.Offset(); got != 0 {
		t.Fatalf("expired offset = %d, want 0", got)
	}
	acceptUpdate(t, e, 8, "/work new sequence")
	if s := stored(t, e); s.Offset != 9 || !s.LastUpdate.Equal(now) || len(s.Tasks) != 2 {
		t.Fatalf("new sequence not saved: %+v", s)
	}
}

func TestOpenRejectsOldSnapshotWithoutChangingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	const old = `{"version":1,"tasks":{},"notebook":{},"grants":{"reference":true}}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	if e, err := Open(dir, echoRunner); err == nil {
		closeEngine(t, e)
		t.Fatal("opened a version 1 snapshot")
	} else if !strings.Contains(err.Error(), "unsupported engine state version 1") {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != old {
		t.Fatalf("version 1 state changed: %s, %v", data, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	e := openEngine(t, dir, echoRunner)
	acceptUpdate(t, e, 1, "/work start fresh")
	if s := stored(t, e); s.Version != stateVersion || len(s.Tasks) != 1 {
		t.Fatalf("did not create a new snapshot: %+v", s)
	}
}

func TestAcceptDoesNotAcknowledgeFailedSave(t *testing.T) {
	e := openEngine(t, t.TempDir(), echoRunner)
	if err := os.Mkdir(filepath.Join(e.dir, "state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := e.Accept(7, 0, "/work must not run")
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
	const legacy = `{"colour":"legacy-only-value"}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version":2,"tasks":{},"notebook":`+legacy+`}`), 0600); err != nil {
		t.Fatal(err)
	}
	e := openEngine(t, dir, r)
	acceptUpdate(t, e, 1, remember)
	runJob(t, e)
	closeEngine(t, e)
	e = openEngine(t, dir, r)
	acceptUpdate(t, e, 2, forget)
	runJob(t, e)
	for i, want := range []string{remember, forget} {
		var input struct {
			Kind    string          `json:"kind"`
			Request string          `json:"user_request"`
			History []turn          `json:"conversation_history"`
			Legacy  json.RawMessage `json:"notebook"`
		}
		decodePrompt(t, prompts[i], &input)
		if input.Kind != "conversation" || input.Request != want || len(input.Legacy) != 0 {
			t.Fatalf("step %d received the wrong note request context: %+v", i, input)
		}
		if len(input.History) != i || i == 1 && (input.History[0].User != remember || input.History[0].Assistant != "fixture reply") {
			t.Fatalf("step %d received the wrong conversation history: %+v", i, input.History)
		}
	}
	if s := stored(t, e); string(s.LegacyNotebook) != legacy {
		t.Fatal("saving new state did not preserve the retired notebook")
	}
}

func TestRecurringWorkKeepsCadenceAfterRestart(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	runs := 0
	r := runnerFunc(func(_ context.Context, _ string) (string, error) {
		runs++
		if runs == 1 {
			return "Periodic finding.", nil
		}
		return "NO_UPDATE", nil
	})
	dir := t.TempDir()
	e := openEngine(t, dir, r)
	e.now = func() time.Time { return now }
	acceptUpdate(t, e, 1, "/every 500ms too fast")
	acceptUpdate(t, e, 2, "/every 2s check")
	if s := stored(t, e); len(s.Tasks) != 1 || !s.Tasks[1].NextRun.Equal(now.Add(2*time.Second)) || jobFor(t, e) != nil {
		t.Fatalf("unexpected schedule state: %+v", s)
	}
	closeEngine(t, e)
	e = openEngine(t, dir, r)
	now = now.Add(11 * time.Second)
	e.now = func() time.Time { return now }
	runJob(t, e)
	s := stored(t, e)
	if s.Tasks[1].Status != scheduled || !s.Tasks[1].NextRun.Equal(now.Add(time.Second)) || !strings.Contains(outboxText(s), "Periodic finding.") {
		t.Fatalf("missed ticks were not skipped or feedback was not saved: %+v", s.Tasks[1])
	}
	now = now.Add(time.Second)
	before := len(s.Outbox)
	runJob(t, e)
	if s := stored(t, e); len(s.Outbox) != before || s.Tasks[1].LastResult != "Periodic finding." {
		t.Fatal("NO_UPDATE sent a reply or erased the previous finding")
	}
	acceptUpdate(t, e, 3, "/stop 1")
	now = now.Add(time.Minute)
	if jobFor(t, e) != nil {
		t.Fatal("stopped periodic task is still due")
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
