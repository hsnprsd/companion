package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testToken = "123456:example_token_not_a_real_credential"

func TestPollFiltersAndAcknowledgesSavedUpdates(t *testing.T) {
	var next int64
	var accepted []string
	var polled []int64
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+testToken+"/getUpdates" || r.Method != http.MethodPost {
			t.Error("wrong Telegram request")
		}
		var params struct{ Offset int64 }
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			t.Error(err)
		}
		polled = append(polled, params.Offset)
		update := func(id, chat int, chatType, text string) string {
			return fmt.Sprintf(`{"update_id":%d,"message":{"chat":{"id":%d,"type":%q},"text":%q}}`, id, chat, chatType, text)
		}
		switch len(polled) {
		case 1:
			fmt.Fprintf(w, `{"ok":true,"result":[%s,%s,%s,%s]}`,
				update(14, 42, "private", "/approve A1"), update(12, 99, "private", "from stranger"),
				update(13, 42, "group", "from group"), update(11, 42, "private", "/work first"))
		default:
			fmt.Fprintf(w, `{"ok":true,"result":[%s,%s]}`,
				update(14, 42, "private", "/approve A1"), update(15, 42, "private", "hello"))
		}
	}))
	defer server.Close()
	client := New(server.URL, testToken, 42)
	err := client.Poll(ctx, func() int64 { return next }, func(id int64, text string) error {
		accepted = append(accepted, fmt.Sprintf("%d:%s", id, text))
		next = id + 1
		if id == 15 {
			cancel()
		}
		return nil
	})
	if err != nil || ctx.Err() != context.Canceled {
		t.Fatalf("Poll returned %v, context %v", err, ctx.Err())
	}
	want := []string{"11:/work first", "12:", "13:", "14:/approve A1", "15:hello"}
	if !reflect.DeepEqual(accepted, want) || !reflect.DeepEqual(polled, []int64{0, 15}) {
		t.Fatalf("accepted %q, offsets %v", accepted, polled)
	}
}

func TestPollStopsOnPersistenceFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"ok":true,"result":[{"update_id":10,"message":{"chat":{"id":42,"type":"private"},"text":"create task"}}]}`)
	}))
	defer server.Close()
	storeErr := errors.New("disk full")
	client := New(server.URL, testToken, 42)
	err := client.Poll(context.Background(), func() int64 { return 0 }, func(int64, string) error { return storeErr })
	if !errors.Is(err, storeErr) || calls != 1 {
		t.Fatalf("got %v after %d requests", err, calls)
	}
}

func TestSendDoesNotLeakTokenOnFailure(t *testing.T) {
	client := New("http://127.0.0.1:1", testToken, 42)
	err := client.Send(context.Background(), "sample")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || strings.Contains(err.Error(), testToken) || !apiErr.Uncertain() {
		t.Fatal("expected a sanitized, uncertain error")
	}
}

func TestSendPlainText(t *testing.T) {
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			Chat   int64  `json:"chat_id"`
			Text   string `json:"text"`
			Format string `json:"parse_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			t.Error(err)
		}
		if params.Chat != 42 || params.Format != "" {
			t.Error("invalid Telegram message")
		}
		got = append(got, params.Text)
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer server.Close()
	text := "*proposed* <path>\n😀"
	client := New(server.URL, testToken, 42)
	if err := client.Send(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != text {
		t.Fatal("message did not match input")
	}
}
