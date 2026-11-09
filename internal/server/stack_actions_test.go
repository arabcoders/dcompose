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

func TestStackAccepted(t *testing.T) {
	var requests atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !(http.MethodPost == r.Method && "/stacks/demo/actions" == r.URL.Path) &&
			!(http.MethodDelete == r.Method && "/stacks/demo" == r.URL.Path) {
			t.Error("unexpected upstream action", r.Method, r.URL.Path)
		}
		jsonResponse(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}))
	t.Cleanup(up.Close)
	s := testServer(t, up)
	assumeRegistriesApplied(s)
	for _, input := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/nodes/home/stacks/demo/actions", `{"action":"deploy"}`},
		{http.MethodDelete, "/api/nodes/home/stacks/demo", ""},
	} {
		w := perform(t, s, input.method, input.path, input.body)
		var accepted map[string]string
		if http.StatusAccepted != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &accepted) || 1 != len(accepted) ||
			"accepted" != accepted["status"] {
			t.Fatal("action returned an operation instead of acceptance", w.Code, w.Body.String())
		}
		r := httptest.NewRequest(input.method, input.path, strings.NewReader(input.body))
		w = httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if http.StatusUnauthorized != w.Code {
			t.Fatal("unauthenticated action accepted", w.Code)
		}
	}
	if 2 != requests.Load() {
		t.Fatal("unauthenticated action reached node", requests.Load())
	}
}

func TestOperationRoutesRemoved(t *testing.T) {
	var requests atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(up.Close)
	s := testServer(t, up)
	for _, path := range []string{
		"/api/nodes/home/operations",
		"/api/nodes/home/operations/old",
		"/api/nodes/home/operations/old/events",
		"/api/nodes/home/relays",
		"/api/nodes/home/relays/private",
	} {
		if w := perform(t, s, http.MethodGet, path, ""); http.StatusNotFound != w.Code {
			t.Fatal("private or obsolete route retained", path, w.Code)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if http.StatusUnauthorized != w.Code {
			t.Fatal("removed route bypassed authentication", path, w.Code)
		}
	}
	if 0 != requests.Load() {
		t.Fatal("removed route reached node", requests.Load())
	}
}

func TestBrowserPrivateAttach(t *testing.T) {
	fixture := newWorkspace(t)
	browser := workspaceSnapshot(t, fixture.app.URL, fixture.server)
	for _, kind := range []string{"operation.attach", "relay.attach"} {
		browserSend(t, browser, protocol.ChannelMessage{Type: kind, Stream: "forbidden", Node: "home", Resource: "private"})
		got := resourceMessage(t, browser)
		if "error" != got.Type || http.StatusBadRequest != got.Status || 0 != len(fixture.channel.commands) {
			t.Fatal("browser attached a private or obsolete resource", kind, got)
		}
	}
}

func stackMessage(t *testing.T, browser *websocket.Conn, event string) protocol.ChannelMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, browser, &message); nil != err {
			t.Fatal("missing live stack event", event, err)
		}
		if event == message.Event {
			return message
		}
		if "stack.output" == message.Event || "stack.action" == message.Event {
			t.Fatal("unexpected or replayed stack event", message.Event, string(message.Payload))
		}
	}
}

func stackSnapshotBusy(t *testing.T, message protocol.ChannelMessage, busy bool) {
	t.Helper()
	var snapshot struct {
		Stacks []struct {
			Name string `json:"name"`
			Busy bool   `json:"busy"`
		} `json:"stacks"`
	}
	if err := json.Unmarshal(message.Payload, &snapshot); nil != err {
		t.Fatal(err)
	}
	for _, stack := range snapshot.Stacks {
		if "demo" == stack.Name {
			if busy != stack.Busy {
				t.Fatal("wrong live busy flag", stack.Busy, "want", busy)
			}
			return
		}
	}
	t.Fatal("stack missing from runtime snapshot", string(message.Payload))
}

func TestStackLiveOutput(t *testing.T) {
	fixture := newWorkspace(t)
	one := workspaceSnapshot(t, fixture.app.URL, fixture.server)
	two := workspaceSnapshot(t, fixture.app.URL, fixture.server)
	sendRuntime(t, fixture.node, "stack.runtime", `{"name":"demo","status":"running","services":[]}`)
	sendRuntime(t, fixture.node, "stack.action", `{"name":"demo","node":"forged","action":"deploy","phase":"start"}`)
	for _, browser := range []*websocket.Conn{one, two} {
		message := stackMessage(t, browser, "stack.action")
		var action struct {
			Node, Name, Action, Phase string
		}
		if nil != json.Unmarshal(message.Payload, &action) || "event" != message.Type || "" != message.Stream || "home" != action.Node ||
			"demo" != action.Name ||
			"deploy" != action.Action ||
			"start" != action.Phase {
			t.Fatal("live action lost trusted node context", message, string(message.Payload))
		}
	}
	browserSend(t, one, protocol.ChannelMessage{Type: "workspace.sync"})
	stackSnapshotBusy(t, stackMessage(t, one, "node.snapshot"), true)
	browsers := []*websocket.Conn{one, two}
	for index, chunk := range []string{"\x1b[32mpulling\x1b[0m\r", "\n\x1b[2Kfailed: access denied\r\n"} {
		payload, _ := json.Marshal(map[string]string{"name": "demo", "node": "forged", "output": chunk})
		sendRuntime(t, fixture.node, "stack.output", string(payload))
		// Read both tabs before sending exit: output must not wait for completion.
		for _, browser := range browsers {
			message := stackMessage(t, browser, "stack.output")
			var output struct {
				Node, Name, Output string
			}
			if nil != json.Unmarshal(message.Payload, &output) || "" != message.Stream || "home" != output.Node || "demo" != output.Name ||
				chunk != output.Output {
				t.Fatal("live PTY bytes or node context changed", string(message.Payload))
			}
		}
		if 0 == index {
			mid := browserStream(t, fixture.app.URL+"/api/events", fixture.server)
			stackSnapshotBusy(t, stackMessage(t, mid, "node.snapshot"), true)
			browsers = append(browsers, mid)
		}
	}
	sendRuntime(t, fixture.node, "stack.action", `{"name":"demo","action":"deploy","phase":"exit","exit_code":1,"message":"access denied"}`)
	for _, browser := range browsers {
		message := stackMessage(t, browser, "stack.action")
		var exit struct {
			Node, Name, Action, Phase, Message string
			ExitCode                           *int `json:"exit_code"`
		}
		if nil != json.Unmarshal(message.Payload, &exit) || "home" != exit.Node || "demo" != exit.Name || "deploy" != exit.Action ||
			"exit" != exit.Phase ||
			nil == exit.ExitCode ||
			1 != *exit.ExitCode ||
			"" == exit.Message {
			t.Fatal("failure completion lost live context", string(message.Payload))
		}
	}
	browserSend(t, one, protocol.ChannelMessage{Type: "workspace.sync"})
	stackSnapshotBusy(t, stackMessage(t, one, "node.snapshot"), false)
	late := browserStream(t, fixture.app.URL+"/api/events", fixture.server)
	stackSnapshotBusy(t, stackMessage(t, late, "node.snapshot"), false)
	// A fresh runtime event is a FIFO barrier after the late subscription.
	sendRuntime(t, fixture.node, "stack.runtime", `{"name":"demo","status":"stopped","services":[]}`)
	for _, browser := range append(browsers, late) {
		stackMessage(t, browser, "stack.runtime")
	}
}
