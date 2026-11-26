// Package engine coordinates durable work for one private chat.
package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// Runner runs one model step. Return an emoji only if canReact is true.
// Call progress at most once during Run, with a model-written update, and
// return any callback error.
type Runner interface {
	Run(ctx context.Context, prompt string, canReact bool, progress func(string) error) (text, emoji string, err error)
}

type activeStep struct {
	cancel context.CancelFunc
}

// Engine owns the snapshot and serializes all durable state changes.
type Engine struct {
	mu    sync.Mutex
	dir   string
	lock  *os.File
	state state
	// Keep the last confirmed cursor even if a commit fails.
	offset      int64
	lastUpdate  time.Time
	failed      error
	runner      Runner
	typing      func(context.Context) error
	active      *activeStep
	working     bool
	delivering  bool
	workWake    chan struct{}
	sendWake    chan struct{}
	now         func() time.Time
	stepTimeout time.Duration
}

// Open loads or initializes the durable snapshot. Pass nil to omit typing
// indicators. A typing sender must return when its context is canceled.
func Open(stateDir string, runner Runner, typing func(context.Context) error) (*Engine, error) {
	if runner == nil || stateDir == "" {
		return nil, errors.New("engine requires a state directory and runner")
	}
	dir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	if err := ensureStateDir(dir); err != nil {
		return nil, fmt.Errorf("create engine state directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open engine state lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("lock engine state directory: %w", err)
	}
	s, err := load(dir)
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("load engine state: %w", err)
	}
	e := &Engine{
		dir: dir, lock: lock, state: s, offset: s.Offset, lastUpdate: s.LastUpdate, runner: runner, typing: typing,
		workWake: make(chan struct{}, 1), sendWake: make(chan struct{}, 1),
		now: time.Now, stepTimeout: 5 * time.Minute,
	}
	if len(s.Outbox) != 0 && s.Outbox[0].Sending {
		err := e.commit(func(s *state) {
			s.loseDelivery("service stopped before it recorded the send result")
		})
		if err != nil {
			lock.Close()
			return nil, err
		}
	}
	return e, nil
}

// Close releases the state directory lock. Stop Work and Deliver first.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.working || e.delivering {
		return errors.New("stop engine loops before closing the state directory")
	}
	if e.lock == nil {
		return nil
	}
	err := e.lock.Close()
	e.lock = nil
	return err
}

const cursorLifetime = 6 * 24 * time.Hour

func (e *Engine) nextOffset(now time.Time) int64 {
	if !e.lastUpdate.IsZero() && now.Sub(e.lastUpdate) >= cursorLifetime {
		return 0
	}
	return e.offset
}

// Offset returns the next update ID to request from Telegram. After six days
// without new updates, return 0 because Telegram can start a new ID sequence.
func (e *Engine) Offset() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nextOffset(e.now())
}

// Accept saves an update and its effects before returning success. Call it
// in update ID order. Pass empty text to save only the cursor and a message ID
// of zero if there is no reaction target.
func (e *Engine) Accept(updateID, messageID int64, text string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failed != nil {
		return e.failed
	}
	if e.lock == nil {
		return errors.New("engine is closed")
	}
	if updateID < 0 || updateID == math.MaxInt64 {
		return errors.New("invalid Telegram update ID")
	}
	now := e.now()
	if updateID < e.nextOffset(now) {
		return nil
	}
	if messageID < 0 {
		return errors.New("invalid Telegram message ID")
	}
	if !utf8.ValidString(text) {
		return errors.New("invalid Telegram message encoding")
	}
	return e.commit(func(s *state) {
		s.Offset, s.LastUpdate = updateID+1, now
		if strings.TrimSpace(text) != "" {
			e.acceptText(s, messageID, text)
		}
	})
}

func wake(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func pause(ctx context.Context, wake <-chan struct{}, delay time.Duration) {
	var timer <-chan time.Time
	if delay >= 0 {
		t := time.NewTimer(delay)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-ctx.Done():
	case <-wake:
	case <-timer:
	}
}
