package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
)

func TestWorkDeliversModelMessagesInOrderBeforeTurnEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		messages := []string{"I will check the workspace.", "I found two files.", "Full answer"}
		finish := make(chan struct{})
		r := runnerFunc(func(ctx context.Context, _ string, sendMessage func(string) error) error {
			for _, text := range messages {
				if err := sendMessage(text); err != nil {
					return err
				}
			}
			select {
			case <-finish:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		e := openEngine(t, t.TempDir(), r)
		sent := make(chan string, len(messages))
		stopDelivery := startLoop(t, func(ctx context.Context) error {
			return e.Deliver(ctx, func(_ context.Context, text string) error {
				sent <- text
				return nil
			})
		})
		defer stopDelivery()
		acceptUpdate(t, e, 1, "Summarize the workspace")
		stopWork := startLoop(t, e.Work)
		defer stopWork()
		for _, want := range messages {
			if got := receive(t, sent); got != want {
				t.Fatalf("message = %q, want %q", got, want)
			}
		}
		if s := stored(t, e); len(s.Pending) != 1 || len(s.History) != 0 {
			t.Fatal("sending messages completed the request before the turn ended")
		}
		close(finish)
		synctest.Wait()
		if s := stored(t, e); len(s.Pending) != 0 || len(s.History) != 1 || s.History[0].Assistant != strings.Join(messages, "\n\n") {
			t.Fatalf("messages not saved in order: %+v", s)
		}
		if len(sent) != 0 {
			t.Fatal("ending the turn sent a duplicate message")
		}
	})
}

func TestWorkKeepsQueuedMessagesOnModelError(t *testing.T) {
	r := runnerFunc(func(_ context.Context, _ string, sendMessage func(string) error) error {
		if err := sendMessage("I started the work."); err != nil {
			return err
		}
		return errors.New("model failed")
	})
	e := openEngine(t, t.TempDir(), r)
	acceptUpdate(t, e, 1, "run")
	runJob(t, e)
	s := stored(t, e)
	if len(s.Pending) != 0 || len(s.History) != 1 || s.History[0].Assistant != "I started the work." {
		t.Fatalf("wrong history after model failure: %+v", s)
	}
	if len(s.Outbox) != 2 || s.Outbox[0].Text != "I started the work." || !strings.Contains(s.Outbox[1].Text, "model failed") {
		t.Fatalf("wrong outbox after model failure: %+v", s.Outbox)
	}
}

func TestWorkReportsACompletedTurnWithNoMessages(t *testing.T) {
	r := runnerFunc(func(context.Context, string, func(string) error) error { return nil })
	e := openEngine(t, t.TempDir(), r)
	acceptUpdate(t, e, 1, "run")
	runJob(t, e)
	s := stored(t, e)
	if len(s.Outbox) != 1 || !strings.Contains(s.Outbox[0].Text, "the model did not send a message") {
		t.Fatalf("wrong outbox after a turn with no messages: %+v", s.Outbox)
	}
}

func TestShutdownDiscardsLateModelMessages(t *testing.T) {
	started := make(chan struct{})
	r := runnerFunc(func(ctx context.Context, _ string, sendMessage func(string) error) error {
		close(started)
		<-ctx.Done()
		if err := sendMessage("Late message"); !errors.Is(err, context.Canceled) {
			t.Errorf("sendMessage after cancellation returned %v", err)
		}
		return ctx.Err()
	})
	e := openEngine(t, t.TempDir(), r)
	acceptUpdate(t, e, 1, "run")
	stopWork := startLoop(t, e.Work)
	receive(t, started)
	stopWork()
	if s := stored(t, e); len(s.Outbox) != 0 || len(s.Pending) != 1 {
		t.Fatalf("canceled request queued a reply or was removed: %+v", s)
	}
}
