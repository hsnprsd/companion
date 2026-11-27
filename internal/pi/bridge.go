// Package pi connects Companion to a separate container that runs Pi RPC.
package pi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	DefaultSocketDir = "/run/companion"
	maxPromptRequest = 16 << 20
	maxText          = 64 << 10
	maxToolRequest   = 8 * maxText // Allow JSON escapes.
)

var stepIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Client struct {
	SocketDir, Provider, Model, ThinkingLevel string
}

type stepRequest struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinkingLevel"`
	Prompt        string `json:"prompt"`
}

type messageRequest struct {
	Text string `json:"text"`
}

func runnerSocket(dir string) string      { return filepath.Join(dir, "runner.sock") }
func messageSocket(dir, id string) string { return filepath.Join(dir, "msg-"+id+".sock") }

// Run accepts send_message calls only on the socket for this step.
func (c Client) Run(ctx context.Context, prompt string, sendMessage func(string) error) error {
	var id [16]byte
	rand.Read(id[:])
	step := stepRequest{hex.EncodeToString(id[:]), c.Provider, c.Model, c.ThinkingLevel, prompt}
	var mu sync.Mutex
	active := true
	var callbackErr error
	messages := http.NewServeMux()
	messages.HandleFunc("POST /message", func(w http.ResponseWriter, r *http.Request) {
		var msg messageRequest
		if !readRequest(w, r, &msg, maxToolRequest) || len(msg.Text) > maxText || strings.TrimSpace(msg.Text) == "" {
			http.Error(w, "invalid message", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if !active || ctx.Err() != nil || r.Context().Err() != nil {
			http.Error(w, "step ended", http.StatusGone)
			return
		}
		if callbackErr == nil {
			callbackErr = sendMessage(msg.Text)
		}
		if callbackErr != nil {
			http.Error(w, "message was not queued", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	listener, err := net.Listen("unix", messageSocket(c.SocketDir, step.ID))
	if err != nil {
		return fmt.Errorf("open Pi message socket: %w", err)
	}
	defer listener.Close()
	server := &http.Server{Handler: messages, ReadTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()

	runErr := requestStep(ctx, runnerSocket(c.SocketDir), step)
	mu.Lock()
	defer mu.Unlock()
	active = false
	if callbackErr != nil {
		return callbackErr
	}
	return runErr
}

func requestStep(ctx context.Context, socket string, step stepRequest) error {
	data, err := json.Marshal(step)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://companion/run", bytes.NewReader(data))
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socket)
	}, DisableKeepAlives: true}
	res, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("could not reach the Pi runner")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNoContent {
		return nil
	}
	var result struct{ Error string }
	if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&result) == nil && result.Error != "" {
		return errors.New(result.Error)
	}
	return fmt.Errorf("Pi runner returned HTTP %d", res.StatusCode)
}

// Serve listens for steps in the runner container. Run at most one Pi process at a time.
func Serve(ctx context.Context, dir, workspace, extension string) error {
	return serve(ctx, dir, func(ctx context.Context, step stepRequest) error {
		return runPi(ctx, "pi", workspace, extension, messageSocket(dir, step.ID), step)
	})
}

func serve(ctx context.Context, dir string, run func(context.Context, stepRequest) error) error {
	path := runnerSocket(dir)
	listener, err := listenRunner(path)
	if err != nil {
		return fmt.Errorf("open Pi runner socket: %w", err)
	}
	mux := http.NewServeMux()
	slot := make(chan struct{}, 1)
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		var step stepRequest
		if !readRequest(w, r, &step, maxPromptRequest) || !stepIDPattern.MatchString(step.ID) ||
			step.Provider == "" || step.Model == "" || step.Prompt == "" {
			http.Error(w, "invalid Pi step", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		select {
		case slot <- struct{}{}:
			defer func() { <-slot }()
		case <-ctx.Done():
			return
		}
		if err := run(ctx, step); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(struct{ Error string }{err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{
		Handler: mux, ReadTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	// Shutdown closes the listener and lets active handlers reap their Pi processes.
	stop := make(chan struct{})
	defer close(stop)
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done <- server.Shutdown(shutdown)
		case <-stop:
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return <-done
	}
	return err
}

func listenRunner(path string) (net.Listener, error) {
	listener, err := net.Listen("unix", path)
	if !errors.Is(err, syscall.EADDRINUSE) {
		return listener, err
	}
	// Recover the socket left by a runner that did not exit cleanly.
	info, statErr := os.Lstat(path)
	if statErr != nil || info.Mode()&os.ModeSocket == 0 {
		return nil, err
	}
	conn, dialErr := net.DialTimeout("unix", path, time.Second)
	if dialErr == nil {
		conn.Close()
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return nil, err
	}
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	return net.Listen("unix", path)
}

func readRequest(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	return d.Decode(dst) == nil && d.Decode(new(json.RawMessage)) == io.EOF
}
