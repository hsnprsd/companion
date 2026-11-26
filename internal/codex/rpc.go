package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/coder/websocket"
)

const maxMessageBytes = 8 << 20

// App-server uses JSON-RPC messages without a jsonrpc field.
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type session struct {
	ws   *websocket.Conn
	in   chan incoming
	stop context.CancelFunc
	next int
}

type incoming struct {
	message message
	err     error
}

func connect(ctx context.Context, endpoint string) (*session, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("codex: URL must be a ws or wss endpoint without user information or a fragment")
	}

	header := make(http.Header)
	if path, configured := os.LookupEnv("CODEX_WS_TOKEN_FILE"); configured {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("codex: read CODEX_WS_TOKEN_FILE: %w", err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" || strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r >= 127 }) {
			return nil, errors.New("codex: invalid WebSocket bearer token")
		}
		header.Set("Authorization", "Bearer "+token)
	}

	// Do not send the runner credential through an environment proxy or redirect.
	ws, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPHeader: header,
		HTTPClient: &http.Client{
			Transport:     &http.Transport{},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if resp != nil {
			return nil, fmt.Errorf("codex: WebSocket handshake failed (HTTP %d)", resp.StatusCode)
		}
		// A dial error can contain the complete URL or request headers.
		return nil, errors.New("codex: could not connect to app-server WebSocket")
	}
	ws.SetReadLimit(maxMessageBytes)

	// Keep the read alive on Run cancellation so Run can interrupt a known turn.
	readCtx, stop := context.WithCancel(context.Background())
	s := &session{ws: ws, stop: stop, in: make(chan incoming)}
	go s.read(readCtx)
	return s, nil
}

func (s *session) close() {
	s.stop()
	_ = s.ws.CloseNow()
}

func (s *session) read(ctx context.Context) {
	defer close(s.in)
	for {
		kind, data, err := s.ws.Read(ctx)
		var m message
		switch {
		case err != nil:
			err = fmt.Errorf("codex: read app-server WebSocket: %w", err)
		case kind != websocket.MessageText:
			err = errors.New("codex: expected a WebSocket text message")
		case json.Unmarshal(data, &m) != nil || (m.Method == "" && !validID(m.ID)):
			err = errors.New("codex: invalid app-server RPC message")
		case len(m.ID) != 0 && !validID(m.ID):
			err = errors.New("codex: invalid app-server RPC ID")
		}
		select {
		case s.in <- incoming{m, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return false
	}
	if id[0] == '"' {
		return true // The envelope is already valid JSON.
	}
	var n int64
	return (id[0] == '-' || id[0] >= '0' && id[0] <= '9') && json.Unmarshal(id, &n) == nil
}

func (s *session) receive(ctx context.Context) (message, error) {
	select {
	case <-ctx.Done():
		return message{}, ctx.Err()
	case event, ok := <-s.in:
		if ctx.Err() != nil {
			return message{}, ctx.Err()
		}
		if !ok {
			return message{}, errors.New("codex: app-server disconnected")
		}
		return event.message, event.err
	}
}

func (s *session) send(ctx context.Context, m message) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := s.ws.Write(ctx, websocket.MessageText, data); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("codex: write app-server WebSocket: %w", err)
	}
	return nil
}

func (s *session) reject(ctx context.Context, m message, code int, reason string) error {
	return s.send(ctx, message{ID: m.ID, Error: &rpcError{Code: code, Message: reason}})
}

// Only one client request is outstanding. Dispatch interleaved notifications
// and server requests before reading its reply.
func (s *session) request(ctx context.Context, method string, params any, out any, dispatch func(message) error) error {
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	s.next++
	id := fmt.Sprint(s.next)
	if err := s.send(ctx, message{ID: json.RawMessage(id), Method: method, Params: data}); err != nil {
		return err
	}
	for {
		m, err := s.receive(ctx)
		if err != nil {
			return err
		}
		switch {
		case m.Method != "":
			if dispatch != nil {
				err = dispatch(m)
			} else if len(m.ID) != 0 {
				err = s.reject(ctx, m, -32601, "server request not allowed")
			}
			if err != nil {
				return err
			}
		case string(m.ID) == id:
			if m.Error != nil {
				return fmt.Errorf("codex: %s: %s (RPC %d)", method, m.Error.Message, m.Error.Code)
			}
			if len(m.Result) == 0 || string(m.Result) == "null" || json.Unmarshal(m.Result, out) != nil {
				return fmt.Errorf("codex: invalid %s result", method)
			}
			return nil
		}
	}
}
