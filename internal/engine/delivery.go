package engine

import (
	"context"
	"errors"
	"time"
)

const telegramLimit = 4096

// Deliver sends text in queue order. Return nil from send only after Telegram
// confirms the request. A send error with Uncertain() set to false permits
// retries unless Permanent() returns true. An error can supply RetryAfter()
// time.Duration. Do not retry an unknown result.
func (e *Engine) Deliver(ctx context.Context, send func(context.Context, string) error) error {
	if send == nil {
		return errors.New("engine requires a text sender")
	}
	e.mu.Lock()
	if e.delivering || e.lock == nil {
		e.mu.Unlock()
		return errors.New("engine is closed or Deliver is already running")
	}
	e.delivering = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.delivering = false
		e.mu.Unlock()
	}()
	for {
		part, delay, err := e.nextDelivery(ctx)
		if err != nil {
			return err
		}
		if part == "" {
			pause(ctx, e.sendWake, delay)
			continue
		}
		if err := ctx.Err(); err != nil {
			e.mu.Lock()
			saveErr := e.commit(func(s *state) { s.Outbox[0].Sending = false })
			e.mu.Unlock()
			if saveErr != nil {
				return saveErr
			}
			return err
		}
		sendErr := send(ctx, part)
		if err := e.finishDelivery(len(part), sendErr); err != nil {
			return err
		}
	}
}

func (e *Engine) nextDelivery(ctx context.Context) (string, time.Duration, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failed != nil {
		return "", 0, e.failed
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if len(e.state.Outbox) == 0 {
		return "", -1, nil
	}
	d := &e.state.Outbox[0]
	if delay := d.NotBefore.Sub(e.now()); delay > 0 {
		return "", delay, nil
	}
	part := messagePart(d.Text[d.Sent:])
	err := e.commit(func(*state) { d.Sending = true })
	return part, 0, err
}

func (e *Engine) finishDelivery(n int, sendErr error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.commit(func(s *state) {
		d := &s.Outbox[0]
		if sendErr == nil {
			d.Sent += n
			if d.Sent == len(d.Text) {
				s.dropDelivery()
			} else {
				d.Sending, d.Failures, d.NotBefore = false, 0, time.Time{}
			}
			return
		}
		var outcome interface{ Uncertain() bool }
		if !errors.As(sendErr, &outcome) || outcome.Uncertain() {
			// Do not include transport error strings: they can contain tokens.
			s.loseDelivery("Telegram did not confirm whether it received the message")
			return
		}
		var permanent interface{ Permanent() bool }
		if errors.As(sendErr, &permanent) && permanent.Permanent() {
			s.reportDelivery("Telegram permanently rejected the message", "A message did not reach you")
			return
		}
		d.Failures = min(d.Failures+1, 7)
		wait := min(time.Second<<(d.Failures-1), time.Minute)
		var retry interface{ RetryAfter() time.Duration }
		if errors.As(sendErr, &retry) {
			wait = max(wait, retry.RetryAfter())
		}
		d.NotBefore, d.Sending = e.now().Add(wait), false
	})
}

func (s *state) dropDelivery() {
	s.Outbox[0] = delivery{}
	s.Outbox = s.Outbox[1:]
}

func (s *state) loseDelivery(reason string) {
	s.reportDelivery(reason, "A message might not have reached you")
}

func (s *state) reportDelivery(reason, summary string) {
	d := s.Outbox[0]
	s.Incidents = append(s.Incidents, incident{Delivery: d, Reason: reason})
	if d.Notice {
		// Do not generate another notice if a notice might have failed.
		s.dropDelivery()
		return
	}
	s.Outbox[0] = delivery{
		Text:   summary + ": " + reason + ". Companion did not resend it. If you missed a reply, ask Companion to repeat it.",
		Notice: true,
	}
}

func runeUnits(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

func units(text string) (n int) {
	for _, r := range text {
		n += runeUnits(r)
	}
	return n
}

func messagePart(text string) string {
	n := 0
	for i, r := range text {
		n += runeUnits(r)
		if n > telegramLimit {
			return text[:i]
		}
	}
	return text
}
