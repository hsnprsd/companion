package engine

import (
	"context"
	"errors"
	"time"
)

const telegramLimit = 4096

// Deliver sends text and reactions in queue order. Return nil from send or
// react only after Telegram confirms the request. A send error with Uncertain()
// set to false permits retries unless Permanent() returns true. An error can
// supply RetryAfter() time.Duration. Do not retry an unknown result.
func (e *Engine) Deliver(ctx context.Context, send func(context.Context, string) error, react func(context.Context, int64, string) error) error {
	if send == nil || react == nil {
		return errors.New("engine requires text and reaction senders")
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
		if part.Text == "" && part.Emoji == "" {
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
		var sendErr error
		if part.Emoji != "" {
			sendErr = react(ctx, part.MessageID, part.Emoji)
		} else {
			sendErr = send(ctx, part.Text)
		}
		if err := e.finishDelivery(len(part.Text), sendErr); err != nil {
			return err
		}
	}
}

func (e *Engine) nextDelivery(ctx context.Context) (delivery, time.Duration, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failed != nil {
		return delivery{}, 0, e.failed
	}
	if err := ctx.Err(); err != nil {
		return delivery{}, 0, err
	}
	if len(e.state.Outbox) == 0 {
		return delivery{}, -1, nil
	}
	d := &e.state.Outbox[0]
	if delay := d.NotBefore.Sub(e.now()); delay > 0 {
		return delivery{}, delay, nil
	}
	part := delivery{Text: messagePart(d.Text[d.Sent:]), MessageID: d.MessageID, Emoji: d.Emoji}
	err := e.commit(func(*state) { d.Sending = true })
	return part, 0, err
}

func (e *Engine) finishDelivery(n int, sendErr error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.commit(func(s *state) {
		d := &s.Outbox[0]
		if sendErr == nil {
			if d.Emoji != "" {
				s.dropDelivery()
				return
			}
			d.Sent += n
			if d.Sent == len(d.Text) {
				s.dropDelivery()
			} else {
				d.Sending, d.Failures, d.NotBefore = false, 0, time.Time{}
			}
			return
		}
		kind := d.kind()
		var outcome interface{ Uncertain() bool }
		if !errors.As(sendErr, &outcome) || outcome.Uncertain() {
			// Do not include transport error strings: they can contain tokens.
			s.loseDelivery("Telegram did not confirm whether it received the " + kind)
			return
		}
		var permanent interface{ Permanent() bool }
		if errors.As(sendErr, &permanent) && permanent.Permanent() {
			s.reportDelivery("Telegram permanently rejected the "+kind, "A "+kind+" did not reach you")
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

func (d delivery) kind() string {
	if d.Emoji != "" {
		return "reaction"
	}
	return "message"
}

func (s *state) loseDelivery(reason string) {
	s.reportDelivery(reason, "A "+s.Outbox[0].kind()+" might not have reached you")
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
		Text:   summary + ": " + reason + ". Companion did not resend it. Use /tasks or /task ID to check results.",
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
