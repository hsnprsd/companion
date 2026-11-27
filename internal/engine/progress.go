package engine

import (
	"context"
	"strings"
)

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
