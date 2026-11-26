// Command fixture serves the local Docker peers and drives their test controls.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	baseURL = "http://127.0.0.1:8080"
	botPath = "/bot123456789:docker_fixture_token_123456789/"
	chatID  = 9876543210
)

type update struct {
	ID      int64 `json:"update_id"`
	Message struct {
		ID   int64  `json:"message_id"`
		Text string `json:"text"`
		Chat struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	} `json:"message"`
}

type transcript struct {
	Offset    int64    `json:"offset"`
	Messages  []string `json:"messages"`
	Reactions []string `json:"reactions"`
	Actions   []string `json:"actions"`
	Turns     []string `json:"turns"`
	Problems  []string `json:"problems"`
}

type fixture struct {
	mu sync.Mutex
	transcript
	pending []update
	replay  []update
}

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if slices.Equal(args, []string{"serve"}) || slices.Equal(args, []string{"serve-telegram"}) {
		f := new(fixture)
		mux := http.NewServeMux()
		mux.HandleFunc("POST "+botPath+"getUpdates", f.poll)
		mux.HandleFunc("POST "+botPath+"sendMessage", f.message)
		mux.HandleFunc("POST "+botPath+"setMessageReaction", f.reaction)
		mux.HandleFunc("POST "+botPath+"sendChatAction", f.action)
		mux.HandleFunc("POST /updates", f.enqueue)
		mux.HandleFunc("GET /status", f.status)
		if args[0] == "serve" {
			ws := &http.Server{Addr: "127.0.0.1:4500", Handler: http.HandlerFunc(f.codex), ReadHeaderTimeout: 5 * time.Second}
			go func() { log.Fatal(ws.ListenAndServe()) }()
		}
		s := &http.Server{Addr: "127.0.0.1:8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		return s.ListenAndServe()
	}
	if len(args) != 0 {
		switch args[0] {
		case "status":
			if len(args) == 1 {
				st, err := snapshot()
				if err != nil {
					return err
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			}
		case "send", "replay":
			if len(args) == 5 {
				var u update
				var err error
				u.ID, err = strconv.ParseInt(args[1], 10, 64)
				if err != nil {
					return err
				}
				u.Message.Chat.ID, err = strconv.ParseInt(args[2], 10, 64)
				if err != nil {
					return err
				}
				u.Message.ID = u.ID + 1000 // Distinguish message IDs from update IDs.
				u.Message.Chat.Type, u.Message.Text = args[3], args[4]
				return control("POST", "/updates", envelope{u, args[0] == "replay"}, nil)
			}
		case "offset":
			if len(args) == 2 {
				want, err := strconv.ParseInt(args[1], 10, 64)
				if err != nil {
					return err
				}
				return wait("poll offset "+args[1], func(st transcript) bool { return st.Offset >= want })
			}
		case "count", "wait":
			if (args[0] == "count" && len(args) == 3) || (args[0] == "wait" && len(args) == 4) {
				kind, fragment := args[1], args[2]
				if kind != "messages" && kind != "reactions" && kind != "actions" && kind != "turns" {
					return fmt.Errorf("unknown transcript field: %s", kind)
				}
				if args[0] == "count" {
					st, err := snapshot()
					if err != nil {
						return err
					}
					fmt.Println(st.count(kind, fragment))
					return nil
				}
				want, err := strconv.Atoi(args[3])
				if err != nil {
					return err
				}
				return wait(fmt.Sprintf("%d %s containing %q", want, kind, fragment), func(st transcript) bool {
					return st.count(kind, fragment) >= want
				})
			}
		}
	}
	return errors.New("use serve, serve-telegram, status, send/replay ID CHAT TYPE TEXT, offset ID, count KIND SUBSTRING, or wait KIND SUBSTRING COUNT")
}

type envelope struct {
	Update update `json:"update"`
	Replay bool   `json:"replay"`
}

func (f *fixture) enqueue(w http.ResponseWriter, r *http.Request) {
	var e envelope
	if err := decode(r, &e); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.Replay {
		f.replay = append(f.replay, e.Update)
	} else {
		f.pending = append(f.pending, e.Update)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fixture) poll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Offset  int64 `json:"offset"`
		Timeout int   `json:"timeout"`
	}
	if err := decode(r, &req); err != nil || req.Timeout <= 0 {
		f.problem("invalid getUpdates request or no long-poll timeout")
		http.Error(w, "invalid getUpdates", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	if req.Offset < f.Offset {
		f.Problems = append(f.Problems, fmt.Sprintf("Telegram offset regressed from %d to %d", f.Offset, req.Offset))
	}
	f.Offset = req.Offset
	f.pending = slices.DeleteFunc(f.pending, func(u update) bool { return u.ID < req.Offset })
	f.mu.Unlock()

	// Use a short fixture long poll. Keep normal updates until the app acks them.
	timeout := time.NewTimer(250 * time.Millisecond)
	defer timeout.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		f.mu.Lock()
		batch := append(slices.Clone(f.pending), f.replay...)
		f.replay = nil
		f.mu.Unlock()
		if len(batch) != 0 {
			respond(w, map[string]any{"ok": true, "result": batch})
			return
		}
		select {
		case <-tick.C:
		case <-timeout.C:
			respond(w, map[string]any{"ok": true, "result": []update{}})
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (f *fixture) message(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Chat int64  `json:"chat_id"`
		Text string `json:"text"`
	}
	if err := decode(r, &req); err != nil || req.Chat != chatID || req.Text == "" {
		f.problem("invalid sendMessage request or wrong destination chat")
		http.Error(w, "invalid sendMessage", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.Messages = append(f.Messages, req.Text)
	n := len(f.Messages)
	f.mu.Unlock()
	respond(w, map[string]any{"ok": true, "result": map[string]int{"message_id": n}})
}

func (f *fixture) reaction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Chat      int64 `json:"chat_id"`
		MessageID int64 `json:"message_id"`
		Reaction  []struct {
			Type  string `json:"type"`
			Emoji string `json:"emoji"`
		} `json:"reaction"`
	}
	if err := decode(r, &req); err != nil || req.Chat != chatID || req.MessageID <= 0 || len(req.Reaction) != 1 || req.Reaction[0].Type != "emoji" || req.Reaction[0].Emoji == "" {
		f.problem("invalid setMessageReaction request")
		http.Error(w, "invalid setMessageReaction", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.Reactions = append(f.Reactions, fmt.Sprintf("%d:%s", req.MessageID, req.Reaction[0].Emoji))
	f.mu.Unlock()
	respond(w, map[string]any{"ok": true, "result": true})
}

func (f *fixture) action(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Chat   int64  `json:"chat_id"`
		Action string `json:"action"`
	}
	if err := decode(r, &req); err != nil || req.Chat != chatID || req.Action != "typing" {
		f.problem("invalid sendChatAction request")
		http.Error(w, "invalid sendChatAction", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.Actions = append(f.Actions, req.Action)
	f.mu.Unlock()
	respond(w, map[string]any{"ok": true, "result": true})
}

func (f *fixture) problem(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Problems = append(f.Problems, fmt.Sprintf(format, args...))
}

func (f *fixture) status(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	respond(w, f.transcript)
}

func decode(r *http.Request, dest any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dest)
}

func respond(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// A shutdown can cancel an in-flight long poll.
		return
	}
}

func (st transcript) count(kind, fragment string) int {
	var list []string
	switch kind {
	case "messages":
		list = st.Messages
	case "reactions":
		list = st.Reactions
	case "actions":
		list = st.Actions
	case "turns":
		list = st.Turns
	}
	n := 0
	for _, item := range list {
		if strings.Contains(item, fragment) {
			n++
		}
	}
	return n
}

func snapshot() (transcript, error) {
	var st transcript
	err := control("GET", "/status", nil, &st)
	return st, err
}

func wait(description string, done func(transcript) bool) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st, err := snapshot()
		if err != nil {
			return err
		}
		if len(st.Problems) != 0 {
			return errors.New(strings.Join(st.Problems, "; "))
		}
		if done(st) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", description)
}

func control(method, path string, body, out any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequest(method, baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	res, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("fixture control %s returned HTTP %d", path, res.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}
