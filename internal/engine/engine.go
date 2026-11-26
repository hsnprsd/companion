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

// Runner runs one model step and returns when its context ends.
type Runner interface {
	Run(context.Context, string) (string, error)
}

type activeStep struct {
	id, revision int64
	cancel       context.CancelFunc
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
	active      *activeStep
	lastTaskID  int64
	working     bool
	delivering  bool
	workWake    chan struct{}
	sendWake    chan struct{}
	now         func() time.Time
	stepTimeout time.Duration
}

// Open loads or initializes the durable snapshot.
func Open(stateDir string, runner Runner) (*Engine, error) {
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
		dir: dir, lock: lock, state: s, offset: s.Offset, lastUpdate: s.LastUpdate, runner: runner,
		workWake: make(chan struct{}, 1), sendWake: make(chan struct{}, 1),
		now: time.Now, stepTimeout: 5 * time.Minute,
	}
	interrupted := len(s.Outbox) != 0 && s.Outbox[0].Sending
	for _, t := range s.Tasks {
		interrupted = interrupted || t.Status == running
	}
	if interrupted {
		err := e.commit(func(s *state) {
			for _, t := range s.Tasks {
				if t.Status == running {
					t.Status = ready
				}
			}
			if len(s.Outbox) != 0 && s.Outbox[0].Sending {
				s.loseDelivery("service stopped before it recorded the send result")
			}
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
// in update ID order. Pass an empty string to save only the cursor.
func (e *Engine) Accept(updateID int64, text string) error {
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
	if !utf8.ValidString(text) {
		return errors.New("invalid Telegram message encoding")
	}
	err := e.commit(func(s *state) {
		s.Offset, s.LastUpdate = updateID+1, now
		if strings.TrimSpace(text) != "" {
			e.acceptText(s, text)
		}
	})
	if err == nil && e.active != nil && e.state.Tasks[e.active.id].Revision != e.active.revision {
		e.active.cancel()
	}
	return err
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
