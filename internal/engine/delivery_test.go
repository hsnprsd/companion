package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type sendError struct {
	uncertain bool
	permanent bool
	retry     time.Duration
}

func (e sendError) Error() string             { return "sender failure (sensitive details)" }
func (e sendError) Uncertain() bool           { return e.uncertain }
func (e sendError) RetryAfter() time.Duration { return e.retry }
func (e sendError) Permanent() bool           { return e.permanent }

func nextSend(t *testing.T, e *Engine) (string, time.Duration) {
	t.Helper()
	part, delay, err := e.nextDelivery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return part, delay
}

func recordSend(t *testing.T, e *Engine, part string, err error) {
	t.Helper()
	if err := e.finishDelivery(len(part), err); err != nil {
		t.Fatal(err)
	}
}

func TestDefiniteRejectionRetriesAfterRestart(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	e := openEngine(t, dir, echoRunner)
	e.now = func() time.Time { return now }
	acceptUpdate(t, e, 1, "remember this")
	runJob(t, e)
	part, _ := nextSend(t, e)
	if s := stored(t, e); part == "" || !s.Outbox[0].Sending {
		t.Fatal("send started without durable outbox intent")
	}
	recordSend(t, e, part, fmt.Errorf("wrapped: %w", sendError{retry: 7 * time.Second}))
	closeEngine(t, e)
	e = openEngine(t, dir, echoRunner)
	e.now = func() time.Time { return now }
	if got, delay := nextSend(t, e); got != "" || delay != 7*time.Second {
		t.Fatalf("retry after reopen: message %q, delay %s", got, delay)
	}
	now = now.Add(7 * time.Second)
	if got, _ := nextSend(t, e); got != part {
		t.Fatalf("retried %q, want %q", got, part)
	}
	recordSend(t, e, part, nil)
	if s := stored(t, e); len(s.Outbox) != 0 || s.Offset != 2 || len(s.Incidents) != 0 {
		t.Fatalf("unexpected state after confirmed delivery: %+v", s)
	}
}

func TestAmbiguousSendDoesNotReplay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		result  error
		restart bool
	}{
		{name: "crash after send started", restart: true},
		{name: "ambiguous transport error", result: sendError{uncertain: true}},
		{name: "unclassified transport error", result: errors.New("possible credential in transport error")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			e := openEngine(t, dir, echoRunner)
			acceptUpdate(t, e, 1, "first request")
			runJob(t, e)
			acceptUpdate(t, e, 2, "/status")
			original, _ := nextSend(t, e)
			if tc.restart {
				closeEngine(t, e)
				e = openEngine(t, dir, echoRunner)
			} else {
				recordSend(t, e, original, tc.result)
			}
			notice, _ := nextSend(t, e)
			if s := stored(t, e); !strings.Contains(notice, "might not have reached you") || strings.Contains(notice, original) || strings.Contains(notice, "credential") || len(s.Incidents) != 1 || s.Incidents[0].Delivery.Text != original {
				t.Fatalf("incorrect uncertainty handling; notice=%q, incidents=%+v", notice, s.Incidents)
			}
			recordSend(t, e, notice, sendError{uncertain: true})
			if next, _ := nextSend(t, e); next != "Companion is idle." {
				t.Fatalf("unknown result for the notice blocked the queue or repeated the original: %q", next)
			}
		})
	}
}

func TestPermanentRejectionDoesNotBlockOutbox(t *testing.T) {
	e := openEngine(t, t.TempDir(), echoRunner)
	acceptUpdate(t, e, 1, "/help")
	acceptUpdate(t, e, 2, "/status")
	part, _ := nextSend(t, e)
	recordSend(t, e, part, sendError{permanent: true})
	notice, _ := nextSend(t, e)
	if !strings.Contains(notice, "permanently rejected") {
		t.Fatalf("expected a permanent rejection notice, got %q", notice)
	}
	recordSend(t, e, notice, sendError{permanent: true})
	if next, _ := nextSend(t, e); next != "Companion is idle." {
		t.Fatalf("permanent rejection blocked later messages: %q", next)
	}
}

func TestUTF16ChunksResumeWithoutReplayingConfirmedPart(t *testing.T) {
	message := "a" + strings.Repeat("😀", 2048) + "\nremaining text"
	r := runnerFunc(func(context.Context, string) (string, error) { return message, nil })
	dir := t.TempDir()
	e := openEngine(t, dir, r)
	acceptUpdate(t, e, 1, "Please talk")
	runJob(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var first string
	err := e.Deliver(ctx, func(_ context.Context, part string) error {
		first = part
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || units(first) != 4095 || !strings.HasPrefix(message, first) {
		t.Fatalf("bad chunk or loop result: %d units, %v", units(first), err)
	}
	closeEngine(t, e)
	e = openEngine(t, dir, r)
	rest, _ := nextSend(t, e)
	if rest != strings.TrimPrefix(message, first) || units(rest) > telegramLimit {
		t.Fatalf("reopened queue did not resume at the next character: %q", rest)
	}
	recordSend(t, e, rest, nil)
	if s := stored(t, e); len(s.Outbox) != 0 || len(s.Incidents) != 0 {
		t.Fatalf("queue did not drain: %+v", s)
	}
}
