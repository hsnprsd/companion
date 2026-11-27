package engine

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

type progressRunner func(context.Context, func(string) error) (string, error)

func (r progressRunner) Run(ctx context.Context, _ string, progress func(string) error) (string, error) {
	return r(ctx, progress)
}

func TestWorkDeliversModelProgressAndThenResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const update = "I will inspect the workspace and summarize the files."
		started, report, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
		r := progressRunner(func(ctx context.Context, progress func(string) error) (string, error) {
			close(started)
			select {
			case <-report:
				if err := progress(update); err != nil {
					return "", err
				}
			case <-ctx.Done():
				return "", ctx.Err()
			}
			select {
			case <-finish:
				return "Full answer", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
		e := openEngine(t, t.TempDir(), r)
		sent := make(chan string, 10)
		stopDelivery := startLoop(t, func(ctx context.Context) error {
			return e.Deliver(ctx, func(_ context.Context, text string) error {
				sent <- text
				return nil
			})
		})
		defer stopDelivery()
		wantMessage := func(want string) {
			t.Helper()
			if got := receive(t, sent); got != want {
				t.Fatalf("message = %q, want %q", got, want)
			}
		}
		acceptUpdate(t, e, 1, "/status")
		wantMessage("Companion is idle.")
		acceptUpdate(t, e, 2, "Summarize the workspace")
		acceptUpdate(t, e, 3, "/status")
		wantMessage("Companion is ready to start work.\nRequests in queue: 1.")
		stopWork := startLoop(t, e.Work)
		defer stopWork()
		receive(t, started)
		acceptUpdate(t, e, 4, "/status")
		wantMessage("Companion is working on a request.")
		time.Sleep(15 * time.Second)
		synctest.Wait()
		if len(sent) != 0 {
			t.Fatalf("automatic replies before model update = %d, want 0", len(sent))
		}
		close(report)
		wantMessage(update)
		if s := stored(t, e); len(s.Pending) != 1 || len(s.History) != 0 {
			t.Fatal("progress reply changed the pending requests or conversation history")
		}
		close(finish)
		wantMessage("Full answer")
		if s := stored(t, e); len(s.History) != 1 || s.History[0].Assistant != "Full answer" {
			t.Fatal("the final answer was not saved")
		}
	})
}

func TestShutdownDiscardsLateProgress(t *testing.T) {
	started := make(chan struct{})
	r := progressRunner(func(ctx context.Context, progress func(string) error) (string, error) {
		close(started)
		<-ctx.Done()
		if err := progress("Late update"); !errors.Is(err, context.Canceled) {
			t.Errorf("progress after cancellation returned %v", err)
		}
		return "", ctx.Err()
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
