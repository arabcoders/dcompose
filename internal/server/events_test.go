package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func channelCommand(t *testing.T, f *channelFixture, kind string) protocol.ChannelMessage {
	t.Helper()
	select {
	case message := <-f.commands:
		if kind != message.Type {
			t.Fatal("wrong command", message, "want", kind)
		}
		return message
	case <-time.After(3 * time.Second):
		t.Fatal("missing command", kind)
		return protocol.ChannelMessage{}
	}
}

func TestBrowserMux(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, node, app := fixture.channel, fixture.server, fixture.node, fixture.app
	browser := workspaceSnapshot(t, app.URL, s)
	attachments := []protocol.ChannelMessage{
		{Type: "logs.attach", Stream: "logs", Node: "home", Resource: "demo"},
		{Type: "logs.attach", Stream: "other-logs", Node: "home", Resource: "other"},
		{Type: "shell.attach", Stream: "terminal", Node: "home", Shell: "session"},
	}
	commands := make([]protocol.ChannelMessage, 0, len(attachments))
	for _, message := range attachments {
		browserSend(t, browser, message)
		commands = append(commands, channelCommand(t, f, message.Type))
	}
	if "" == commands[0].Stream || commands[0].Stream == commands[1].Stream {
		t.Fatal("subscriptions share node ID", commands)
	}
	frames := []protocol.ChannelMessage{
		{Type: "stream.event", Stream: commands[0].Stream, Event: "message", Payload: json.RawMessage(`{"message":"log line"}`)},
		{Type: "stream.event", Stream: commands[1].Stream, Event: "message", Payload: json.RawMessage(`{"message":"before"}`)},
		{Type: "stream.event", Stream: commands[1].Stream, Event: "message", Payload: json.RawMessage(`{"message":"after"}`)},
		{
			Type:    "shell.event",
			Shell:   "session",
			Event:   "operation.snapshot",
			Payload: json.RawMessage(`{"id":"session","output":"before"}`),
		},
		{Type: "shell.event", Shell: "session", Event: "operation.output", Payload: json.RawMessage(`{"id":"session","output":"after"}`)},
		{Type: "event", Event: "stack.runtime", Payload: json.RawMessage(readFixture(t, "stack-runtime.json"))},
	}
	for _, frame := range frames {
		browserSend(t, node, frame)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	seen := map[string][]string{}
	for count := 0; count < len(frames); {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, browser, &message); nil != err {
			t.Fatal(err)
		}
		if "node.state" == message.Event {
			continue
		}
		seen[message.Stream] = append(seen[message.Stream], message.Event)
		if "terminal" == message.Stream && ("shell.event" != message.Type || "session" != message.Shell) {
			t.Fatal("wrong shell routing", message)
		}
		count++
	}
	if "message" != strings.Join(seen["logs"], ",") || "message,message" != strings.Join(seen["other-logs"], ",") ||
		"operation.snapshot,operation.output" != strings.Join(seen["terminal"], ",") ||
		"stack.runtime" != strings.Join(seen[""], ",") {
		t.Fatal("wrong multiplexed events", seen)
	}
	for index, id := range []string{"logs", "other-logs", "terminal"} {
		browserSend(t, browser, protocol.ChannelMessage{Type: "stream.detach", Stream: id})
		if 2 > index {
			detach := channelCommand(t, f, "stream.detach")
			if commands[index].Stream != detach.Stream {
				t.Fatal("detached another resource", detach)
			}
		} else {
			nextCommand(t, f, "shell.close")
			nextCommand(t, f, "shell.detach")
		}
	}
	sendRuntime(t, node, "stack.removed", `{"name":"demo"}`)
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, browser, &message); nil != err {
			t.Fatal(err)
		}
		if "stack.removed" == message.Event {
			break
		}
	}
	if 1 != f.count.Load() || 0 != f.health.Load() {
		t.Fatal("resource subscription made another node connection", f.count.Load(), f.health.Load())
	}
}

func TestBrowserTabs(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, node, app := fixture.channel, fixture.server, fixture.node, fixture.app
	one := workspaceSnapshot(t, app.URL, s)
	two := workspaceSnapshot(t, app.URL, s)
	attach := protocol.ChannelMessage{Type: "logs.attach", Stream: "same-id", Node: "home", Resource: "demo"}
	browserSend(t, one, attach)
	first := channelCommand(t, f, "logs.attach")
	browserSend(t, two, attach)
	second := channelCommand(t, f, "logs.attach")
	if first.Stream == second.Stream {
		t.Fatal("browser IDs reused as node IDs")
	}
	if "demo" != first.Resource || "demo" != second.Resource {
		t.Fatal("log attachment changed resource", first, second)
	}
	for index, command := range []protocol.ChannelMessage{first, second} {
		payload := json.RawMessage(`{"message":"one"}`)
		if 1 == index {
			payload = json.RawMessage(`{"message":"two"}`)
		}
		browserSend(t, node, protocol.ChannelMessage{Type: "stream.event", Stream: command.Stream, Event: "message", Payload: payload})
	}
	if got := resourceMessage(
		t,
		one,
	); "event" != got.Type || "same-id" != got.Stream || "message" != got.Event ||
		!strings.Contains(string(got.Payload), "one") {
		t.Fatal(got)
	}
	if got := resourceMessage(t, two); "same-id" != got.Stream || !strings.Contains(string(got.Payload), "two") {
		t.Fatal(got)
	}
	_ = one.CloseNow()
	if detach := channelCommand(t, f, "stream.detach"); first.Stream != detach.Stream {
		t.Fatal("closed another tab subscription", detach)
	}
	browserSend(
		t,
		node,
		protocol.ChannelMessage{
			Type:    "stream.event",
			Stream:  second.Stream,
			Event:   "message",
			Payload: json.RawMessage(`{"message":"still here"}`),
		},
	)
	if got := resourceMessage(t, two); !strings.Contains(string(got.Payload), "still here") {
		t.Fatal(got)
	}
	_ = two.CloseNow()
	if detach := channelCommand(t, f, "stream.detach"); second.Stream != detach.Stream {
		t.Fatal(detach)
	}
}

func TestShellObservers(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, node, app := fixture.channel, fixture.server, fixture.node, fixture.app
	one := workspaceSnapshot(t, app.URL, s)
	two := workspaceSnapshot(t, app.URL, s)
	attach := protocol.ChannelMessage{Type: "shell.attach", Stream: "same-id", Node: "home", Shell: "session"}
	browserSend(t, one, attach)
	nextCommand(t, f, "shell.attach")
	browserSend(
		t,
		node,
		protocol.ChannelMessage{
			Type:    "shell.event",
			Shell:   "session",
			Event:   "operation.snapshot",
			Payload: json.RawMessage(`{"id":"session","output":"first"}`),
		},
	)
	resourceMessage(t, one)
	browserSend(t, two, attach)
	nextCommand(t, f, "shell.attach")
	// A new observer ignores deltas preceding its initial snapshot.
	browserSend(
		t,
		node,
		protocol.ChannelMessage{
			Type:    "shell.event",
			Shell:   "session",
			Event:   "operation.output",
			Payload: json.RawMessage(`{"id":"session","output":"next"}`),
		},
	)
	browserSend(
		t,
		node,
		protocol.ChannelMessage{
			Type:    "shell.event",
			Shell:   "session",
			Event:   "operation.snapshot",
			Payload: json.RawMessage(`{"id":"session","output":"firstnext"}`),
		},
	)
	if got := resourceMessage(t, two); "operation.snapshot" != got.Event {
		t.Fatal("delta preceded snapshot", got)
	}
	_ = one.CloseNow()
	// The second tab remains attached, so the first close must not close the shell.
	browserSend(t, two, protocol.ChannelMessage{Type: "shell.input", Stream: "same-id", Shell: "forged", Node: "forged", Data: "test"})
	nextCommand(t, f, "shell.input")
	_ = two.CloseNow()
	nextCommand(t, f, "shell.close")
	nextCommand(t, f, "shell.detach")
}

func TestBrowserAuth(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, app := fixture.channel, fixture.server, fixture.app
	for _, origin := range []string{"", app.URL} {
		conn, response, err := dialBrowser(t, app, s, false, origin)
		if nil != err || http.StatusSwitchingProtocols != response.StatusCode {
			t.Fatal("same-origin unauthenticated handshake", response, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var message protocol.ChannelMessage
		if err := wsjson.Read(
			ctx,
			conn,
			&message,
		); nil != err || "error" != message.Type || http.StatusUnauthorized != message.Status ||
			"" == message.Error {
			t.Fatal("unexpected unauthenticated data", message, err)
		}
		if _, _, err := conn.Read(ctx); nil == err {
			t.Fatal("unauthenticated socket remained open")
		}
		cancel()
		_ = conn.CloseNow()
	}
	s.hubMu.Lock()
	watchers := len(s.watchers)
	s.hubMu.Unlock()
	if 0 != watchers || 0 != len(f.commands) {
		t.Fatal("unauthenticated browser subscribed", watchers)
	}
	for _, origin := range []string{"https://evil.example", strings.Replace(app.URL, "http://", "https://", 1)} {
		conn, response, err := dialBrowser(t, app, s, false, origin)
		if nil != conn {
			_ = conn.CloseNow()
		}
		if nil == err || nil == response || 403 != response.StatusCode {
			t.Fatal("cross-origin handshake accepted", response, err)
		}
	}
}

func TestBrowserRevocation(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, app := fixture.channel, fixture.server, fixture.app
	browser := workspaceSnapshot(t, app.URL, s)
	s.configMu.Lock()
	s.config.Auth.Username = "changed"
	s.configMu.Unlock()
	browserSend(t, browser, protocol.ChannelMessage{Type: "shell.attach", Stream: "terminal", Node: "home", Shell: "session"})
	got := resourceMessage(t, browser)
	if "error" != got.Type || 401 != got.Status || "" != got.Stream || "" == got.Error || 0 != len(f.commands) {
		t.Fatal("expired credentials accepted a command", got)
	}
}

func TestBrowserExpiry(t *testing.T) {
	s := testServer(t, nil)
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	config, _ := s.snapshot()
	token, err := sessionToken(config.Auth, time.Now().Add(3*time.Second))
	if nil != err {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set("Cookie", (&http.Cookie{Name: "session", Value: token}).String())
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(app.URL, "http")+"/api/events", &websocket.DialOptions{HTTPHeader: headers})
	if nil != err {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	var message protocol.ChannelMessage
	if err := wsjson.Read(ctx, conn, &message); nil != err || "error" != message.Type || 401 != message.Status || "" == message.Error {
		t.Fatal("idle browser did not notice session expiry", message, err)
	}
	if _, _, err := conn.Read(ctx); nil == err {
		t.Fatal("expired browser remained connected")
	}
}

func TestBrowserNodeFailure(t *testing.T) {
	first := fakeChannel(t)
	second := fakeChannel(t)
	s := testServer(t, first.server)
	nodeConfig := s.config.Nodes["home"]
	nodeConfig.URL, nodeConfig.Fingerprint = second.server.URL, testFingerprint(second.server)
	s.config.Nodes["other"] = nodeConfig
	s.nodes["other"] = nodeClient(nodeConfig)
	startChannels(t, s)
	broken := nextConnection(t, first)
	healthy := nextConnection(t, second)
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	browser := workspaceSnapshot(t, app.URL, s)
	for _, node := range []string{"home", "other"} {
		browserSend(t, browser, protocol.ChannelMessage{Type: "logs.attach", Stream: node, Node: node, Resource: "demo"})
	}
	channelCommand(t, first, "logs.attach")
	remaining := channelCommand(t, second, "logs.attach")
	_ = broken.CloseNow()
	if got := resourceMessage(t, browser); "home" != got.Stream || "error" != got.Type || 502 != got.Status {
		t.Fatal("node failure did not invalidate its subscription", got)
	}
	browserSend(
		t,
		healthy,
		protocol.ChannelMessage{
			Type:    "stream.event",
			Stream:  remaining.Stream,
			Event:   "message",
			Payload: json.RawMessage(`{"message":"healthy"}`),
		},
	)
	if got := resourceMessage(t, browser); "other" != got.Stream || "message" != got.Event {
		t.Fatal("node failure affected another subscription", got)
	}
	sendRuntime(t, healthy, "stack.runtime", readFixture(t, "stack-runtime.json"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, browser, &message); nil != err {
			t.Fatal("node failure closed workspace", err)
		}
		if "stack.runtime" == message.Event {
			if "" != message.Stream || !strings.Contains(string(message.Payload), `"node":"other"`) {
				t.Fatal(message)
			}
			break
		}
	}
	nextConnection(t, first)
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, browser, &message); nil != err {
			t.Fatal("reconnected node snapshot missing", err)
		}
		if "node.snapshot" == message.Event && strings.Contains(string(message.Payload), `"node":"home"`) {
			break
		}
	}
}

func TestLegacyRoutes(t *testing.T) {
	s := testServer(t, nil)
	s.config.Nodes["home"] = NodeConfig{}
	for _, path := range []string{
		"/api/nodes/home/stacks/demo/logs",
		"/api/nodes/home/operations/deploy/events",
		"/api/nodes/home/shells/session/connect",
		"/api/nodes/home/shells/session/events",
		"/api/nodes/home/shells/session/input",
		"/api/nodes/home/shells/session/resize",
	} {
		if w := perform(t, s, "GET", path, ""); 404 != w.Code {
			t.Fatal("legacy route retained", path, w.Code)
		}
	}
}

func TestAuthChallenge(t *testing.T) {
	s := testServer(t, nil)
	for _, input := range []struct {
		path string
		body string
	}{
		{"/api/auth/login", `{"username":"admin","password":"wrong"}`},
		{"/api/settings/account", `{"username":"admin","current_password":"wrong"}`},
	} {
		w := perform(t, s, "POST", input.path, input.body)
		if 401 != w.Code || "" != w.Header().Get("WWW-Authenticate") {
			t.Fatal("credential error invalidated session", input.path, w.Code, w.Header())
		}
	}
	config, _ := s.snapshot()
	token, err := sessionToken(config.Auth, time.Now().Add(-time.Second))
	if nil != err {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/nodes", nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: token})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 401 != w.Code || "Session" != w.Header().Get("WWW-Authenticate") {
		t.Fatal("expired session did not invalidate cache", w.Code, w.Header())
	}
}

func TestWorkspaceSync(t *testing.T) {
	f := fakeChannel(t)
	s := testServer(t, f.server)
	startChannels(t, s)
	node := nextConnection(t, f)
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	browser := workspaceSnapshot(t, app.URL, s)
	browserSend(t, browser, protocol.ChannelMessage{Type: "logs.attach", Stream: "logs", Node: "home", Resource: "demo"})
	attach := channelCommand(t, f, "logs.attach")
	other, _ := s.subscribeWorkspace()
	t.Cleanup(func() {
		s.hubMu.Lock()
		delete(s.watchers, other)
		s.hubMu.Unlock()
	})
	sendRuntime(t, node, "stack.runtime", readFixture(t, "stack-runtime.json"))
	awaitCache(t, s, `"name":"demo"`, "")
	<-other
	browserSend(t, browser, protocol.ChannelMessage{Type: "workspace.sync"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, browser, &message); nil != err {
			t.Fatal(err)
		}
		if "node.snapshot" == message.Event {
			if "" != message.Stream || !strings.Contains(string(message.Payload), `"name":"demo"`) {
				t.Fatal("stale workspace sync", message)
			}
			break
		}
	}
	if 0 != len(other) || 0 != len(f.commands) {
		t.Fatal("workspace sync affected other subscribers or node connection")
	}
	browserSend(
		t,
		node,
		protocol.ChannelMessage{
			Type:    "stream.event",
			Stream:  attach.Stream,
			Event:   "message",
			Payload: json.RawMessage(`{"message":"after sync"}`),
		},
	)
	if got := resourceMessage(t, browser); "logs" != got.Stream {
		t.Fatal("workspace sync detached logs", got)
	}
}
