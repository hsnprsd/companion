package engine

import (
	"context"
	"strings"
	"time"
)

const typingInterval = 4 * time.Second

// startTyping refreshes the typing indicator until ctx is canceled. Cancel ctx
// before waiting for the returned channel to close.
func (e *Engine) startTyping(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if e.typing == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		tick := time.NewTicker(typingInterval)
		defer tick.Stop()
		for ctx.Err() == nil {
			// A typing error does not affect the step or the durable outbox.
			_ = e.typing(ctx)
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return done
}

func (e *Engine) queueProgress(ctx context.Context, step *activeStep, text string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.active != step || strings.TrimSpace(text) == "" {
		return nil
	}
	return e.commit(func(s *state) { s.say(text) })
}
