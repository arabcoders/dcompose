package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type channelFixture struct {
	server      *httptest.Server
	connections chan *websocket.Conn
	commands    chan protocol.ChannelMessage
	count       atomic.Int32
	health      atomic.Int32
	active      atomic.Pointer[websocket.Conn]
	http        http.Handler
	relays      http.Handler
	snapshot    json.RawMessage
	onCommand   func(context.Context, *websocket.Conn, protocol.ChannelMessage) bool
}

func fakeChannel(t *testing.T) *channelFixture {
	t.Helper()
	f := &channelFixture{connections: make(chan *websocket.Conn, 8), commands: make(chan protocol.ChannelMessage, 32)}
	f.snapshot = json.RawMessage(readFixture(t, "node-snapshot.json"))
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if "Bearer agent-test-token" != r.Header.Get("Authorization") || strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) {
			t.Error("missing channel authentication/protocol")
			w.WriteHeader(401)
			return
		}
		if "/health" == r.URL.Path {
			f.health.Add(1)
			testHealth(w, r)
			return
		}
		if "/unpair" == r.URL.Path && http.MethodPost == r.Method {
			jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
			return
		}
		if "/channel" != r.URL.Path {
			if http.MethodGet == r.Method && "/relays" == r.URL.Path {
				if nil != f.relays {
					f.relays.ServeHTTP(w, r)
					return
				}
				jsonResponse(w, http.StatusOK, map[string]interface{}{"relays": []interface{}{}})
				return
			}
			if nil != f.http {
				f.http.ServeHTTP(w, r)
				return
			}
			t.Error("unexpected upstream route", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if nil != err {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(8 << 20)
		f.count.Add(1)
		if !ackRegistrySync(t, r.Context(), conn) {
			return
		}
		f.active.Store(conn)
		if err := wsjson.Write(r.Context(), conn, protocol.ChannelMessage{Type: "event", Event: "node.snapshot", Payload: f.snapshot}); nil != err {
			return
		}
		f.connections <- conn
		for {
			var message protocol.ChannelMessage
			if nil != wsjson.Read(r.Context(), conn, &message) {
				return
			}
			if "registry.sync" == message.Type {
				var snapshot protocol.RegistrySync
				if nil != json.Unmarshal(message.Payload, &snapshot) {
					return
				}
				payload, _ := json.Marshal(protocol.RegistryApplied{Revision: snapshot.Revision})
				if nil != wsjson.Write(r.Context(), conn, protocol.ChannelMessage{Type: "registry.applied", Payload: payload}) {
					return
				}
				continue
			}
			if nil == f.onCommand || !f.onCommand(r.Context(), conn, message) {
				f.commands <- message
			}
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func startChannels(t *testing.T, s *Server) {
	t.Helper()
	s.Start(context.Background())
	t.Cleanup(s.Close)
}

func nextConnection(t *testing.T, f *channelFixture) *websocket.Conn {
	t.Helper()
	select {
	case conn := <-f.connections:
		return conn
	case <-time.After(3 * time.Second):
		t.Fatal("missing node channel")
		return nil
	}
}

func sendRuntime(t *testing.T, conn *websocket.Conn, name, data string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, protocol.ChannelMessage{Type: "event", Event: name, Payload: json.RawMessage(data)}); nil != err {
		t.Fatal(err)
	}
}

func awaitCache(t *testing.T, s *Server, contains, excludes string) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		s.hubMu.Lock()
		var data []byte
		if runtime := s.runtime["home"]; nil != runtime {
			data, _ = json.Marshal(runtime.snapshot)
		}
		s.hubMu.Unlock()
		if strings.Contains(string(data), contains) && ("" == excludes || !strings.Contains(string(data), excludes)) {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("missing cached runtime", string(data))
			return
		}
	}
}

func browserStream(t *testing.T, address string, s *Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	headers := make(http.Header)
	headers.Set("Cookie", testSession(t, s).String())
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(address, "http"), &websocket.DialOptions{HTTPHeader: headers})
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func workspaceSnapshot(t *testing.T, address string, s *Server) *websocket.Conn {
	t.Helper()
	conn := browserStream(t, address+"/api/events", s)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, conn, &message); nil != err {
			t.Fatal("missing browser snapshot", err)
		}
		if "node.snapshot" == message.Event {
			return conn
		}
	}
}

func TestSharedChannel(t *testing.T) {
	f := fakeChannel(t)
	s := testServer(t, f.server)
	startChannels(t, s)
	conn := nextConnection(t, f)
	sendRuntime(t, conn, "stack.runtime", readFixture(t, "stack-runtime.json"))
	sendRuntime(t, conn, "stack.removed", `{"name":"old"}`)
	awaitCache(t, s, `"name":"demo"`, `"name":"old"`)
	s.hubMu.Lock()
	watchers := len(s.watchers)
	s.hubMu.Unlock()
	if 0 != watchers {
		t.Fatal("unexpected browser subscriber")
	}
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	one := workspaceSnapshot(t, app.URL, s)
	two := workspaceSnapshot(t, app.URL, s)
	if 1 != f.count.Load() || 0 != f.health.Load() {
		t.Fatal("browser opened another channel/health request", f.count.Load(), f.health.Load())
	}
	_ = one.CloseNow()
	_ = two.CloseNow()
	sendRuntime(t, conn, "stack.runtime", `{"name":"demo","status":"stopped","services":[]}`)
	awaitCache(t, s, `"status":"stopped"`, `"status":"running"`)
	frames, initial := s.subscribeWorkspace()
	s.hubMu.Lock()
	delete(s.watchers, frames)
	s.hubMu.Unlock()
	if !strings.Contains(string(initial[0]), `"status":"stopped"`) {
		t.Fatal("stale initial snapshot", string(initial[0]))
	}
	if !strings.Contains(string(initial[0]), `"loading":false`) {
		t.Fatal("cached runtime stack is not a workspace snapshot stack", string(initial[0]))
	}
}

func dialBrowser(t *testing.T, app *httptest.Server, s *Server, cookie bool, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	headers := make(http.Header)
	if cookie {
		headers.Set("Cookie", testSession(t, s).String())
	}
	if "" != origin {
		headers.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(app.URL, "http")+"/api/events", &websocket.DialOptions{HTTPHeader: headers})
}

func browserSend(t *testing.T, conn *websocket.Conn, message protocol.ChannelMessage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, message); nil != err {
		t.Fatal(err)
	}
}

func resourceMessage(t *testing.T, conn *websocket.Conn) protocol.ChannelMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, conn, &message); nil != err {
			t.Fatal(err)
		}
		if "" != message.Stream || "error" == message.Type {
			return message
		}
	}
}

func nextCommand(t *testing.T, f *channelFixture, kind string) protocol.ChannelMessage {
	t.Helper()
	select {
	case message := <-f.commands:
		if kind != message.Type || "session" != message.Shell {
			t.Fatal("wrong bound command", message.Type, message.Shell)
		}
		return message
	case <-time.After(2 * time.Second):
		t.Fatal("missing command", kind)
		return protocol.ChannelMessage{}
	}
}

func TestShellChannel(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, upstream, app := fixture.channel, fixture.server, fixture.node, fixture.app
	for _, test := range []struct {
		cookie bool
		origin string
		status int
	}{{true, "https://evil.example", 403}} {
		conn, response, err := dialBrowser(t, app, s, test.cookie, test.origin)
		if nil != conn {
			_ = conn.CloseNow()
		}
		if nil == err || nil == response || test.status != response.StatusCode {
			t.Fatal("browser websocket authorization/origin", response, err)
		}
	}
	browser, _, err := dialBrowser(t, app, s, true, app.URL)
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = browser.CloseNow() })
	browserSend(t, browser, protocol.ChannelMessage{Type: "shell.attach", Stream: "terminal", Node: "home", Shell: "session"})
	nextCommand(t, f, "shell.attach")
	for _, message := range []protocol.ChannelMessage{
		{Type: "shell.input", Stream: "terminal", Node: "forged", Shell: "forged", Data: "echo test\n"},
		{Type: "shell.resize", Stream: "terminal", Shell: "forged", Cols: 90, Rows: 35},
		{Type: "shell.input", Stream: "terminal", Data: strings.Repeat("\x00", 64<<10)},
	} {
		browserSend(t, browser, message)
		got := nextCommand(t, f, message.Type)
		if got.Data != message.Data || got.Cols != message.Cols || got.Rows != message.Rows {
			t.Fatal("command changed")
		}
	}
	if 0 != f.health.Load() {
		t.Fatal("shell commands checked health", f.health.Load())
	}
	browser.SetReadLimit(8 << 20)
	large, err := json.Marshal(map[string]string{"id": "session", "output": strings.Repeat("\x00", 1<<20)})
	if nil != err {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, upstream, protocol.ChannelMessage{Type: "shell.event", Shell: "session", Event: "operation.snapshot", Payload: large}); nil != err {
		t.Fatal(err)
	}
	got := resourceMessage(t, browser)
	if "operation.snapshot" != got.Event || 6<<20 > len(got.Payload) || "terminal" != got.Stream {
		t.Fatal("large terminal snapshot rejected", len(got.Payload), got.Stream)
	}
	output := protocol.ChannelMessage{Type: "shell.event", Shell: "session", Event: "operation.output", Payload: json.RawMessage(`{"id":"session","output":"hello"}`)}
	browserSend(t, upstream, output)
	got = resourceMessage(t, browser)
	if "operation.output" != got.Event || !strings.Contains(string(got.Payload), "hello") {
		t.Fatal("missing terminal output", got)
	}
	_ = upstream.CloseNow()
	got = resourceMessage(t, browser)
	if "shell.error" != got.Type {
		t.Fatal("node disconnect did not fail browser shell", got)
	}
	_ = browser.CloseNow()
}

func TestChannelPin(t *testing.T) {
	f := fakeChannel(t)
	s := testServer(t, f.server)
	node := s.config.Nodes["home"]
	node.Fingerprint = strings.Repeat("A", 43) + "="
	s.config.Nodes["home"] = node
	s.nodes["home"] = nodeClient(node)
	startChannels(t, s)
	frames, _ := s.subscribeWorkspace()
	select {
	case frame := <-frames:
		if !strings.Contains(string(frame), `"state":"unavailable"`) {
			t.Fatal(string(frame))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missing pin failure")
	}
	if 0 != f.count.Load() || 0 != f.health.Load() {
		t.Fatal("unpinned node reached HTTP endpoint")
	}
}

func TestChannelRejection(t *testing.T) {
	output := captureNodeLogs(t, slog.LevelWarn)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": "IP 192.0.2.15 is not allowed. Allowed IPs: 192.0.2.20/32."})
	}))
	t.Cleanup(up.Close)
	s := testServer(t, up)
	node := s.config.Nodes["home"]
	_, err := dialChannel(&nodeChannel{node: node, client: s.nodes["home"], ctx: context.Background()})
	var rejection nodeHTTPError
	if !errors.As(err, &rejection) || http.StatusForbidden != rejection.status {
		t.Fatal("missing node rejection", err)
	}
	if detail := safeTransportError(err); !strings.Contains(detail, "192.0.2.15") || !strings.Contains(detail, "192.0.2.20/32") {
		t.Fatal("missing rejected IP or allowlist", detail)
	}
	startChannels(t, s)
	select {
	case line := <-output.lines:
		var record struct {
			Message string `json:"msg"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal(line, &record); nil != err {
			t.Fatal(err)
		}
		for _, detail := range []string{nodeLogLabel(node), safeTransportError(err), "403", "1s"} {
			if !strings.Contains(record.Message, detail) {
				t.Fatal("retry message hides rejection context", record.Message)
			}
		}
		if safeTransportError(err) != record.Error {
			t.Fatal("retry error changed", record)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing channel rejection log")
	}
}

func TestTransportDetails(t *testing.T) {
	cause := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	err := &url.Error{Op: "Get", URL: "https://user:secret@node.invalid?token=secret", Err: cause}
	if detail := safeTransportError(err); !strings.Contains(detail, cause.Op) || !strings.Contains(detail, cause.Net) || !strings.Contains(detail, cause.Err.Error()) || strings.Contains(detail, "secret") {
		t.Fatal("missing safe connection details", detail)
	}
	const reason = "message exceeded the configured read limit"
	if detail := safeTransportError(errors.New(reason)); reason != detail {
		t.Fatal("underlying error was hidden", detail)
	}
}

func TestChannelRetryLog(t *testing.T) {
	output := captureNodeLogs(t, slog.LevelWarn)
	attempts := make(chan struct{}, 8)
	s := testServer(t, nil)
	node := NodeConfig{Name: "Gaia", URL: "https://user:secret@node.invalid/private?token=secret", token: "secret"}
	cause := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	s.config.Nodes["home"] = node
	s.nodes["home"] = &http.Client{Transport: logTransport(func(*http.Request) (*http.Response, error) {
		attempts <- struct{}{}
		return nil, &url.Error{Op: "Get", URL: node.URL, Err: cause}
	})}
	startChannels(t, s)
	select {
	case line := <-output.lines:
		var record struct {
			Level   string        `json:"level"`
			Message string        `json:"msg"`
			Node    string        `json:"node"`
			Error   string        `json:"error"`
			RetryIn time.Duration `json:"retry_in"`
		}
		if err := json.Unmarshal(line, &record); nil != err {
			t.Fatal(err)
		}
		if "WARN" != record.Level || nodeLogLabel(node) != record.Node || time.Second != record.RetryIn || !strings.Contains(record.Error, cause.Err.Error()) {
			t.Fatal("missing structured retry context", string(line))
		}
		for _, detail := range []string{record.Node, record.Error, time.Second.String()} {
			if !strings.Contains(record.Message, detail) {
				t.Fatal("retry message hides failure context", record.Message)
			}
		}
		if strings.Contains(string(line), "secret") || strings.Contains(string(line), "/private") || strings.Contains(string(line), "user:") {
			t.Fatal("transport URL leaked", string(line))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing channel retry log")
	}
	for range 2 {
		select {
		case <-attempts:
		case <-time.After(2 * time.Second):
			t.Fatal("connection retries were delayed with warnings")
		}
	}
	select {
	case line := <-output.lines:
		t.Fatal("repeated outage warning was not suppressed", string(line))
	case <-time.After(100 * time.Millisecond):
	}
}

func TestChannelWarnings(t *testing.T) {
	now := time.Date(2026, 11, 3, 4, 14, 0, 0, time.UTC)
	var warnings channelWarnings
	for _, interval := range []time.Duration{
		time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute,
		16 * time.Minute, 32 * time.Minute, time.Hour, time.Hour,
	} {
		if !warnings.allow(now) || !warnings.next.Equal(now.Add(interval)) {
			t.Fatal("incorrect warning interval", warnings, interval)
		}
		if warnings.allow(now.Add(interval - time.Nanosecond)) {
			t.Fatal("outage warning emitted before its interval")
		}
		now = now.Add(interval)
	}
	var otherNode channelWarnings
	if !otherNode.allow(now) {
		t.Fatal("another node's outage was suppressed")
	}
	warnings = channelWarnings{}
	if !warnings.allow(now) || !warnings.next.Equal(now.Add(time.Minute)) {
		t.Fatal("recovery did not reset warning intervals")
	}
}

func TestChannelWarningReset(t *testing.T) {
	output := captureNodeLogs(t, slog.LevelWarn)
	f := fakeChannel(t)
	s := testServer(t, f.server)
	transport := s.nodes["home"].Transport
	var fail atomic.Bool
	fail.Store(true)
	s.nodes["home"].Transport = logTransport(func(r *http.Request) (*http.Response, error) {
		if "/channel" == r.URL.Path && fail.CompareAndSwap(true, false) {
			return nil, syscall.ECONNREFUSED
		}
		return transport.RoundTrip(r)
	})
	startChannels(t, s)
	select {
	case <-output.lines:
	case <-time.After(3 * time.Second):
		t.Fatal("missing first outage warning")
	}
	conn := nextConnection(t, f)
	awaitCache(t, s, `"name":"old"`, "")
	_ = conn.CloseNow()
	select {
	case <-output.lines:
	case <-time.After(2 * time.Second):
		t.Fatal("new outage after recovery was suppressed")
	}
}

func TestChannelConfig(t *testing.T) {
	f := fakeChannel(t)
	s := persistedServer(t, nil)
	startChannels(t, s)
	frames, _ := s.subscribeWorkspace()
	changed := func() {
		t.Helper()
		deadline := time.NewTimer(time.Second)
		defer deadline.Stop()
		for {
			select {
			case frame := <-frames:
				var message protocol.ChannelMessage
				if nil == json.Unmarshal(frame, &message) && "nodes.changed" == message.Event {
					return
				}
			case <-deadline.C:
				t.Fatal("node change was not immediate")
				return
			}
		}
	}
	body := fmt.Sprintf(`{"name":"Home","url":%q,"token":"agent-test-token","fingerprint":%q}`, f.server.URL, testFingerprint(f.server))
	w := perform(t, s, "POST", "/api/settings/nodes", body)
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	var created safeNode
	if err := json.Unmarshal(w.Body.Bytes(), &created); nil != err || "" == created.ID {
		t.Fatal("missing generated identity", err)
	}
	changed()
	nextConnection(t, f)
	path := "/api/settings/nodes/" + created.ID
	body = fmt.Sprintf(`{"name":"Manual","url":%q,"fingerprint":%q}`, f.server.URL, testFingerprint(f.server))
	if 200 != perform(t, s, "PUT", path, body).Code {
		t.Fatal("node edit failed")
	}
	changed()
	nextConnection(t, f)
	if 200 != perform(t, s, "PUT", path, body).Code {
		t.Fatal("unchanged node save failed")
	}
	config, _ := s.snapshot()
	if "Manual" != config.Nodes[created.ID].Name || 2 != f.count.Load() {
		t.Fatal("unchanged save restarted node")
	}
	for 0 < len(frames) {
		if strings.Contains(string(<-frames), "nodes.changed") {
			t.Fatal("unchanged save emitted node change")
		}
	}
	if 200 != perform(t, s, "DELETE", path, "").Code {
		t.Fatal("node delete failed")
	}
	changed()
	s.hubMu.Lock()
	remaining := len(s.channels) + len(s.runtime)
	s.hubMu.Unlock()
	if 0 != remaining {
		t.Fatal("removed channel/runtime retained")
	}
}

func TestLogStreamError(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, upstream, app := fixture.channel, fixture.server, fixture.node, fixture.app
	browser := browserStream(t, app.URL+"/api/events", s)
	browserSend(t, browser, protocol.ChannelMessage{Type: "logs.attach", Stream: "missing-log", Node: "home", Resource: "missing"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var attach protocol.ChannelMessage
	select {
	case attach = <-f.commands:
	case <-ctx.Done():
		t.Fatal("missing log attach")
	}
	if "logs.attach" != attach.Type || "missing" != attach.Resource {
		t.Fatal(attach)
	}
	if err := wsjson.Write(ctx, upstream, protocol.ChannelMessage{Type: "stream.error", Stream: attach.Stream, Status: 404, Error: "private transport secret"}); nil != err {
		t.Fatal(err)
	}
	message := resourceMessage(t, browser)
	if "error" != message.Type || "missing-log" != message.Stream || 404 != message.Status || strings.Contains(message.Error, "secret") {
		t.Fatal(message)
	}
}

func TestChannelCancellation(t *testing.T) {
	f := fakeChannel(t)
	s := testServer(t, f.server)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	t.Cleanup(s.Close)
	nextConnection(t, f)
	cancel()
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("channel shutdown blocked")
	}
}
