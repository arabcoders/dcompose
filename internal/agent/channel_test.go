package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func openChannel(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	headers := http.Header{}
	headers.Set("Authorization", "Bearer test-secret")
	headers.Set(protocol.Header, strconv.Itoa(protocol.AgentAPI))
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "https://", "wss://", 1)+"/channel", &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: headers})
	if nil != err {
		t.Fatal(err)
	}
	conn.SetReadLimit(8 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func channelSend(t *testing.T, conn *websocket.Conn, message protocol.ChannelMessage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, message); nil != err {
		t.Fatal(err)
	}
}

func channelNext(t *testing.T, conn *websocket.Conn) protocol.ChannelMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var message protocol.ChannelMessage
	if err := wsjson.Read(ctx, conn, &message); nil != err {
		t.Fatal(err)
	}
	return message
}

func channelUntil(t *testing.T, conn *websocket.Conn, kind, name string) protocol.ChannelMessage {
	t.Helper()
	for {
		message := channelNext(t, conn)
		if kind == message.Type && name == message.Event {
			return message
		}
	}
}

func channelServer(t *testing.T, a *agent) *httptest.Server {
	t.Helper()
	a.startBackground()
	server := httptest.NewTLSServer(a)
	t.Cleanup(server.Close)
	return server
}

func TestChannelRuntime(t *testing.T) {
	a, fake := fixture(t)
	dir := seed(t, a, "demo", "compose.yaml")
	write(t, filepath.Join(dir, ".env"), "SECRET=private")
	server := channelServer(t, a)
	conn := openChannel(t, server)
	message := channelNext(t, conn)
	if "event" != message.Type || "node.snapshot" != message.Event || !strings.Contains(string(message.Payload), `"name":"demo"`) || strings.Contains(string(message.Payload), "SECRET") {
		t.Fatalf("initial snapshot: %+v", message)
	}
	var snapshot struct {
		Health nodeHealth `json:"health"`
	}
	if nil != json.Unmarshal(message.Payload, &snapshot) || "ok" != snapshot.Health.Status || protocol.AgentAPI != snapshot.Health.APIVersion || a.config.Global.ImageCheckInterval != snapshot.Health.ImageCheckInterval {
		t.Fatal("invalid channel health")
	}
	message = channelUntil(t, conn, "event", "stack.runtime")
	if !strings.Contains(string(message.Payload), `"name":"demo"`) {
		t.Fatalf("runtime payload: %s", message.Payload)
	}
	eventually(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(fake, "event-pids"))
		return 1 == len(strings.Fields(string(data)))
	})
	_ = conn.CloseNow()
	seed(t, a, "external", "compose.yaml")
	if err := os.Remove(filepath.Join(dir, "compose.yaml")); nil != err {
		t.Fatal(err)
	}
	conn = openChannel(t, server)
	message = channelNext(t, conn)
	if "node.snapshot" != message.Event || !strings.Contains(string(message.Payload), `"name":"external"`) || strings.Contains(string(message.Payload), `"name":"demo"`) {
		t.Fatalf("reconnect inventory: %s", message.Payload)
	}
	data, _ := os.ReadFile(filepath.Join(fake, "event-pids"))
	if 1 != len(strings.Fields(string(data))) {
		t.Fatalf("duplicate listeners: %s", data)
	}
}

func TestChannelShell(t *testing.T) {
	a, _ := shellFixture(t)
	s := createdShell(t, a, `{"container":"web"}`)
	server := channelServer(t, a)
	conn := openChannel(t, server)
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.attach", Shell: s.op.ID})
	message := channelNext(t, conn)
	for "shell.event" != message.Type {
		message = channelNext(t, conn)
	}
	if "operation.snapshot" != message.Event {
		t.Fatalf("shell delta preceded snapshot: %s", message.Event)
	}
	var snapshot operationSnapshot
	if nil != json.Unmarshal(message.Payload, &snapshot) || s.op.ID != message.Shell || s.op.ID != snapshot.ID {
		t.Fatalf("shell snapshot: %+v", message)
	}
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.resize", Shell: s.op.ID, Cols: 90, Rows: 25})
	// Two consecutive chunks must reach the PTY in their original order.
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.input", Shell: s.op.ID, Data: "printf 'channel-"})
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.input", Shell: s.op.ID, Data: "ordered\\n'; stty size\n"})
	output := ""
	for !strings.Contains(output, "channel-ordered\r\n") || !strings.Contains(output, "25 90") {
		message = channelUntil(t, conn, "shell.event", "operation.output")
		var chunk struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal(message.Payload, &chunk); nil != err {
			t.Fatal(err)
		}
		output += chunk.Output
	}
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.input", Shell: s.op.ID, Data: strings.Repeat("x", (64<<10)+1)})
	message = channelUntil(t, conn, "shell.error", "")
	if s.op.ID != message.Shell || "" == message.Error {
		t.Fatalf("input limit: %+v", message)
	}
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.resize", Shell: s.op.ID, Cols: 501, Rows: 25})
	if message = channelUntil(t, conn, "shell.error", ""); s.op.ID != message.Shell || "" == message.Error {
		t.Fatalf("resize limit: %+v", message)
	}
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.close", Shell: s.op.ID})
	message = channelUntil(t, conn, "shell.event", "operation.done")
	if nil != json.Unmarshal(message.Payload, &snapshot) || "succeeded" != snapshot.Status || nil == snapshot.ExitCode || 0 != *snapshot.ExitCode {
		t.Fatalf("close: %s", message.Payload)
	}
}

func TestChannelDetach(t *testing.T) {
	a, _ := shellFixture(t)
	s := createdShell(t, a, `{"container":"web"}`)
	server := channelServer(t, a)
	first, second := openChannel(t, server), openChannel(t, server)
	for _, conn := range []*websocket.Conn{first, second} {
		channelSend(t, conn, protocol.ChannelMessage{Type: "shell.attach", Shell: s.op.ID})
		channelUntil(t, conn, "shell.event", "operation.snapshot")
	}
	channelSend(t, first, protocol.ChannelMessage{Type: "shell.detach", Shell: s.op.ID})
	eventually(t, func() bool { s.op.mu.Lock(); defer s.op.mu.Unlock(); return 1 == len(s.op.subscribers) })
	channelSend(t, second, protocol.ChannelMessage{Type: "shell.input", Shell: s.op.ID, Data: "echo observer-live\n"})
	output := ""
	for !strings.Contains(output, "observer-live") {
		message := channelUntil(t, second, "shell.event", "operation.output")
		var chunk struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal(message.Payload, &chunk); nil != err {
			t.Fatal(err)
		}
		output += chunk.Output
	}
	_ = second.CloseNow()
	_ = first.CloseNow()
	eventually(t, func() bool {
		s.op.mu.Lock()
		defer s.op.mu.Unlock()
		a.events.mu.Lock()
		defer a.events.mu.Unlock()
		return 0 == len(s.op.subscribers) && 0 == len(a.events.subscribers)
	})
	if "running" != s.op.snapshot().Status {
		t.Fatal("detach or channel disconnect killed shell")
	}
	conn := openChannel(t, server)
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.attach", Shell: s.op.ID})
	message := channelUntil(t, conn, "shell.event", "operation.snapshot")
	if !strings.Contains(string(message.Payload), "observer-live") {
		t.Fatal("reconnect lost shell output")
	}
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.close", Shell: s.op.ID})
	channelUntil(t, conn, "shell.event", "operation.done")
}

func TestChannelAuth(t *testing.T) {
	a, fake := fixture(t)
	for _, test := range []struct {
		token, ip, version string
		code               int
	}{
		{"", "127.0.0.1:42", "", 401},
		{"test-secret", "192.0.2.1:42", "", 403},
		{"test-secret", "127.0.0.1:42", "999", 426},
	} {
		r := httptest.NewRequest(http.MethodGet, "/channel", nil)
		r.RemoteAddr = test.ip
		r.Header.Set("Authorization", "Bearer "+test.token)
		r.Header.Set(protocol.Header, test.version)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if test.code != w.Code {
			t.Fatalf("auth: got %d, want %d", w.Code, test.code)
		}
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatal("unauthorized request invoked Docker")
	}
}

func TestChannelShutdown(t *testing.T) {
	a, _ := fixture(t)
	ctx, cancel := context.WithCancel(a.ctx)
	a.ctx = ctx
	defer cancel()
	conn := openChannel(t, channelServer(t, a))
	channelUntil(t, conn, "event", "node.snapshot")
	cancel()
	readCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(readCtx, conn, &message); nil != err {
			if nil != readCtx.Err() {
				t.Fatal("agent cancellation did not close channel")
			}
			break
		}
	}
	eventually(t, func() bool { a.events.mu.Lock(); defer a.events.mu.Unlock(); return 0 == len(a.events.subscribers) })
}

func TestChannelErrors(t *testing.T) {
	a, _ := fixture(t)
	for _, test := range []struct {
		name, level string
		err         error
		canceled    bool
	}{
		{"eof", "INFO", fmt.Errorf("failed to read frame header: %w", io.EOF), false},
		{"normal", "DEBUG", websocket.CloseError{Code: websocket.StatusNormalClosure}, false},
		{"going-away", "DEBUG", websocket.CloseError{Code: websocket.StatusGoingAway}, false},
		{"partial", "WARN", fmt.Errorf("failed to read: %w", io.ErrUnexpectedEOF), false},
		{"protocol", "WARN", websocket.CloseError{Code: websocket.StatusProtocolError}, false},
		{"failure", "WARN", errors.New("connection reset by peer"), false},
		{"canceled", "", io.EOF, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs := captureAutomaticLogs(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.canceled {
				cancel()
			}
			a.channelError(ctx, "127.0.0.1:42", "reading a request", test.err)
			if test.canceled {
				if "" != logs.text() {
					t.Fatal("shutdown logged a connection failure", logs.text())
				}
				return
			}
			var record struct {
				Level, Error string
				Message      string `json:"msg"`
			}
			if err := json.Unmarshal([]byte(logs.text()), &record); nil != err {
				t.Fatal(err)
			}
			if test.level != record.Level || !strings.Contains(record.Message, "127.0.0.1:42") || !strings.Contains(record.Message, a.config.Global.NodeID) {
				t.Fatal("incorrect connection log", logs.text())
			}
			if "WARN" == test.level {
				if test.err.Error() != record.Error || (!errors.Is(test.err, io.ErrUnexpectedEOF) && !strings.Contains(record.Message, record.Error)) {
					t.Fatal("connection failure cause was lost", logs.text())
				}
			}
			if status := websocket.CloseStatus(test.err); "DEBUG" == test.level && !strings.Contains(record.Message, strconv.Itoa(int(status))) {
				t.Fatal("connection close code was lost", logs.text())
			}
		})
	}
}

func TestChannelDisconnect(t *testing.T) {
	for _, mode := range []string{"abrupt", "normal", "going-away", "invalid-json"} {
		t.Run(mode, func(t *testing.T) {
			logs := captureAutomaticLogs(t)
			a, _ := fixture(t)
			if w := pairRequest(a, a.pairingToken); http.StatusOK != w.Code {
				t.Fatal("could not pair node", w.Code)
			}
			server := channelServer(t, a)
			conn := openChannel(t, server)
			channelUntil(t, conn, "event", "node.snapshot")
			switch mode {
			case "abrupt":
				_ = conn.CloseNow()
			case "normal":
				_ = conn.Close(websocket.StatusNormalClosure, "")
			case "going-away":
				_ = conn.Close(websocket.StatusGoingAway, "")
			case "invalid-json":
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := conn.Write(ctx, websocket.MessageText, []byte("not JSON")); nil != err {
					t.Fatal(err)
				}
				for {
					if _, _, err := conn.Read(ctx); nil != err {
						if websocket.StatusInvalidFramePayloadData != websocket.CloseStatus(err) {
							t.Fatal("invalid JSON did not receive a protocol close", err)
						}
						break
					}
				}
			}
			eventually(t, func() bool {
				a.events.mu.Lock()
				defer a.events.mu.Unlock()
				return 0 == len(a.events.subscribers)
			})
			warned := strings.Contains(logs.text(), `"level":"WARN"`)
			if ("invalid-json" == mode) != warned {
				t.Fatal("incorrect disconnect severity", logs.text())
			}
			if !a.config.Global.Paired || "" != a.pairingToken {
				t.Fatal("connection loss reset pairing")
			}
			conn = openChannel(t, server)
			channelUntil(t, conn, "event", "node.snapshot")
		})
	}
}

func TestChannelInputIsolation(t *testing.T) {
	a, _ := fixture(t)
	master, slave, err := shellTerminal()
	if nil != err {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	cmd := exec.Command("stty", "raw", "-echo")
	cmd.Stdin = slave
	if err := cmd.Run(); nil != err {
		t.Fatal(err)
	}
	s := &shellSession{op: &operation{operationSnapshot: operationSnapshot{ID: "blocked", Status: "running"}}, terminal: master}
	a.shells["blocked"] = s
	conn := openChannel(t, channelServer(t, a))
	channelUntil(t, conn, "event", "node.snapshot")
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.input", Shell: "blocked", Data: strings.Repeat("x", 64<<10)})
	eventually(t, func() bool {
		if s.mu.TryLock() {
			s.mu.Unlock()
			return false
		}
		return true
	})
	a.setNodeState(nodeState{State: "isolation-test"})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, conn, &message); nil != err {
			t.Fatalf("blocked PTY stalled runtime: %v", err)
		}
		if "node.state" == message.Event && strings.Contains(string(message.Payload), "isolation-test") {
			break
		}
	}
	_ = conn.CloseNow()
}

func TestChannelLargeSnapshot(t *testing.T) {
	a, _ := fixture(t)
	a.shells["large"] = &shellSession{op: &operation{operationSnapshot: operationSnapshot{ID: "large", Status: "succeeded", Output: strings.Repeat("\x00", 1<<20)}}}
	conn := openChannel(t, channelServer(t, a))
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.attach", Shell: "large"})
	message := channelUntil(t, conn, "shell.event", "operation.snapshot")
	var snapshot operationSnapshot
	if err := json.Unmarshal(message.Payload, &snapshot); nil != err || 1<<20 != len(snapshot.Output) {
		t.Fatalf("escaped snapshot truncated: %v", err)
	}
	channelUntil(t, conn, "shell.event", "operation.done")
}

func TestChannelCleanupRetry(t *testing.T) {
	a, fake := shellFixture(t)
	s := createdShell(t, a, `{"container":"web"}`)
	conn := openChannel(t, channelServer(t, a))
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.attach", Shell: s.op.ID})
	channelUntil(t, conn, "shell.event", "operation.snapshot")
	write(t, filepath.Join(fake, "cleanup-fail"), "")
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.close", Shell: s.op.ID})
	done, failed := false, false
	for !done || !failed {
		message := channelNext(t, conn)
		if "shell.error" == message.Type {
			if s.op.ID != message.Shell || "" == message.Error || strings.Contains(message.Error, fake) {
				t.Fatalf("unsafe cleanup error: %+v", message)
			}
			failed = true
		}
		if "operation.done" == message.Event {
			var snapshot operationSnapshot
			if nil != json.Unmarshal(message.Payload, &snapshot) || "failed" != snapshot.Status {
				t.Fatalf("cleanup failure missing: %s", message.Payload)
			}
			done = true
		}
	}
	if err := os.Remove(filepath.Join(fake, "cleanup-fail")); nil != err {
		t.Fatal(err)
	}
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.close", Shell: s.op.ID})
	message := channelUntil(t, conn, "shell.event", "operation.done")
	var snapshot operationSnapshot
	if nil != json.Unmarshal(message.Payload, &snapshot) || "succeeded" != snapshot.Status || nil == snapshot.ExitCode || 0 != *snapshot.ExitCode {
		t.Fatalf("cleanup retry: %s", message.Payload)
	}
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.close", Shell: s.op.ID})
	channelUntil(t, conn, "shell.event", "operation.done")
}

func TestChannelStreamErrors(t *testing.T) {
	a, _ := fixture(t)
	dir := seed(t, a, "removed", "compose.yaml")
	if err := os.Remove(filepath.Join(dir, "compose.yaml")); nil != err {
		t.Fatal(err)
	}
	conn := openChannel(t, channelServer(t, a))
	for _, test := range []struct {
		kind, resource string
		status         int
	}{
		{"operation.attach", "missing", 404},
		{"operation.attach", "", 404},
		{"relay.attach", "missing", 404},
		{"relay.attach", "", 400},
		{"logs.attach", "missing", 404},
		{"logs.attach", "removed", 404},
		{"logs.attach", "../private", 400},
	} {
		channelSend(t, conn, protocol.ChannelMessage{Type: test.kind, Stream: "errors", Resource: test.resource})
		message := channelUntil(t, conn, "stream.error", "")
		if "errors" != message.Stream || test.status != message.Status || strings.Contains(message.Error, "private") {
			t.Fatalf("stream rejection: %+v", message)
		}
	}
}

func TestChannelLogsStartError(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	if err := os.Remove(filepath.Join(fake, "docker")); nil != err {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake)
	conn := openChannel(t, channelServer(t, a))
	channelSend(t, conn, protocol.ChannelMessage{Type: "logs.attach", Stream: "logs", Resource: "demo"})
	message := channelUntil(t, conn, "stream.error", "")
	if 502 != message.Status || "logs" != message.Stream || "" == message.Error || strings.Contains(message.Error, fake) {
		t.Fatalf("upstream failure: %+v", message)
	}
}

func TestChannelMultiplex(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	write(t, filepath.Join(fake, "block"), "")
	conn := openChannel(t, channelServer(t, a))
	w := request(a, "POST", "/stacks/demo/actions", `{"action":"start"}`)
	channelSend(t, conn, protocol.ChannelMessage{Type: "logs.attach", Stream: "logs", Resource: "demo"})
	seen := make(map[string]bool)
	for !seen["output"] || !seen["logs"] || !seen["runtime"] {
		message := channelNext(t, conn)
		if "stream.event" == message.Type {
			seen[message.Stream] = true
		}
		if "event" == message.Type && "node.snapshot" == message.Event {
			seen["runtime"] = true
		}
		if "event" == message.Type && "stack.output" == message.Event {
			seen["output"] = true
		}
	}
	eventually(t, func() bool { _, err := os.Stat(filepath.Join(fake, "log-child")); return nil == err })
	pidText, _ := os.ReadFile(filepath.Join(fake, "log-pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidText)))
	_ = conn.CloseNow()
	eventually(t, func() bool { return processGone(pid) })
	a.mu.Lock()
	busy := a.busy["demo"]
	a.mu.Unlock()
	if !busy {
		t.Fatal("disconnect killed the action")
	}
	if err := os.Remove(filepath.Join(fake, "block")); nil != err {
		t.Fatal(err)
	}
	completed(t, a, w)
}

func TestChannelOnlyRealtime(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	a.shells["legacy"] = &shellSession{}
	for _, path := range []string{"/events", "/operations/missing/events", "/stacks/demo/logs", "/shells/missing/events"} {
		if w := request(a, "GET", path, ""); 404 != w.Code {
			t.Fatalf("legacy stream remains active: %s (%d)", path, w.Code)
		}
	}
	for _, path := range []string{"/shells/legacy/input", "/shells/legacy/resize"} {
		if w := request(a, "POST", path, "{}"); 404 != w.Code {
			t.Fatalf("legacy terminal route remains active: %s (%d)", path, w.Code)
		}
	}
	if a.events.started {
		t.Fatal("legacy endpoint started runtime subscription")
	}
}
