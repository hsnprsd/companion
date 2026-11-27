package engine

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPendingRequestsSurviveRestartInOrder(t *testing.T) {
	started := make(chan struct{})
	r := runnerFunc(func(ctx context.Context, _ string, sendMessage func(string) error) error {
		close(started)
		<-ctx.Done()
		return sendMessage("discard this reply")
	})
	dir := t.TempDir()
	e := openEngine(t, dir, r)
	acceptUpdate(t, e, 1, "first")
	stop := startLoop(t, e.Work)
	receive(t, started)
	if e.Close() == nil {
		t.Fatal("released the lock during Work")
	}
	acceptUpdate(t, e, 2, "second")
	stop()
	if s := stored(t, e); len(s.Pending) != 2 || s.Pending[0].Text != "first" || len(s.History) != 0 || len(s.Outbox) != 0 {
		t.Fatalf("shutdown did not leave both requests pending: %+v", s)
	}
	closeEngine(t, e)
	runs := 0
	e = openEngine(t, dir, runnerFunc(func(_ context.Context, prompt string, sendMessage func(string) error) error {
		var input struct {
			Request string `json:"user_request"`
			History []turn `json:"conversation_history"`
		}
		decodePrompt(t, prompt, &input)
		switch runs {
		case 0:
			if input.Request != "first" || len(input.History) != 0 {
				t.Errorf("wrong prompt for the first request: %+v", input)
			}
		case 1:
			if input.Request != "second" || len(input.History) != 1 || input.History[0] != (turn{User: "first", Assistant: "reply to first"}) {
				t.Errorf("wrong prompt for the second request: %+v", input)
			}
		}
		runs++
		return sendMessage("reply to " + input.Request)
	}))
	runJob(t, e)
	runJob(t, e)
	closeEngine(t, e)
	e = openEngine(t, dir, echoRunner)
	if s := stored(t, e); jobFor(t, e) != nil || len(s.History) != 2 || outboxText(s) != "reply to first\nreply to second\n" {
		t.Fatalf("completed requests were not saved together with their replies: %+v", s)
	}
}

func TestActiveRequestRemainsOnDiskBeforeModelCompletes(t *testing.T) {
	dir := t.TempDir()
	e := openEngine(t, dir, echoRunner)
	acceptUpdate(t, e, 1, "run")
	if jobFor(t, e) == nil || len(stored(t, e).Pending) != 1 {
		t.Fatal("taking a request removed it from the saved queue")
	}
	// Close without recording a result, as after an interrupted process.
	e.active.cancel()
	closeEngine(t, e)
	e = openEngine(t, dir, echoRunner)
	runJob(t, e)
	if s := stored(t, e); len(s.Pending) != 0 || outboxText(s) != "done\n" {
		t.Fatalf("did not recover the interrupted request: %+v", s)
	}
}

func TestTimedOutRequestDoesNotBlockQueue(t *testing.T) {
	r := runnerFunc(func(ctx context.Context, prompt string, sendMessage func(string) error) error {
		if strings.Contains(prompt, `"user_request":"slow"`) {
			<-ctx.Done()
			return sendMessage("late reply")
		}
		return sendMessage("next reply")
	})
	e := openEngine(t, t.TempDir(), r)
	e.stepTimeout = time.Millisecond
	acceptUpdate(t, e, 1, "slow")
	acceptUpdate(t, e, 2, "next")
	runJob(t, e)
	s := stored(t, e)
	if len(s.Pending) != 1 || s.Pending[0].Text != "next" || len(s.History) != 1 || s.History[0] != (turn{User: "slow"}) || !strings.Contains(outboxText(s), "time limit") {
		t.Fatalf("timed out request did not leave the queue with an error reply: %+v", s)
	}
	e.stepTimeout = time.Minute
	runJob(t, e)
	if s := stored(t, e); len(s.Pending) != 0 || !strings.HasSuffix(outboxText(s), "next reply\n") {
		t.Fatalf("did not finish the next request: %+v", s)
	}
}
