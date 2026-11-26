package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type reactionRunner func(context.Context, string, bool) (string, string, error)

func (r reactionRunner) Run(ctx context.Context, prompt string, canReact bool, _ func(string) error) (string, string, error) {
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
	if s := stored(t, e); len(s.Pending) != 0 || s.History[0].Assistant != "Requested reaction: 👍" || len(s.Outbox) != 1 {
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

func TestRequestWithoutMessageIDCannotReact(t *testing.T) {
	r := reactionRunner(func(_ context.Context, _ string, canReact bool) (string, string, error) {
		if canReact {
			t.Error("reaction tool available without a message ID")
		}
		return "Reply", "👍", nil
	})
	e := openEngine(t, t.TempDir(), r)
	acceptUpdate(t, e, 1, "React and reply")
	runJob(t, e)
	if s := stored(t, e); len(s.Outbox) != 1 || s.Outbox[0].Emoji != "" || s.Outbox[0].Text != "Reply" {
		t.Fatalf("expected only a reply: %+v", s.Outbox)
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
