package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const testModel = "model-exact"

type outcome struct {
	text, emoji string
	err         error
}

type peer struct {
	t   *testing.T
	ctx context.Context
	ws  *websocket.Conn
}

func testRunner(t *testing.T) (Client, <-chan *websocket.Conn) {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "ws-token")
	if err := os.WriteFile(tokenFile, []byte("fixture-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_WS_TOKEN_FILE", tokenFile)
	connections := make(chan *websocket.Conn, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("Origin") != "" {
			t.Errorf("incorrect WebSocket headers")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		connections <- ws
	}))
	t.Cleanup(server.Close)
	return Client{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Model: testModel}, connections
}

func launch(t *testing.T, client Client, connections <-chan *websocket.Conn, canReact bool) (*peer, <-chan outcome, context.CancelFunc) {
	t.Helper()
	peerCtx, cancelPeer := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancelPeer)
	ctx, cancel := context.WithCancel(peerCtx)
	t.Cleanup(cancel)
	results := make(chan outcome, 1)
	go func() {
		text, emoji, err := client.Run(ctx, "fixture prompt", canReact)
		results <- outcome{text, emoji, err}
	}()
	select {
	case ws := <-connections:
		t.Cleanup(func() { _ = ws.CloseNow() })
		return &peer{t, peerCtx, ws}, results, cancel
	case result := <-results:
		t.Fatalf("Run exited before dial: %+v", result)
	case <-peerCtx.Done():
		t.Fatal(peerCtx.Err())
	}
	return nil, nil, nil
}

func (p *peer) read(method string) message {
	p.t.Helper()
	var m message
	if err := wsjson.Read(p.ctx, p.ws, &m); err != nil {
		p.t.Fatal(err)
	}
	if m.Method != method {
		p.t.Fatalf("method %q, want %q", m.Method, method)
	}
	return m
}

func (p *peer) send(v any) {
	p.t.Helper()
	if err := wsjson.Write(p.ctx, p.ws, v); err != nil {
		p.t.Fatal(err)
	}
}

func (p *peer) reply(req message, result any) {
	p.t.Helper()
	p.send(params{"id": req.ID, "result": result})
}

func (p *peer) event(method string, data params) {
	p.t.Helper()
	p.send(params{"method": method, "params": data})
}

func (p *peer) serverRequest(id any, method string, data params) {
	p.t.Helper()
	p.send(params{"id": id, "method": method, "params": data})
}

func decode[T any](t *testing.T, data json.RawMessage) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func check(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func (p *peer) init() {
	p.t.Helper()
	init := p.read("initialize")
	check(p.t, decode[params](p.t, init.Params)["capabilities"], params{"experimentalApi": true, "requestAttestation": false})
	p.reply(init, params{"codexHome": "/home/node/.codex", "platformOs": "linux", "platformFamily": "unix"})
	check(p.t, len(p.read("initialized").ID), 0)
}

func (p *peer) catalog() {
	p.t.Helper()
	m := p.read("model/list")
	check(p.t, decode[params](p.t, m.Params), params{"modelProvider": "openai", "includeHidden": true})
	p.reply(m, params{"data": []params{{"id": "picker-id", "model": testModel}}, "nextCursor": nil})
}

func threadResult(id string) params {
	return params{
		"thread": params{"id": id, "ephemeral": true, "model": testModel, "environments": []params{
			{"environmentId": "local", "cwd": "/workspace", "runtimeWorkspaceRoots": []string{"/workspace"}},
		}},
		"cwd": "/workspace", "model": testModel, "modelProvider": "openai", "approvalPolicy": "never",
		"activePermissionProfile": params{"id": "companion-workspace", "extends": nil},
		"sandbox": params{
			"type": "workspaceWrite", "writableRoots": []string{}, "networkAccess": true,
			"excludeTmpdirEnvVar": true, "excludeSlashTmp": true,
		},
	}
}

func assertRestrictions(t *testing.T, args params) {
	t.Helper()
	check(t, args["model"], testModel)
	check(t, args["cwd"], "/workspace")
	check(t, args["runtimeWorkspaceRoots"], []any{"/workspace"})
	check(t, args["permissions"], "companion-workspace")
	check(t, args["approvalPolicy"], "never")
	check(t, args["sandboxPolicy"], nil)
	check(t, args["sandbox"], nil)
}

func (p *peer) thread(id string, canReact bool) {
	p.t.Helper()
	m := p.read("thread/start")
	args := decode[params](p.t, m.Params)
	assertRestrictions(p.t, args)
	check(p.t, args["modelProvider"], "openai")
	check(p.t, args["ephemeral"], true)
	check(p.t, args["allowProviderModelFallback"], false)
	cfg := args["config"].(map[string]any)
	check(p.t, cfg["features"], params{"standalone_web_search": true})
	check(p.t, cfg["web_search"], "live")
	check(p.t, cfg["permissions"], nil)
	check(p.t, args["developerInstructions"], workspaceNotesInstructions)
	if canReact {
		want, err := json.Marshal([]params{reactionTool()})
		if err != nil {
			p.t.Fatal(err)
		}
		check(p.t, args["dynamicTools"], decode[[]any](p.t, want))
	} else {
		check(p.t, args["dynamicTools"], nil)
	}
	p.reply(m, threadResult(id))
}

func (p *peer) turn(thread string) message {
	p.t.Helper()
	m := p.read("turn/start")
	args := decode[params](p.t, m.Params)
	assertRestrictions(p.t, args)
	check(p.t, args["threadId"], thread)
	check(p.t, args["input"], []any{params{"type": "text", "text": "fixture prompt", "text_elements": []any{}}})
	return m
}

func turnResult(id, status string, items ...params) params {
	return params{"turn": params{"id": id, "status": status, "items": items}}
}

func item(name, phase, text string) params {
	return params{"type": name, "phase": phase, "text": text}
}

func waitResult(t *testing.T, results <-chan outcome) outcome {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(6 * time.Second):
		t.Fatal("Run did not exit")
		return outcome{}
	}
}

func (p *peer) assertRejection(id string, code int) {
	p.t.Helper()
	r := p.read("")
	check(p.t, string(r.ID), id)
	if r.Error == nil || r.Error.Code != code || len(r.Result) != 0 {
		p.t.Fatalf("expected RPC error %d: %+v", code, r)
	}
}

func TestRunProtocol(t *testing.T) {
	client, conns := testRunner(t)
	p, results, _ := launch(t, client, conns, false)
	p.init()
	list := p.read("model/list")
	// Picker ID alone is not an exact model match.
	p.reply(list, params{"data": []params{{"id": testModel, "model": "model-other"}}, "nextCursor": "page-2"})
	list = p.read("model/list")
	check(t, decode[params](t, list.Params)["cursor"], "page-2")
	p.reply(list, params{"data": []params{{"id": "picker", "model": testModel}}, "nextCursor": nil})
	p.thread("thread-1", false)
	turn := p.turn("thread-1")

	p.serverRequest("escalate", "item/permissions/requestApproval", params{})
	p.assertRejection(`"escalate"`, -32601)
	p.event("item/completed", params{"threadId": "thread-1", "turnId": "turn-1", "item": item("agentMessage", "", "Early answer")})
	p.reply(turn, turnResult("turn-1", "inProgress"))

	for _, method := range []string{"item/commandExecution/requestApproval", "account/chatgptAuthTokens/refresh", "item/tool/call"} {
		p.serverRequest(100, method, params{})
		p.assertRejection("100", -32601)
	}

	for _, i := range []params{
		item("commandExecution", "", ""), item("webSearch", "", ""),
		item("agentMessage", "commentary", "Working"), item("agentMessage", "final_answer", "The final answer."),
	} {
		p.event("item/completed", params{"threadId": "thread-1", "turnId": "turn-1", "item": i})
	}
	foreign := turnResult("turn-1", "completed", item("agentMessage", "final_answer", "Not our answer"))
	foreign["threadId"] = "foreign"
	p.event("turn/completed", foreign)
	done := turnResult("turn-1", "completed")
	done["threadId"] = "thread-1"
	p.event("turn/completed", done)
	check(t, waitResult(t, results), outcome{text: "The final answer."})

	// The next step opens another socket and starts another ephemeral thread.
	p, results, _ = launch(t, client, conns, false)
	p.init()
	p.catalog()
	p.thread("thread-2", false)
	p.reply(p.turn("thread-2"), turnResult("turn-2", "inProgress"))
	done = turnResult("turn-2", "completed", item("agentMessage", "", "A second answer"))
	done["threadId"] = "thread-2"
	p.event("turn/completed", done)
	check(t, waitResult(t, results), outcome{text: "A second answer"})
}

func TestRunRejectsMissingModel(t *testing.T) {
	client, conns := testRunner(t)
	p, results, _ := launch(t, client, conns, false)
	p.init()
	p.reply(p.read("model/list"), params{"data": []params{{"id": testModel, "model": "MODEL-EXACT"}}})
	if result := waitResult(t, results); result.err == nil || !strings.Contains(result.err.Error(), "not in the app-server catalog") {
		t.Fatalf("got %+v, want missing model error", result)
	}
}

func TestRunRejectsUnexpectedThreadSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(params)
		want string
	}{
		{"model", func(p params) { p["model"] = "fallback" }, "did not select configured model"},
		{"thread model", func(p params) { p["thread"].(params)["model"] = "fallback" }, "did not select configured model"},
		{"network disabled", func(p params) { p["sandbox"].(params)["networkAccess"] = false }, "did not confirm restricted"},
		{"network missing", func(p params) { delete(p["sandbox"].(params), "networkAccess") }, "did not confirm restricted"},
		{"write roots", func(p params) { p["sandbox"].(params)["writableRoots"] = []string{"/app"} }, "did not confirm restricted"},
		{"approval", func(p params) { p["approvalPolicy"] = "on-request" }, "did not confirm restricted"},
		{"persistence", func(p params) { p["thread"].(params)["ephemeral"] = false }, "did not confirm restricted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, conns := testRunner(t)
			p, results, _ := launch(t, client, conns, false)
			p.init()
			p.catalog()
			result := threadResult("thread")
			tc.set(result)
			p.reply(p.read("thread/start"), result)
			if got := waitResult(t, results); got.err == nil || !strings.Contains(got.err.Error(), tc.want) {
				t.Fatalf("got %+v, want %q", got, tc.want)
			}
		})
	}
}

func TestRunTurnErrors(t *testing.T) {
	for _, name := range []string{"reroute", "cancel", "failure", "interrupted", "no answer", "disconnect"} {
		t.Run(name, func(t *testing.T) {
			client, conns := testRunner(t)
			p, results, cancel := launch(t, client, conns, false)
			p.init()
			p.catalog()
			p.thread("thread", false)
			p.reply(p.turn("thread"), turnResult("turn", "inProgress"))
			done := params{"threadId": "thread", "turn": params{"id": "turn"}}
			state := done["turn"].(params)
			var want string
			switch name {
			case "reroute":
				p.event("model/rerouted", params{"threadId": "thread", "turnId": "turn", "fromModel": testModel, "toModel": "other"})
				want = "rerouted"
			case "cancel":
				// A reply proves that Run already accepted the turn ID.
				p.serverRequest("ping", "fixture/forbidden", params{})
				p.assertRejection(`"ping"`, -32601)
				cancel()
				want = "context canceled"
			case "failure":
				state["status"] = "failed"
				state["error"] = params{"message": "fixture unavailable"}
				p.event("turn/completed", done)
				want = "fixture unavailable"
			case "interrupted":
				state["status"] = "interrupted"
				p.event("turn/completed", done)
				want = "interrupted"
			case "no answer":
				state["status"] = "completed"
				p.event("turn/completed", done)
				want = "without an assistant answer"
			case "disconnect":
				_ = p.ws.CloseNow()
				want = "read app-server WebSocket"
			}
			if name == "cancel" || name == "reroute" {
				m := p.read("turn/interrupt")
				check(t, decode[params](t, m.Params), params{"threadId": "thread", "turnId": "turn"})
				p.reply(m, params{})
			}
			result := waitResult(t, results)
			if result.text != "" || result.err == nil || !strings.Contains(result.err.Error(), want) {
				t.Fatalf("got %+v, want %q", result, want)
			}
			if name == "cancel" && !errors.Is(result.err, context.Canceled) {
				t.Fatalf("lost context error: %v", result.err)
			}
		})
	}
}
