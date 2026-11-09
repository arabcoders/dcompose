package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestRelayFailure(t *testing.T) {
	s := New(Config{}, nil)
	updates := make(chan []byte, 1)
	s.watchers[updates] = struct{}{}
	code := 1
	cause := "helper-failure-sentinel"
	s.emitRelayAction("home", "demo", "update", "exit", &code, "\x1b[31m"+cause+"\x1b[0m\r\n")
	var frame protocol.ChannelMessage
	if nil != json.Unmarshal(<-updates, &frame) || "stack.action" != frame.Event {
		t.Fatal("missing live helper failure")
	}
	var payload struct {
		Node, Name, Action, Phase, Message string
		ExitCode                           int `json:"exit_code"`
	}
	if nil != json.Unmarshal(frame.Payload, &payload) || "home" != payload.Node || "demo" != payload.Name || "update" != payload.Action || "exit" != payload.Phase || code != payload.ExitCode {
		t.Fatal("invalid helper failure context", string(frame.Payload))
	}
	for _, detail := range []string{"home", "demo", "update", cause} {
		if !strings.Contains(payload.Message, detail) {
			t.Fatal("helper failure omitted context", detail, payload.Message)
		}
	}
	if strings.Contains(payload.Message, "\x1b") {
		t.Fatal("helper failure message retained ANSI controls")
	}
}

func relayOutput(t *testing.T, browser *websocket.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, browser, &message); nil != err {
			t.Fatal("missing helper output", err)
		}
		if "stack.output" != message.Event {
			continue
		}
		var output struct {
			Node, Name, Output string
			ID                 string `json:"id"`
		}
		if nil != json.Unmarshal(message.Payload, &output) || "" != message.Stream || "" != output.ID || "home" != output.Node || "demo" != output.Name || want != output.Output {
			t.Fatal("helper replayed output or exposed private identity", string(message.Payload))
		}
		return
	}
}

func TestRelayResume(t *testing.T) {
	f := fakeChannel(t)
	var pending atomic.Bool
	var discoveries atomic.Int32
	f.relays = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		discoveries.Add(1)
		refs := []map[string]string{}
		if pending.Load() {
			refs = append(refs, map[string]string{"id": "self-helper", "stack": "demo", "action": "update"})
		}
		jsonResponse(w, http.StatusOK, map[string]any{"relays": refs})
	})
	f.http = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if http.MethodPost != r.Method || "/stacks/demo/actions" != r.URL.Path {
			t.Error("unexpected helper route", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		pending.Store(true)
		jsonResponse(w, http.StatusAccepted, map[string]string{"status": "accepted", "relay": "self-helper"})
	})
	s := testServer(t, f.server)
	startChannels(t, s)
	node := nextConnection(t, f)
	awaitImage(t, func() bool { return 0 < discoveries.Load() })
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	one := workspaceSnapshot(t, app.URL, s)
	two := workspaceSnapshot(t, app.URL, s)
	w := perform(t, s, http.MethodPost, "/api/nodes/home/stacks/demo/actions", `{"action":"update"}`)
	var accepted map[string]string
	if http.StatusAccepted != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &accepted) || 1 != len(accepted) || "accepted" != accepted["status"] {
		t.Fatal("private helper identity escaped acceptance response", w.Code, w.Body.String())
	}
	attach := channelCommand(t, f, "relay.attach")
	if "self-helper" != attach.Resource || "" == attach.Stream {
		t.Fatal("private helper did not attach", attach)
	}
	old, delta, next := "\x1b[32mhelper started\x1b[0m\r\n", "pulling\r", "finished\r\n"
	send := func(conn *websocket.Conn, stream, event string, payload any) {
		t.Helper()
		data, err := json.Marshal(payload)
		if nil != err {
			t.Fatal(err)
		}
		browserSend(t, conn, protocol.ChannelMessage{Type: "stream.event", Stream: stream, Event: event, Payload: data})
	}
	send(node, attach.Stream, "relay.snapshot", map[string]any{"id": "self-helper", "stack": "demo", "action": "update", "status": "running", "output": old})
	for _, browser := range []*websocket.Conn{one, two} {
		relayOutput(t, browser, old)
	}
	send(node, attach.Stream, "relay.output", map[string]string{"id": "self-helper", "output": delta})
	for _, browser := range []*websocket.Conn{one, two} {
		relayOutput(t, browser, delta)
	}
	_ = node.CloseNow()
	node = nextConnection(t, f)
	resumed := channelCommand(t, f, "relay.attach")
	if "self-helper" != resumed.Resource || "" == resumed.Stream || attach.Stream == resumed.Stream {
		t.Fatal("helper did not resume on a fresh stream", resumed)
	}
	send(node, resumed.Stream, "relay.snapshot", map[string]any{"id": "self-helper", "stack": "demo", "action": "update", "status": "running", "output": old + delta + next})
	for _, browser := range []*websocket.Conn{one, two} {
		relayOutput(t, browser, next)
	}
	pending.Store(false)
	send(node, resumed.Stream, "relay.done", map[string]any{"id": "self-helper", "stack": "demo", "action": "update", "status": "succeeded", "output": old + delta + next, "exit_code": 0})
	for _, browser := range []*websocket.Conn{one, two} {
		message := stackMessage(t, browser, "stack.action")
		var exit struct {
			Node, Name, Action, Phase string
			ExitCode                  *int `json:"exit_code"`
		}
		if nil != json.Unmarshal(message.Payload, &exit) || "home" != exit.Node || "demo" != exit.Name || "update" != exit.Action || "exit" != exit.Phase || nil == exit.ExitCode || 0 != *exit.ExitCode {
			t.Fatal("helper completion was not a live stack exit", string(message.Payload))
		}
	}
	if detach := channelCommand(t, f, "stream.detach"); resumed.Stream != detach.Stream {
		t.Fatal("helper completion did not acknowledge journal collection", detach)
	}
	late := browserStream(t, app.URL+"/api/events", s)
	stackMessage(t, late, "node.snapshot")
	browserSend(t, late, protocol.ChannelMessage{Type: "workspace.sync"})
	stackMessage(t, late, "node.snapshot")
	sendRuntime(t, node, "stack.runtime", `{"name":"demo","status":"running","services":[]}`)
	stackMessage(t, late, "stack.runtime")
}
