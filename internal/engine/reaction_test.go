package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type reactionRunner func(context.Context, string, bool) (string, string, error)

func (r reactionRunner) Run(ctx context.Context, prompt string, canReact bool) (string, string, error) {
	return r(ctx, prompt, canReact)
}

func reactToMessage(t *testing.T, e *Engine, updateID, messageID int64, text string) {
	t.Helper()
	if err := e.Accept(updateID, messageID, text); err != nil {
		t.Fatal(err)
	}
}

func TestReactionOnlySurvivesRestart(t *testing.T) {
	r := reactionRunner(func(_ context.Context, _ string, canReact bool) (string, string, error) {
		if !canReact {
			t.Error("reaction tool unavailable for a conversation")
		}
		return "", "👍", nil
	})
	dir := t.TempDir()
	e := openEngine(t, dir, r)
	reactToMessage(t, e, 100, 35, "Please acknowledge this")
	closeEngine(t, e)
	e = openEngine(t, dir, r)
	runJob(t, e)
	if s := stored(t, e); s.Tasks[1].Status != complete || s.History[0].Assistant != "Requested reaction: 👍" || len(s.Outbox) != 1 {
		t.Fatalf("reaction did not complete the conversation: %+v", s)
	}
	closeEngine(t, e)
	e = openEngine(t, dir, r)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := e.Deliver(ctx, func(_ context.Context, text string) error {
		t.Errorf("unexpected text: %q", text)
		cancel()
		return nil
	}, func(_ context.Context, id int64, emoji string) error {
		if s := stored(t, e); id != 35 || emoji != "👍" || !s.Outbox[0].Sending {
			t.Errorf("wrong reaction target or missing durable send marker: %d %s, %+v", id, emoji, s.Outbox)
		}
		cancel()
		return nil
	})
	if s := stored(t, e); !errors.Is(err, context.Canceled) || len(s.Outbox) != 0 || len(s.Incidents) != 0 {
		t.Fatalf("reaction did not confirm: %v, %+v", err, s)
	}
}

func TestReactionFromReplacedStepIsDiscarded(t *testing.T) {
	started := make(chan struct{})
	r := reactionRunner(func(ctx context.Context, prompt string, _ bool) (string, string, error) {
		if strings.Contains(prompt, `"user_request":"old"`) {
			close(started)
			<-ctx.Done()
		}
		return "", "👍", nil
	})
	e := openEngine(t, t.TempDir(), r)
	reactToMessage(t, e, 1, 10, "/work old")
	job := jobFor(t, e)
	done := make(chan error, 1)
	go func() { done <- job() }()
	receive(t, started)
	reactToMessage(t, e, 2, 11, "/change 1 new")
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	runJob(t, e)
	if s := stored(t, e); len(s.Outbox) != 3 || s.Outbox[2].MessageID != 11 || s.Outbox[2].Emoji != "👍" {
		t.Fatalf("wrong reaction after /change: %+v", s.Outbox)
	}
}

func TestPeriodicStepHasNoReactionTarget(t *testing.T) {
	r := reactionRunner(func(_ context.Context, _ string, canReact bool) (string, string, error) {
		if canReact {
			t.Error("recurring task can react")
		}
		return "NO_UPDATE", "👍", nil
	})
	e := openEngine(t, t.TempDir(), r)
	now := time.Now()
	e.now = func() time.Time { return now }
	reactToMessage(t, e, 1, 10, "/every 1s Check the workspace")
	now = now.Add(time.Second)
	runJob(t, e)
	if s := stored(t, e); len(s.Outbox) != 1 || s.Outbox[0].Emoji != "" {
		t.Fatalf("recurring task queued a reaction: %+v", s.Outbox)
	}
}

func TestUnconfirmedReactionIsNotResentAfterRestart(t *testing.T) {
	r := reactionRunner(func(context.Context, string, bool) (string, string, error) {
		return "Reply", "👍", nil
	})
	dir := t.TempDir()
	e := openEngine(t, dir, r)
	reactToMessage(t, e, 1, 35, "Please react and reply")
	runJob(t, e)
	part, _, err := e.nextDelivery(t.Context())
	if err != nil || part.Emoji != "👍" {
		t.Fatalf("next delivery: %+v, %v", part, err)
	}
	closeEngine(t, e)
	e = openEngine(t, dir, r)
	notice, _ := nextSend(t, e)
	if s := stored(t, e); !strings.Contains(notice, "A reaction might not have reached you") || len(s.Incidents) != 1 || s.Incidents[0].Delivery.MessageID != 35 {
		t.Fatalf("incorrect recovery: %+v", s)
	}
	recordSend(t, e, notice, nil)
	if next, _ := nextSend(t, e); next != "Reply" {
		t.Fatalf("uncertain reaction blocked the reply: %q", next)
	}
}
