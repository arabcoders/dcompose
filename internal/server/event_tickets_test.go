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

const eventTicketOrigin = "https://integration.example"

func mintEventTicket(t *testing.T, s *Server) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/tickets", strings.NewReader(`{}`))
	r.AddCookie(testSession(t, s))
	return mintEventTicketRequest(t, s, r)
}

func mintEventTicketRequest(t *testing.T, s *Server, r *http.Request) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if http.StatusCreated != w.Code {
		t.Fatal("could not mint ticket", w.Code)
	}
	var result struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); nil != err || "" == result.Ticket {
		t.Fatal("missing connection ticket")
	}
	return result.Ticket
}

func dialEventTicket(t *testing.T, app *httptest.Server, path string, headers http.Header) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(app.URL, "http")+path, &websocket.DialOptions{HTTPHeader: headers})
	if nil != err {
		t.Fatal("ticket websocket handshake failed")
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func authenticateEventTicket(t *testing.T, conn *websocket.Conn, ticket string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, struct {
		Type   string `json:"type"`
		Ticket string `json:"ticket"`
	}{Type: "auth", Ticket: ticket}); nil != err {
		t.Fatal("could not send ticket authentication")
	}
	_, data, err := conn.Read(ctx)
	if nil != err || `{"type":"auth.ok"}` != string(data) {
		t.Fatal("authentication acknowledgement must precede workspace data")
	}
}

func rejectEventTicket(t *testing.T, conn *websocket.Conn, allowClose bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if nil != err {
		if !allowClose || nil != ctx.Err() {
			t.Fatal("ticket rejection did not return an error frame")
		}
		return
	}
	var message protocol.ChannelMessage
	if err := json.Unmarshal(
		data,
		&message,
	); nil != err || "error" != message.Type || http.StatusUnauthorized != message.Status || "" == message.Error ||
		"" != message.Stream {
		t.Fatal("unexpected ticket rejection frame")
	}
	if _, _, err := conn.Read(ctx); nil == err || nil != ctx.Err() {
		t.Fatal("rejected ticket connection did not close")
	}
}

func TestTicketMux(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, node := fixture.channel, fixture.server, fixture.node
	s.configMu.Lock()
	s.config.Global.WebsocketOrigins = []string{eventTicketOrigin}
	s.configMu.Unlock()
	awaitCache(t, s, `"health"`, "")
	browser := dialEventTicket(t, fixture.app, "/api/events", http.Header{"Origin": {eventTicketOrigin}})
	authenticateEventTicket(t, browser, mintEventTicket(t, s))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var initial protocol.ChannelMessage
	if err := wsjson.Read(ctx, browser, &initial); nil != err || "node.snapshot" != initial.Event || "event" != initial.Type {
		t.Fatal("missing initial workspace snapshot after authentication")
	}

	// No cookie or Bearer header accompanies this socket or its commands.
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
	if "" == commands[0].Stream || commands[0].Stream == commands[1].Stream || "demo" != commands[0].Resource ||
		"other" != commands[1].Resource ||
		"session" != commands[2].Shell {
		t.Fatal("ticket connection changed resource routing")
	}
	for _, frame := range []protocol.ChannelMessage{
		{Type: "stream.event", Stream: commands[0].Stream, Event: "message", Payload: json.RawMessage(`{"message":"log line"}`)},
		{Type: "stream.event", Stream: commands[1].Stream, Event: "message", Payload: json.RawMessage(`{"message":"other log line"}`)},
		{Type: "shell.event", Shell: "session", Event: "operation.snapshot", Payload: json.RawMessage(`{"id":"session","output":"before"}`)},
	} {
		browserSend(t, node, frame)
	}
	seen := map[string]string{}
	for 3 > len(seen) {
		message := resourceMessage(t, browser)
		if "error" == message.Type {
			t.Fatal("authenticated resource command was rejected", message.Status)
		}
		seen[message.Stream] = message.Event
	}
	if "message" != seen["logs"] || "message" != seen["other-logs"] || "operation.snapshot" != seen["terminal"] {
		t.Fatal("ticket connection lost multiplexed events")
	}

	input := strings.Repeat("\x00", 64<<10)
	browserSend(t, browser, protocol.ChannelMessage{Type: "shell.input", Stream: "terminal", Data: input})
	if command := nextCommand(t, f, "shell.input"); input != command.Data {
		t.Fatal("ticket connection lost terminal input")
	}
	browserSend(t, browser, protocol.ChannelMessage{Type: "shell.resize", Stream: "terminal", Cols: 100, Rows: 40})
	if command := nextCommand(t, f, "shell.resize"); 100 != command.Cols || 40 != command.Rows {
		t.Fatal("ticket connection lost terminal resize")
	}
	browserSend(t, browser, protocol.ChannelMessage{Type: "shell.close", Stream: "terminal"})
	nextCommand(t, f, "shell.close")

	sendRuntime(t, node, "stack.runtime", readFixture(t, "stack-runtime.json"))
	awaitCache(t, s, `"name":"demo"`, "")
	browserSend(t, browser, protocol.ChannelMessage{Type: "workspace.sync"})
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, browser, &message); nil != err || "error" == message.Type {
			t.Fatal("ticket workspace sync failed")
		}
		if "node.snapshot" == message.Event {
			if !strings.Contains(string(message.Payload), `"name":"demo"`) {
				t.Fatal("ticket workspace sync returned stale runtime")
			}
			break
		}
	}
	for index, id := range []string{"logs", "other-logs", "terminal"} {
		browserSend(t, browser, protocol.ChannelMessage{Type: "stream.detach", Stream: id})
		if 2 > index {
			if command := channelCommand(t, f, "stream.detach"); commands[index].Stream != command.Stream {
				t.Fatal("ticket connection detached another resource")
			}
		} else {
			nextCommand(t, f, "shell.close")
			nextCommand(t, f, "shell.detach")
		}
	}
	if 1 != f.count.Load() || 0 != f.health.Load() {
		t.Fatal("ticket connection opened another upstream connection")
	}
}

func TestTicketFrames(t *testing.T) {
	fixture := newWorkspace(t)
	s := fixture.server
	s.configMu.Lock()
	s.config.Global.WebsocketOrigins = []string{eventTicketOrigin}
	s.configMu.Unlock()
	expired := mintEventTicket(t, s)
	s.ticketMu.Lock()
	s.tickets[expired] = websocketTicket{expires: time.Now().Add(-time.Second)}
	s.ticketMu.Unlock()
	query := mintEventTicket(t, s)
	for _, test := range []struct {
		name, path, data string
		kind             websocket.MessageType
		allowClose       bool
	}{
		{name: "unknown", data: `{"type":"auth","ticket":"unknown"}`},
		{name: "empty", data: `{"type":"auth","ticket":""}`},
		{name: "expired", data: `{"type":"auth","ticket":"` + expired + `"}`},
		{name: "command", data: `{"type":"workspace.sync"}`},
		{name: "query", path: "/api/events?ticket=" + query, data: `{"type":"workspace.sync"}`},
		{name: "malformed", data: `{`, allowClose: true},
		{name: "binary", kind: websocket.MessageBinary, data: `{"type":"auth","ticket":"` + query + `"}`},
		{name: "oversized", data: strings.Repeat("x", 1025), allowClose: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := test.path
			if "" == path {
				path = "/api/events"
			}
			conn := dialEventTicket(t, fixture.app, path, http.Header{"Origin": {eventTicketOrigin}})
			kind := test.kind
			if 0 == kind {
				kind = websocket.MessageText
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := conn.Write(ctx, kind, []byte(test.data)); nil != err {
				t.Fatal("could not send first frame")
			}
			rejectEventTicket(t, conn, test.allowClose)
		})
	}
	s.hubMu.Lock()
	watchers := len(s.watchers)
	s.hubMu.Unlock()
	if 0 != watchers || 0 != len(fixture.channel.commands) {
		t.Fatal("unauthenticated connection subscribed to workspace")
	}
}

func TestTicketReplay(t *testing.T) {
	fixture := newWorkspace(t)
	s := fixture.server
	s.configMu.Lock()
	s.config.Global.WebsocketOrigins = []string{eventTicketOrigin}
	s.configMu.Unlock()
	ticket := mintEventTicket(t, s)
	first := dialEventTicket(t, fixture.app, "/api/events", http.Header{"Origin": {eventTicketOrigin}})
	authenticateEventTicket(t, first, ticket)
	second := dialEventTicket(t, fixture.app, "/api/events", http.Header{"Origin": {eventTicketOrigin}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, second, map[string]string{"type": "auth", "ticket": ticket}); nil != err {
		t.Fatal("could not send replayed ticket")
	}
	rejectEventTicket(t, second, false)
}

func TestTicketRevocation(t *testing.T) {
	for _, credential := range []string{"session", "signing_key", "bearer"} {
		for _, connected := range []bool{false, true} {
			phase := "pending"
			if connected {
				phase = "connected"
			}
			t.Run(credential+"/"+phase, func(t *testing.T) {
				s := testServer(t, nil)
				s.config.Global.WebsocketOrigins = []string{eventTicketOrigin}
				r := httptest.NewRequest(http.MethodPost, "/api/auth/tickets", strings.NewReader(`{}`))
				r.AddCookie(testSession(t, s))
				if "bearer" == credential {
					s.config.Auth.apiKey = strings.Repeat("cd", 32)
					r.Header.Set("Authorization", "Bearer "+s.config.Auth.apiKey)
				}
				ticket := mintEventTicketRequest(t, s, r)
				app := httptest.NewServer(s)
				t.Cleanup(app.Close)
				conn := dialEventTicket(t, app, "/api/events", http.Header{"Origin": {eventTicketOrigin}})
				if connected {
					authenticateEventTicket(t, conn, ticket)
				}

				s.configMu.Lock()
				switch credential {
				case "session":
					s.config.Auth.Username = "changed"
				case "signing_key":
					s.config.Auth.sessionKey = strings.Repeat("\xef", 32)
				case "bearer":
					s.config.Auth.apiKey = strings.Repeat("ef", 32)
				}
				s.configMu.Unlock()

				if connected {
					browserSend(t, conn, protocol.ChannelMessage{Type: "shell.attach", Stream: "terminal", Node: "home", Shell: "session"})
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					if err := wsjson.Write(ctx, conn, map[string]string{"type": "auth", "ticket": ticket}); nil != err {
						t.Fatal(err)
					}
				}
				rejectEventTicket(t, conn, false)
			})
		}
	}
}

func TestTicketSessionExpiry(t *testing.T) {
	s := testServer(t, nil)
	s.config.Global.WebsocketOrigins = []string{eventTicketOrigin}
	token, err := sessionToken(s.config.Auth, time.Now().Add(3*time.Second))
	if nil != err {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/auth/tickets", strings.NewReader(`{}`))
	r.AddCookie(&http.Cookie{Name: "session", Value: token})
	ticket := mintEventTicketRequest(t, s, r)
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	conn := dialEventTicket(t, app, "/api/events", http.Header{"Origin": {eventTicketOrigin}})
	authenticateEventTicket(t, conn, ticket)
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	var message protocol.ChannelMessage
	if err := wsjson.Read(ctx, conn, &message); nil != err || "error" != message.Type || http.StatusUnauthorized != message.Status {
		t.Fatal("idle ticket connection outlived its session", message, err)
	}
	if _, _, err := conn.Read(ctx); nil == err || nil != ctx.Err() {
		t.Fatal("expired ticket connection did not close", err)
	}
}

func TestTicketOrigins(t *testing.T) {
	fixture := newWorkspace(t)
	s, app := fixture.server, fixture.app
	s.configMu.Lock()
	s.config.Global.WebsocketOrigins = []string{eventTicketOrigin, app.URL}
	s.config.Auth.APIKey, s.config.Auth.apiKey = strings.Repeat("cd", 32), strings.Repeat("cd", 32)
	s.configMu.Unlock()
	for _, auth := range []string{"cookie", "bearer"} {
		t.Run(auth, func(t *testing.T) {
			headers := http.Header{"Origin": {eventTicketOrigin}}
			if "cookie" == auth {
				headers.Set("Cookie", testSession(t, s).String())
			} else {
				headers.Set("Authorization", "Bearer "+strings.Repeat("cd", 32))
			}
			conn := dialEventTicket(t, app, "/api/events", headers)
			browserSend(t, conn, protocol.ChannelMessage{Type: "workspace.sync"})
			rejectEventTicket(t, conn, false)

			headers.Set("Origin", "https://unlisted.example")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			rejected, response, err := websocket.Dial(
				ctx,
				"ws"+strings.TrimPrefix(app.URL, "http")+"/api/events",
				&websocket.DialOptions{HTTPHeader: headers},
			)
			if nil != rejected {
				_ = rejected.CloseNow()
			}
			if nil == err || nil == response || http.StatusForbidden != response.StatusCode {
				t.Fatal("unlisted origin accepted native credentials")
			}
		})
	}

	t.Run("same-origin-ticket", func(t *testing.T) {
		awaitCache(t, s, `"health"`, "")
		conn := dialEventTicket(t, app, "/api/events", http.Header{"Origin": {app.URL}})
		authenticateEventTicket(t, conn, mintEventTicket(t, s))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, conn, &message); nil != err || "node.snapshot" != message.Event {
			t.Fatal("explicit same-origin ticket did not receive workspace")
		}
	})
	t.Run("no-origin", func(t *testing.T) {
		conn := dialEventTicket(t, app, "/api/events", nil)
		rejectEventTicket(t, conn, false)
	})
	t.Run("rest", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/auth/tickets", strings.NewReader(`{}`))
		r.AddCookie(testSession(t, s))
		r.Header.Set("Origin", eventTicketOrigin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if http.StatusForbidden != w.Code || "" != w.Header().Get("Access-Control-Allow-Origin") {
			t.Fatal("websocket allowlist enabled cross-origin REST")
		}
	})
}

func TestTicketDeadline(t *testing.T) {
	s := testServer(t, nil)
	s.config.Global.WebsocketOrigins = []string{eventTicketOrigin}
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	conn := dialEventTicket(t, app, "/api/events", http.Header{"Origin": {eventTicketOrigin}})
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	started := time.Now()
	if _, _, err := conn.Read(ctx); nil == err || nil != ctx.Err() {
		t.Fatal("missing first-auth deadline close")
	}
	if 4*time.Second > time.Since(started) {
		t.Fatal("ticket connection closed before first-auth deadline")
	}
}
