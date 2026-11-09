package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket/wsjson"
)

type nodeLogOutput struct {
	lines chan []byte
}

func (o *nodeLogOutput) Write(data []byte) (int, error) {
	select {
	case o.lines <- append([]byte(nil), data...):
	default:
	}
	return len(data), nil
}

func captureNodeLogs(t *testing.T, level slog.Level) *nodeLogOutput {
	t.Helper()
	output := &nodeLogOutput{lines: make(chan []byte, 100)}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return output
}

func (o *nodeLogOutput) next(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line := <-o.lines:
			var record map[string]json.RawMessage
			if err := json.Unmarshal(line, &record); nil != err {
				t.Fatal(err)
			}
			if "true" == string(record["_from_node"]) {
				return record
			}
		case <-timer.C:
			t.Fatal("missing forwarded log")
			return nil
		}
	}
}

func TestNodeLogs(t *testing.T) {
	output := captureNodeLogs(t, slog.LevelDebug)
	channel := &nodeChannel{id: "home", node: NodeConfig{Name: "Home"}, ctx: context.Background()}
	payload := json.RawMessage(
		`{"entries":[{"time":"2026-10-29T12:00:00.123456789Z","level":"ERROR","msg":"diagnostic","attrs":{"node_id":"forged","node_name":"forged","_from_node":false,"stack":"demo","service":"web","operation":"job","image":"example:latest","stderr":"registry denied","error":"manifest failure","attempt":9007199254740993,"automatic":true,"details":{"registry":"example","codes":[401,403]}}}]}`,
	)
	if err := (&Server{}).nodeLogs(channel, payload); nil != err {
		t.Fatal(err)
	}
	record := output.next(t)
	for key, expected := range map[string]string{
		"node_id": `"home"`, "node_name": `"Home"`, "_from_node": "true",
		"time": `"2026-10-29T12:00:00.123456789Z"`, "level": `"ERROR"`,
		"stack": `"demo"`, "service": `"web"`, "operation": `"job"`,
		"image": `"example:latest"`, "stderr": `"registry denied"`, "error": `"manifest failure"`,
		"attempt": "9007199254740993", "automatic": "true",
	} {
		if expected != string(record[key]) {
			t.Fatalf("%s: got %s, want %s", key, record[key], expected)
		}
	}
	var details struct {
		Registry string `json:"registry"`
		Codes    []int  `json:"codes"`
	}
	if nil != json.Unmarshal(record["details"], &details) || "example" != details.Registry || 2 != len(details.Codes) ||
		401 != details.Codes[0] {
		t.Fatalf("nested attrs lost: %s", record["details"])
	}
}

func TestNodeLogLevel(t *testing.T) {
	output := captureNodeLogs(t, slog.LevelWarn)
	channel := &nodeChannel{id: "home", ctx: context.Background()}
	if err := (&Server{}).nodeLogs(
		channel,
		json.RawMessage(`{"entries":[{"level":"DEBUG"},{"level":"INFO"},{"level":"WARN"}]}`),
	); nil != err {
		t.Fatal(err)
	}
	if `"WARN"` != string(output.next(t)["level"]) || 0 != len(output.lines) {
		t.Fatal("server log level not respected")
	}
}

func TestChannelNodeLogs(t *testing.T) {
	output := captureNodeLogs(t, slog.LevelInfo)
	f := fakeChannel(t)
	s := testServer(t, f.server)
	s.config.path = filepath.Join(t.TempDir(), "dcompose.toml")
	if err := s.StartLogHistory(); nil != err {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	startChannels(t, s)
	conn := nextConnection(t, f)
	awaitCache(t, s, `"name":"old"`, "")
	frames, _ := s.subscribeWorkspace()
	t.Cleanup(func() { s.hubMu.Lock(); delete(s.watchers, frames); s.hubMu.Unlock() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	payload := json.RawMessage(
		`{"entries":[{"time":"2026-10-29T12:00:00.123456789Z","level":"ERROR","msg":"diagnostic","source":{"file":"node.go","function":"node.check","line":42},"attrs":{"image":"example:latest","stderr":"registry denied\nsecond line","node_id":"forged","_node_source":{"file":"forged"}}}]}`,
	)
	if err := wsjson.Write(ctx, conn, protocol.ChannelMessage{Type: "node.logs", Node: "forged", Payload: payload}); nil != err {
		t.Fatal(err)
	}
	record := output.next(t)
	if `"home"` != string(record["node_id"]) || `"Home"` != string(record["node_name"]) ||
		`"registry denied\nsecond line"` != string(record["stderr"]) {
		t.Fatalf("channel log: %+v", record)
	}
	select {
	case message := <-frames:
		t.Fatalf("operational log leaked into browser events: %s", message)
	default:
	}
	sendRuntime(t, conn, "stack.runtime", `{"name":"old","status":"stopped","services":[]}`)
	awaitCache(t, s, `"status":"stopped"`, `"status":"running"`)
	page := historyPage(t, s.logHistory, logFilter{node: "home", logger: "node.home", search: "second line"})
	if 1 != len(page.Entries) {
		t.Fatalf("central node history: %+v", page)
	}
	entry := page.Entries[0]
	if "node.home" != entry.Logger || "error" != entry.Level || `"home"` != string(entry.Fields["node_id"]) ||
		`"Home"` != string(entry.Fields["node_name"]) ||
		nil == entry.Source ||
		"node.go" != entry.Source.File ||
		42 != entry.Source.Line ||
		"2026-10-29T12:00:00.123456789Z" != entry.Timestamp.Format(time.RFC3339Nano) {
		t.Fatalf("central node entry: %+v", entry)
	}
	if _, exists := entry.Fields["_from_node"]; exists {
		t.Fatal("internal origin marker exposed")
	}
	if 1 != f.count.Load() {
		t.Fatal("log frame interrupted runtime channel")
	}
}
