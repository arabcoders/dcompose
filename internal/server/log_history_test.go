package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/logging"
)

func startLogHistory(t *testing.T, s *Server) *bytes.Buffer {
	t.Helper()
	s.config.path = filepath.Join(t.TempDir(), "dcompose.toml")
	var stderr bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	if err := s.StartLogHistory(); nil != err {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return &stderr
}

func historyPage(t *testing.T, h *logHistory, filter logFilter) logPage {
	t.Helper()
	if 0 == filter.limit {
		filter.limit = 100
	}
	page, err := h.query(filter)
	if nil != err {
		t.Fatal(err)
	}
	return page
}

func TestHistoryCapture(t *testing.T) {
	s := testServer(t, nil)
	stderr := startLogHistory(t, s)
	slog.With("service", "web").WithGroup("details").Error("diagnostic", "error", errors.New("manifest failure"), "codes", []int{401, 403}, "automatic", true, "attempt", int64(9_007_199_254_740_993))
	page := historyPage(t, s.logHistory, logFilter{})
	if 1 != len(page.Entries) || nil != page.NextBefore || page.LatestID != page.Entries[0].ID || nil == page.OldestID || page.Truncated {
		t.Fatalf("page: %+v", page)
	}
	entry := page.Entries[0]
	if "error" != entry.Level || "server" != entry.Logger || `"web"` != string(entry.Fields["service"]) || entry.Timestamp.IsZero() || nil == entry.Source || !strings.HasSuffix(entry.Source.Function, ".TestHistoryCapture") || 0 == entry.Source.Line {
		t.Fatalf("entry: %+v", entry)
	}
	var details map[string]json.RawMessage
	if nil != json.Unmarshal(entry.Fields["details"], &details) || `"manifest failure"` != string(details["error"]) || "9007199254740993" != string(details["attempt"]) || "true" != string(details["automatic"]) || "[401,403]" != string(details["codes"]) {
		t.Fatalf("fields: %s", entry.Fields["details"])
	}
	if !strings.Contains(stderr.String(), "manifest failure") {
		t.Fatal("local output lost")
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(s.logHistory.path): 0700, s.logHistory.path: 0600} {
		stat, err := os.Stat(path)
		if nil != err || mode != stat.Mode().Perm() {
			t.Fatal(path, stat, err)
		}
	}
}

func TestHistoryRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "dcompose.log")
	h, err := openLogHistory(path)
	if nil != err {
		t.Fatal(err)
	}
	h.maxBytes = 1
	h.latestID = time.Now().Add(time.Hour).UnixMicro()
	logger := slog.New(slog.NewJSONHandler(h, nil).WithGroup("fields"))
	var ids []int64
	for index := 0; 6 > index; index++ {
		logger.Info("diagnostic", "index", index)
		ids = append(ids, h.latestID)
	}
	page := historyPage(t, h, logFilter{after: ids[0]})
	if 4 != len(page.Entries) || !page.Truncated || ids[2] != *page.OldestID || ids[5] != page.LatestID {
		t.Fatalf("retention: %+v", page)
	}
	if err := h.close(); nil != err {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".4"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("too many backups", err)
	}
	h, err = openLogHistory(path)
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.close() })
	logger = slog.New(slog.NewJSONHandler(h, nil).WithGroup("fields"))
	logger.Info("after restart")
	page = historyPage(t, h, logFilter{after: ids[5]})
	if 1 != len(page.Entries) || ids[5]+1 != page.Entries[0].ID || 9_007_199_254_740_991 < page.LatestID {
		t.Fatalf("restart cursor: %+v", page)
	}
}

func TestHistoryPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dcompose.log")
	if err := os.WriteFile(path, []byte(`{"id":`), 0600); nil != err {
		t.Fatal(err)
	}
	h, err := openLogHistory(path)
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.close() })
	slog.New(slog.NewJSONHandler(h, nil)).Info("diagnostic")
	if 1 != len(historyPage(t, h, logFilter{}).Entries) {
		t.Fatal("partial final record hid new logs")
	}
}

func TestHistoryPages(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	slog.Warn("first", "image", "example:latest")
	slog.Info("gap")
	slog.Warn("second", "image", "example:latest")
	slog.Info("last")
	filter := logFilter{limit: 1, levels: map[string]bool{"warning": true}, node: "server", logger: "server", search: "example"}
	page := historyPage(t, s.logHistory, filter)
	if 1 != len(page.Entries) || nil == page.NextBefore || "second" != page.Entries[0].Message || page.LatestID == page.Entries[0].ID {
		t.Fatalf("latest filtered page: %+v", page)
	}
	filter.before = *page.NextBefore
	page = historyPage(t, s.logHistory, filter)
	if 1 != len(page.Entries) || nil != page.NextBefore || "first" != page.Entries[0].Message {
		t.Fatalf("older filtered page: %+v", page)
	}
	filter.search = "absent"
	page = historyPage(t, s.logHistory, filter)
	if nil == page.Entries || 0 != len(page.Entries) || 0 == page.LatestID {
		t.Fatalf("empty filter: %+v", page)
	}
}

func TestHistoryNodeOrder(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	slog.Info("local")
	channel := &nodeChannel{id: "home", node: NodeConfig{Name: "Home"}, ctx: context.Background()}
	payload := json.RawMessage(`{"entries":[{"time":"2026-10-29T12:00:00Z","level":"ERROR","msg":"newer","attrs":{"stderr":"registry denied"}},{"time":"2026-10-28T12:00:00Z","level":"WARN","msg":"buffered","attrs":{"platforms":{"linux/amd64":"example:latest"}}}]}`)
	if err := s.nodeLogs(channel, payload); nil != err {
		t.Fatal(err)
	}
	page := historyPage(t, s.logHistory, logFilter{})
	if 3 != len(page.Entries) || page.Entries[0].ID >= page.Entries[1].ID || page.Entries[1].ID >= page.Entries[2].ID || !page.Entries[2].Timestamp.Before(page.Entries[1].Timestamp) || nil != page.Entries[1].Source {
		t.Fatalf("append/source order: %+v", page)
	}
	filtered := historyPage(t, s.logHistory, logFilter{node: "home", search: "linux/amd64"})
	if 1 != len(filtered.Entries) || "warning" != filtered.Entries[0].Level || `{"linux/amd64":"example:latest"}` != string(filtered.Entries[0].Fields["platforms"]) {
		t.Fatalf("platform diagnostic fields: %+v", filtered)
	}
}

func TestHistorySearch(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	channel := &nodeChannel{id: "home", node: NodeConfig{Name: "Home"}, ctx: context.Background()}
	payload := json.RawMessage(`{"entries":[{"time":"2026-10-29T12:00:00Z","level":"ERROR","msg":"diagnostic","source":{"file":"node.go","function":"node.check","line":42},"attrs":{"stderr":"Échec\nRegistry DENIED","attempt":9007199254740993}}]}`)
	if err := s.nodeLogs(channel, payload); nil != err {
		t.Fatal(err)
	}
	for _, search := range []string{"node.home", "node.check", "node.go", "2026-10-29", "error", "registry denied", "échec\nregistry", "9007199254740993"} {
		page := historyPage(t, s.logHistory, logFilter{search: search})
		if 1 != len(page.Entries) {
			t.Fatal("missing search match", search)
		}
	}
}

func TestHistorySubscription(t *testing.T) {
	h, err := openLogHistory(filepath.Join(t.TempDir(), "dcompose.log"))
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.close() })
	logger := slog.New(slog.NewJSONHandler(h, nil))
	logger.Info("backlog")
	subscriber, backlog, status, err := h.subscribe(logFilter{})
	if nil != err || 1 != len(backlog) || nil == status.OldestID || backlog[0].ID != *status.OldestID || status.Truncated {
		t.Fatal(backlog, err)
	}
	logger.Info("live")
	live := <-subscriber
	if backlog[0].ID >= live.ID {
		t.Fatal("backlog/live order")
	}
	for index := 0; 129 > index; index++ {
		logger.Info("slow reader")
	}
	count := 0
	for range subscriber {
		count++
	}
	if 128 != count {
		t.Fatal("slow reader queue", count)
	}
	page := historyPage(t, h, logFilter{after: live.ID, limit: 500})
	if 129 != len(page.Entries) {
		t.Fatal("slow reader cannot recover from history")
	}
}

func TestHistoryRestore(t *testing.T) {
	s := testServer(t, nil)
	startLogHistory(t, s)
	previous := s.logPrevious
	s.Close()
	if previous != slog.Default() {
		t.Fatal("previous logger not restored")
	}
	other := slog.New(slog.NewTextHandler(io.Discard, nil))
	slog.SetDefault(other)
	s.Close()
	if other != slog.Default() {
		t.Fatal("replacement logger overwritten")
	}
	// Loggers retained by callers after shutdown still reach their local handler.
	if err := s.logLogger.Handler().Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "late", 0)); nil != err {
		t.Fatal(err)
	}
}

func TestHistoryRendered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dcompose.log")
	h, err := openLogHistory(path)
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.close() })
	h.maxBytes = 1
	var local bytes.Buffer
	logger := slog.New(newHistoryHandler(logging.NewConsoleHandler(&local, slog.LevelDebug), h))
	logger = logger.With("node", "home").WithGroup("request").With("operation", "deploy")
	stamp := time.Date(2026, 10, 29, 12, 0, 0, 123456789, time.UTC)
	message := `node "local" path "C:\stacks\compose.yaml"`
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelWarn, slog.LevelError} {
		record := slog.NewRecord(stamp, level, message, 0)
		record.AddAttrs(slog.String("image", "example:latest"), slog.String("stderr", "first line\nsecond line"), slog.Any("error", errors.New("manifest failure")), slog.Group("details", "attempt", 2, "automatic", true))
		if err := logger.Handler().Handle(context.Background(), record); nil != err {
			t.Fatal(err)
		}
	}
	lines := strings.SplitAfter(local.String(), "\n")
	page := historyPage(t, h, logFilter{})
	if 3 != len(page.Entries) {
		t.Fatal("missing rendered records", page)
	}
	for index, entry := range page.Entries {
		if lines[index] != entry.Rendered || message != entry.Message || !strings.Contains(entry.Rendered, " "+message+" node=home ") {
			t.Fatalf("stderr/rendered mismatch: %q != %q", lines[index], entry.Rendered)
		}
	}
	if err := h.close(); nil != err {
		t.Fatal(err)
	}
	h, err = openLogHistory(path)
	if nil != err {
		t.Fatal(err)
	}
	page = historyPage(t, h, logFilter{})
	if 3 != len(page.Entries) {
		t.Fatal("missing rendered records after rotation/restart", page)
	}
	for index, entry := range page.Entries {
		if lines[index] != entry.Rendered {
			t.Fatal("rendered record changed after rotation/restart", entry)
		}
	}
}

func TestHistoryRenderedConcurrent(t *testing.T) {
	h, err := openLogHistory(filepath.Join(t.TempDir(), "dcompose.log"))
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.close() })
	var local bytes.Buffer
	logger := slog.New(newHistoryHandler(logging.NewConsoleHandler(&local, nil), h))
	var workers sync.WaitGroup
	for worker := 0; 8 > worker; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			bound := logger.With("worker", worker).WithGroup("entry")
			for index := 0; 20 > index; index++ {
				bound.Info("diagnostic", "index", index, "error", errors.New("manifest failure"))
			}
		}()
	}
	workers.Wait()
	lines := make(map[string]int)
	for _, line := range strings.SplitAfter(local.String(), "\n") {
		if "" != line {
			lines[line]++
		}
	}
	page := historyPage(t, h, logFilter{limit: 500})
	if 160 != len(page.Entries) {
		t.Fatal("missing concurrent records", len(page.Entries))
	}
	for _, entry := range page.Entries {
		var fields struct {
			Index int `json:"index"`
		}
		if nil != json.Unmarshal(entry.Fields["entry"], &fields) || 0 == lines[entry.Rendered] || !strings.Contains(entry.Rendered, " worker="+string(entry.Fields["worker"])+" ") || !strings.Contains(entry.Rendered, " entry.index="+strconv.Itoa(fields.Index)+" ") {
			t.Fatal("concurrent rendered record mixed", entry)
		}
		lines[entry.Rendered]--
	}
}

func TestHistoryRenderedLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dcompose.log")
	legacy := []byte(`{"id":123,"timestamp":"2026-10-29T12:00:00.123456789Z","level":"warning","logger":"server","message":"node \"local\" path \"C:\\stacks\\compose.yaml\"","fields":{"stderr":"first line\nsecond line","error":"manifest failure","automatic":true,"attempt":9007199254740993,"codes":[401,403],"details":{"service":"web"}}}` + "\n")
	if err := os.WriteFile(path, legacy, 0600); nil != err {
		t.Fatal(err)
	}
	h, err := openLogHistory(path)
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.close() })
	stamp := time.Date(2026, 10, 29, 12, 0, 0, 123456789, time.UTC)
	message := `node "local" path "C:\stacks\compose.yaml"`
	record := slog.NewRecord(stamp, slog.LevelWarn, message, 0)
	record.AddAttrs(slog.Any("attempt", json.Number("9007199254740993")), slog.Bool("automatic", true), slog.Any("codes", []json.Number{"401", "403"}), slog.Any("details", map[string]string{"service": "web"}), slog.String("error", "manifest failure"), slog.String("stderr", "first line\nsecond line"))
	var expected bytes.Buffer
	if err := logging.NewConsoleHandler(&expected, nil).Handle(context.Background(), record); nil != err {
		t.Fatal(err)
	}
	page := historyPage(t, h, logFilter{})
	if 1 != len(page.Entries) || expected.String() != page.Entries[0].Rendered || !strings.Contains(page.Entries[0].Rendered, " "+message+" ") || "9007199254740993" != string(page.Entries[0].Fields["attempt"]) {
		t.Fatal("legacy rendered fallback lost content", page)
	}
	subscriber, backlog, _, err := h.subscribe(logFilter{})
	if nil != err || 1 != len(backlog) || expected.String() != backlog[0].Rendered {
		t.Fatal("legacy stream fallback lost content", backlog, err)
	}
	h.unsubscribe(subscriber)
	unchanged, err := os.ReadFile(path)
	if nil != err || !bytes.Equal(legacy, unchanged) {
		t.Fatal("legacy history was rewritten", err)
	}
}

func TestHistoryRenderedPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dcompose.log")
	rendered := "time=2026-10-29T12:00:00Z level=INFO msg=\"node \\\"local\\\"\"\n"
	entry := logEntry{ID: 123, Timestamp: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), Level: "info", Logger: "server", Message: `node "local"`, Rendered: rendered}
	data, err := json.Marshal(entry)
	if nil != err {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0600); nil != err {
		t.Fatal(err)
	}
	h, err := openLogHistory(path)
	if nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.close() })
	page := historyPage(t, h, logFilter{})
	if 1 != len(page.Entries) || rendered != page.Entries[0].Rendered {
		t.Fatal("persisted rendered text changed", page)
	}
	unchanged, err := os.ReadFile(path)
	if nil != err || !bytes.Equal(data, unchanged) {
		t.Fatal("persisted history was rewritten", err)
	}
}
