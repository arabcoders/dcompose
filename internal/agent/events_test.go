package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
)

type eventReader struct {
	conn     *websocket.Conn
	kind     string
	finished bool
}

func openEvents(t *testing.T, server *httptest.Server, path string) *eventReader {
	t.Helper()
	conn := openChannel(t, server)
	kind := "event"
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if 3 == len(parts) && "operations" == parts[0] {
		kind = "stream.event"
		channelSend(t, conn, protocol.ChannelMessage{Type: "operation.attach", Stream: "operation", Resource: parts[1]})
	}
	return &eventReader{conn: conn, kind: kind}
}

func (r *eventReader) next(t *testing.T) (string, []byte) {
	t.Helper()
	for {
		message := channelNext(t, r.conn)
		if r.kind == message.Type {
			r.finished = "operation.done" == message.Event
			return message.Event, message.Payload
		}
	}
}

func (r *eventReader) until(t *testing.T, expected string) []byte {
	t.Helper()
	for {
		name, data := r.next(t)
		if expected == name {
			return data
		}
	}
}

func streamServer(t *testing.T, a *agent) *httptest.Server {
	t.Helper()
	a.startBackground()
	server := httptest.NewTLSServer(a)
	t.Cleanup(server.Close)
	return server
}

func TestRuntimeProgressive(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "slow", "compose.yaml")
	dir := seed(t, a, "fast", "compose.yaml")
	write(t, filepath.Join(dir, ".env"), "SECRET=hidden")
	write(t, filepath.Join(fake, "block-slow"), "")
	write(t, filepath.Join(fake, "ps"), `[{"Name":"fast-web-1","Service":"web","State":"running"}]`)
	server := streamServer(t, a)
	reader := openEvents(t, server, "/events")
	name, data := reader.next(t)
	var snapshot struct {
		Health nodeHealth         `json:"health"`
		Stacks []inventoryRuntime `json:"stacks"`
	}
	if "node.snapshot" != name || nil != json.Unmarshal(data, &snapshot) || 2 != len(snapshot.Stacks) || "ok" != snapshot.Health.Status {
		t.Fatalf("snapshot: %s %s", name, data)
	}
	for _, state := range snapshot.Stacks {
		if !state.Loading || "unknown" != state.Status || 0 != len(state.Services) {
			t.Fatalf("pending runtime: %+v", state)
		}
	}
	if strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "compose") || strings.Contains(string(data), "output") || strings.Contains(string(data), `"env"`) {
		t.Fatalf("unsafe snapshot: %s", data)
	}
	data = reader.until(t, "stack.runtime")
	var state stackRuntime
	if nil != json.Unmarshal(data, &state) || "fast" != state.Name || "running" != state.Status {
		t.Fatalf("fast runtime waited for slow: %s", data)
	}
	_ = os.Remove(filepath.Join(fake, "block-slow"))
	for "slow" != state.Name {
		data = reader.until(t, "stack.runtime")
		if err := json.Unmarshal(data, &state); nil != err {
			t.Fatal(err)
		}
	}
	// A second subscriber shares the monitor and gets cached services marked
	// loading until its fresh runtime refresh completes.
	second := openEvents(t, server, "/events")
	_, data = second.next(t)
	if nil != json.Unmarshal(data, &snapshot) || !snapshot.Stacks[0].Loading || "running" != snapshot.Stacks[0].Status {
		t.Fatalf("cached reconnect: %s", data)
	}
	pids, _ := os.ReadFile(filepath.Join(fake, "event-pids"))
	if 1 != len(strings.Fields(string(pids))) {
		t.Fatalf("duplicate monitors: %s", pids)
	}
}

func TestRuntimeSignals(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	server := streamServer(t, a)
	reader := openEvents(t, server, "/events")
	reader.until(t, "stack.runtime")
	if w := request(a, "PUT", "/stacks/new", `{"compose":"services: {}","env":"SECRET=private"}`); 200 != w.Code {
		t.Fatalf("save: %s", w.Body)
	}
	var state stackRuntime
	for "new" != state.Name {
		if err := json.Unmarshal(reader.until(t, "stack.runtime"), &state); nil != err {
			t.Fatal(err)
		}
	}
	if "draft" != state.Status {
		t.Fatalf("saved draft: %+v", state)
	}
	completed(t, a, request(a, "DELETE", "/stacks/new", ""))
	if data := reader.until(t, "stack.removed"); `{"name":"new"}` != string(data) {
		t.Fatalf("removed: %s", data)
	}
	write(t, filepath.Join(fake, "failps"), "")
	a.signalRuntime("demo")
	state = stackRuntime{}
	for "demo" != state.Name || "" == state.Error {
		if err := json.Unmarshal(reader.until(t, "stack.runtime"), &state); nil != err {
			t.Fatal(err)
		}
	}
	if "unknown" != state.Status || "" == state.Error || strings.Contains(state.Error, fake) {
		t.Fatalf("runtime error: %+v", state)
	}
	// Reconnect discovers file-only changes, without relying on Docker events.
	seed(t, a, "external", "compose.yaml")
	_ = os.Remove(filepath.Join(a.config.Global.StacksDir, "demo", "compose.yaml"))
	reconnected := openEvents(t, server, "/events")
	_, data := reconnected.next(t)
	if !strings.Contains(string(data), `"name":"external"`) || strings.Contains(string(data), `"name":"demo"`) {
		t.Fatalf("fresh inventory: %s", data)
	}
}

func TestEventFilterBurst(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	for _, line := range []string{
		`not json`,
		`{"Type":"image","Actor":{"Attributes":{"com.docker.compose.project":"demo"}}}`,
		`{"Type":"container","Action":"exec_start","Actor":{"Attributes":{"com.docker.compose.project":"demo"}}}`,
		`{"Type":"container","Actor":{"Attributes":{"com.docker.compose.project":"outside"}}}`,
		`{"Type":"container","Actor":{"Attributes":{"com.docker.compose.project":"../demo"}}}`,
	} {
		a.runtimeEvent([]byte(line))
	}
	if 0 != len(a.events.pending) {
		t.Fatal("unmanaged events queued")
	}
	for i := 0; 1000 > i; i++ {
		a.runtimeEvent([]byte(`{"Type":"container","Action":"health_status: healthy","Actor":{"Attributes":{"com.docker.compose.project":"demo"}}}`))
	}
	if 1 != len(a.events.pending) || !a.events.pending["demo"] {
		t.Fatal("burst was not coalesced")
	}
}

func TestMonitorRecovery(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	reader := openEvents(t, streamServer(t, a), "/events")
	reader.until(t, "stack.runtime")
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	args := strings.SplitN(string(argv), "<--since><", 2)
	if 2 != len(args) {
		t.Fatalf("missing subscription replay boundary: %s", argv)
	}
	boundary, _, _ := strings.Cut(args[1], ">")
	if timestamp, err := strconv.ParseFloat(boundary, 64); nil != err || 0 >= timestamp || !strings.Contains(boundary, ".") {
		t.Fatalf("invalid replay timestamp: %s", boundary)
	}
	write(t, filepath.Join(fake, "events-fail"), "")
	var state nodeState
	for "unavailable" != state.State {
		if err := json.Unmarshal(reader.until(t, "node.state"), &state); nil != err {
			t.Fatal(err)
		}
	}
	_ = os.Remove(filepath.Join(fake, "events-fail"))
	for "connected" != state.State {
		if err := json.Unmarshal(reader.until(t, "node.state"), &state); nil != err {
			t.Fatal(err)
		}
	}
	reader.until(t, "stack.runtime")
	pids, _ := os.ReadFile(filepath.Join(fake, "event-pids"))
	if 2 > len(strings.Fields(string(pids))) {
		t.Fatal("monitor did not restart")
	}
	write(t, filepath.Join(fake, "ps"), `[{"State":"running"}]`)
	write(t, filepath.Join(fake, "events-staged"), `{"Type":"container","Action":"start","Actor":{"Attributes":{"com.docker.compose.project":"demo"}}}`+"\n")
	if err := os.Rename(filepath.Join(fake, "events-staged"), filepath.Join(fake, "events-input")); nil != err {
		t.Fatal(err)
	}
	var runtime stackRuntime
	for "running" != runtime.Status {
		if err := json.Unmarshal(reader.until(t, "stack.runtime"), &runtime); nil != err {
			t.Fatal(err)
		}
	}
}

func TestMonitorScanError(t *testing.T) {
	a, fake := fixture(t)
	reader := openEvents(t, streamServer(t, a), "/events")
	var state nodeState
	for "connected" != state.State {
		if err := json.Unmarshal(reader.until(t, "node.state"), &state); nil != err {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(fake, "events-staged"), strings.Repeat("x", (1<<20)+1)+"\n")
	if err := os.Rename(filepath.Join(fake, "events-staged"), filepath.Join(fake, "events-input")); nil != err {
		t.Fatal(err)
	}
	for "unavailable" != state.State {
		if err := json.Unmarshal(reader.until(t, "node.state"), &state); nil != err {
			t.Fatal(err)
		}
	}
	_ = os.Remove(filepath.Join(fake, "events-input"))
	for "connected" != state.State {
		if err := json.Unmarshal(reader.until(t, "node.state"), &state); nil != err {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool {
		pids, _ := os.ReadFile(filepath.Join(fake, "event-pids"))
		return 2 <= len(strings.Fields(string(pids)))
	})
}

func TestEmptyNodeState(t *testing.T) {
	a, fake := fixture(t)
	write(t, filepath.Join(fake, "events-fail"), "")
	reader := openEvents(t, streamServer(t, a), "/events")
	_, data := reader.next(t)
	if !strings.Contains(string(data), `"stacks":[]`) {
		t.Fatalf("empty inventory: %s", data)
	}
	var state nodeState
	for "" == state.Message {
		if err := json.Unmarshal(reader.until(t, "node.state"), &state); nil != err {
			t.Fatal(err)
		}
		if "unavailable" != state.State {
			t.Fatalf("failed daemon briefly connected: %+v", state)
		}
	}
	if _, err := os.Stat(filepath.Join(fake, "event-pids")); !os.IsNotExist(err) {
		t.Fatal("listener started before positive daemon evidence")
	}
	_ = os.Remove(filepath.Join(fake, "events-fail"))
	for "connected" != state.State {
		if err := json.Unmarshal(reader.until(t, "node.state"), &state); nil != err {
			t.Fatal(err)
		}
	}
}

func TestInventoryInflightRemoval(t *testing.T) {
	a, fake := fixture(t)
	dir := seed(t, a, "slow", "compose.yaml")
	write(t, filepath.Join(fake, "block-slow"), "")
	server := streamServer(t, a)
	reader := openEvents(t, server, "/events")
	reader.until(t, "node.snapshot")
	eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(fake, "ps-slow"))
		return nil == err
	})
	if err := os.Remove(filepath.Join(dir, "compose.yaml")); nil != err {
		t.Fatal(err)
	}
	reconnected := openEvents(t, server, "/events")
	_, data := reconnected.next(t)
	if !strings.Contains(string(data), `"stacks":[]`) {
		t.Fatalf("removed stack in snapshot: %s", data)
	}
	reader.until(t, "stack.removed")
	_ = os.Remove(filepath.Join(fake, "block-slow"))
	reader.until(t, "stack.runtime") // The query started before deletion finishes.
	reader.until(t, "stack.removed") // Its final queued refresh observes deletion.
	a.events.mu.Lock()
	_, resurrected := a.events.stacks["slow"]
	a.events.mu.Unlock()
	if resurrected {
		t.Fatal("stale in-flight result resurrected a removed stack")
	}
}

func TestSubscriberLimit(t *testing.T) {
	s := newSubscriber()
	for i := 0; 65 > i; i++ {
		s.send(event("stack.runtime", unknownRuntime("demo")))
	}
	select {
	case <-s.done:
	default:
		t.Fatal("slow subscriber not disconnected")
	}
	// Further output remains nonblocking after eviction.
	s.send(event("stack.runtime", unknownRuntime("demo")))
}

func TestStreamAuth(t *testing.T) {
	a, _ := fixture(t)
	for _, path := range []string{"/events", "/operations/missing/events"} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "127.0.0.1:1"
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if 401 != w.Code || a.events.started {
			t.Fatalf("unauthenticated stream: %d", w.Code)
		}
	}
	if w := request(a, "GET", "/operations/missing/events", ""); 404 != w.Code {
		t.Fatal("missing operation not 404")
	}
}

func TestOperationEvents(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	write(t, filepath.Join(fake, "block"), "")
	server := streamServer(t, a)
	first := openEvents(t, server, "/events")
	second := openEvents(t, server, "/events")
	first.until(t, "node.snapshot")
	second.until(t, "node.snapshot")
	w := request(a, "POST", "/stacks/demo/actions", `{"action":"start"}`)
	for _, reader := range []*eventReader{first, second} {
		var action protocol.StackAction
		data := reader.until(t, "stack.action")
		if nil != json.Unmarshal(data, &action) || "start" != action.Phase || "demo" != action.Name {
			t.Fatalf("start: %s", data)
		}
		var output protocol.StackOutput
		if data = reader.until(t, "stack.output"); nil != json.Unmarshal(data, &output) || "demo" != output.Name || "" == output.Output {
			t.Fatalf("output: %s", data)
		}
	}
	reconnected := openEvents(t, server, "/events")
	var snapshot struct {
		Stacks []inventoryRuntime `json:"stacks"`
	}
	data := reconnected.until(t, "node.snapshot")
	if nil != json.Unmarshal(data, &snapshot) || 1 != len(snapshot.Stacks) || !snapshot.Stacks[0].Busy || strings.Contains(string(data), "output") {
		t.Fatalf("busy snapshot: %s", data)
	}
	_ = os.Remove(filepath.Join(fake, "block"))
	completed(t, a, w)
	for _, reader := range []*eventReader{first, second, reconnected} {
		var action protocol.StackAction
		for {
			name, payload := reader.next(t)
			if reader == reconnected && "stack.output" == name && strings.Contains(string(payload), "started") {
				t.Fatalf("reconnect replayed command output: %s", payload)
			}
			if "stack.action" == name {
				data = payload
				break
			}
		}
		if nil != json.Unmarshal(data, &action) || "exit" != action.Phase || nil == action.ExitCode || 0 != *action.ExitCode {
			t.Fatalf("exit: %s", data)
		}
	}
}

func TestRuntimeStatusSafe(t *testing.T) {
	a, _ := fixture(t)
	dir := seed(t, a, "demo", "compose.yaml")
	write(t, filepath.Join(dir, ".env"), "SECRET=private")
	w := request(a, "GET", "/stacks/demo/status", "")
	if 200 != w.Code || strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "compose") || strings.Contains(w.Body.String(), `"env"`) {
		t.Fatalf("unsafe runtime: %s", w.Body)
	}
}

func TestMonitorShutdown(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "slow", "compose.yaml")
	ctx, cancel := context.WithCancel(a.ctx)
	a.ctx = ctx
	write(t, filepath.Join(fake, "block-slow"), "")
	reader := openEvents(t, streamServer(t, a), "/events")
	reader.until(t, "node.snapshot")
	eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(fake, "event-pid"))
		return nil == err
	})
	eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(fake, "ps-slow"))
		return nil == err
	})
	pidText, _ := os.ReadFile(filepath.Join(fake, "event-pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidText)))
	for i := 0; 50 > i; i++ {
		a.signalRuntime("slow")
	}
	cancel()
	done := make(chan struct{})
	go func() { a.work.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("background work leaked")
	}
	if err := syscall.Kill(pid, 0); syscall.ESRCH != err {
		t.Fatalf("monitor still alive: %d %v", pid, err)
	}
}

func TestRefreshBound(t *testing.T) {
	a, fake := fixture(t)
	for i := 0; 9 > i; i++ {
		seed(t, a, fmt.Sprintf("demo-%d", i), "compose.yaml")
	}
	write(t, filepath.Join(fake, "block-all"), "")
	reader := openEvents(t, streamServer(t, a), "/events")
	reader.until(t, "node.snapshot")
	count := func() int {
		matches, _ := filepath.Glob(filepath.Join(fake, "ps-demo-*"))
		return len(matches)
	}
	eventually(t, func() bool { return 4 == count() })
	time.Sleep(250 * time.Millisecond)
	if 4 != count() {
		t.Fatal("unbounded initial refresh concurrency")
	}
	_ = os.Remove(filepath.Join(fake, "block-all"))
	seen := make(map[string]bool)
	for 9 > len(seen) {
		var state stackRuntime
		if err := json.Unmarshal(reader.until(t, "stack.runtime"), &state); nil != err {
			t.Fatal(err)
		}
		seen[state.Name] = true
	}
}

func TestOperationUTF8(t *testing.T) {
	op := &operation{ID: "utf8", Status: "running"}
	s := newSubscriber()
	op.subscribers = map[*eventSubscriber]struct{}{s: {}}
	text := []byte("日")
	_, _ = op.Write(text[:1])
	if "" != op.snapshot().Output || 0 != len(s.events) {
		t.Fatal("partial rune emitted")
	}
	_, _ = op.Write(text[1:])
	if "日" != op.snapshot().Output {
		t.Fatal("split UTF-8 corrupted")
	}
	e := <-s.events
	if `{"id":"utf8","output":"日"}` != string(e.data) {
		t.Fatalf("invalid UTF-8 delta: %s", e.data)
	}
	op.Output = strings.Repeat("x", (1<<20)-1)
	_, _ = op.Write([]byte("日"))
	if (1<<20)-1 != len(op.snapshot().Output) || 0 != len(s.events) {
		t.Fatal("output cap split a rune")
	}
}
