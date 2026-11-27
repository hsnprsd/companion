package pi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMessageCallbackWaitsForSave(t *testing.T) {
	for _, saveErr := range []error{nil, errors.New("save failed")} {
		name := "success"
		if saveErr != nil {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			saving, finish := make(chan string, 1), make(chan struct{})
			status := make(chan int, 1)
			callbackPath := make(chan string, 1)
			dir := startTestRunner(t, func(ctx context.Context, step stepRequest, dir string) error {
				path := messageSocket(dir, step.ID)
				callbackPath <- path
				if got := postMessage(ctx, path, `{"text":""}`); got != http.StatusBadRequest {
					return errors.New("blank message was not rejected")
				}
				status <- postMessage(ctx, path, `{"text":"progress"}`)
				return nil
			})
			client := Client{SocketDir: dir, Provider: "test", Model: "fixture"}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err := make(chan error, 1)
			go func() {
				err <- client.Run(ctx, "test", func(text string) error {
					saving <- text
					select {
					case <-finish:
						return saveErr
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			select {
			case text := <-saving:
				if text != "progress" {
					t.Fatalf("save received %q", text)
				}
			case <-ctx.Done():
				t.Fatal("callback did not start")
			}
			select {
			case code := <-status:
				t.Fatalf("callback returned HTTP %d before save finished", code)
			case <-time.After(20 * time.Millisecond):
			}
			close(finish)
			want := http.StatusNoContent
			if saveErr != nil {
				want = http.StatusInternalServerError
			}
			if got := <-status; got != want {
				t.Errorf("got HTTP %d, want %d", got, want)
			}
			if got := <-err; !errors.Is(got, saveErr) {
				t.Errorf("got %v, want %v", got, saveErr)
			}
			if _, err := os.Stat(<-callbackPath); !os.IsNotExist(err) {
				t.Errorf("callback socket was not removed: %v", err)
			}
		})
	}
}

func TestCancelStepDisconnectsRunner(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	dir := startTestRunner(t, func(ctx context.Context, step stepRequest, dir string) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := Client{SocketDir: dir, Provider: "test", Model: "fixture"}
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx, "test", func(string) error { return nil }) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want canceled", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not cancel the step")
	}
}

func startTestRunner(t *testing.T, run func(context.Context, stepRequest, string) error) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "companion-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, dir, func(ctx context.Context, step stepRequest) error { return run(ctx, step, dir) })
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", runnerSocket(dir), 10*time.Millisecond)
		if err == nil {
			conn.Close()
			return dir
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("test runner socket did not start")
	return ""
}

func postMessage(ctx context.Context, path, body string) int {
	var dialer net.Dialer
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", path)
	}}
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://companion/message", strings.NewReader(body))
	res, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	return res.StatusCode
}
