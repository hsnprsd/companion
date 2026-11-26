package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWorkShutdownRecoveryAndModelTimeout(t *testing.T) {
	t.Run("shutdown", func(t *testing.T) {
		started := make(chan struct{})
		r := runnerFunc(func(ctx context.Context, _ string) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		})
		dir := t.TempDir()
		e := openEngine(t, dir, r)
		acceptUpdate(t, e, 1, "/work run")
		stop := startLoop(t, e.Work)
		receive(t, started)
		if e.Close() == nil {
			t.Fatal("released the lock during Work")
		}
		stop()
		if s := stored(t, e); s.Tasks[1].Status != ready {
			t.Fatalf("shutdown task status = %q, want ready", s.Tasks[1].Status)
		}
		closeEngine(t, e)
		e = openEngine(t, dir, echoRunner)
		runJob(t, e)
	})
	t.Run("interrupted snapshot", func(t *testing.T) {
		dir := t.TempDir()
		e := openEngine(t, dir, echoRunner)
		acceptUpdate(t, e, 1, "/work run")
		if jobFor(t, e) == nil || stored(t, e).Tasks[1].Status != running {
			t.Fatal("model start was not persisted")
		}
		e.active.cancel()
		closeEngine(t, e)
		e = openEngine(t, dir, echoRunner)
		runJob(t, e)
	})
	t.Run("step timeout and retry", func(t *testing.T) {
		r := runnerFunc(func(ctx context.Context, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})
		dir := t.TempDir()
		e := openEngine(t, dir, r)
		e.stepTimeout = time.Millisecond
		acceptUpdate(t, e, 1, "/work run")
		runJob(t, e)
		if s := stored(t, e); s.Tasks[1].Status != blocked || !strings.Contains(s.Tasks[1].Problem, "time limit") || jobFor(t, e) != nil {
			t.Fatalf("timed out step did not block: %+v", s.Tasks[1])
		}
		closeEngine(t, e)
		e = openEngine(t, dir, echoRunner)
		acceptUpdate(t, e, 2, "/retry 1")
		runJob(t, e)
		if s := stored(t, e); s.Tasks[1].Status != complete {
			t.Fatalf("retry did not finish: %+v", s.Tasks[1])
		}
	})
}

func TestChangeCancelsStepAndDiscardsOldResult(t *testing.T) {
	started := make(chan struct{})
	r := runnerFunc(func(ctx context.Context, prompt string) (string, error) {
		var input struct {
			Request string `json:"user_request"`
		}
		decodePrompt(t, prompt, &input)
		if input.Request == "old" {
			close(started)
			<-ctx.Done()
			return "stale result", nil
		}
		return "new result", nil
	})
	e := openEngine(t, t.TempDir(), r)
	acceptUpdate(t, e, 1, "/work old")
	job := jobFor(t, e)
	done := make(chan error, 1)
	go func() { done <- job() }()
	receive(t, started)
	acceptUpdate(t, e, 2, "/change 1 new")
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	runJob(t, e)
	s := stored(t, e)
	if s.Tasks[1].LastResult != "new result" || strings.Contains(outboxText(s), "stale result") {
		t.Fatalf("unexpected task state: %+v", s.Tasks[1])
	}
}
