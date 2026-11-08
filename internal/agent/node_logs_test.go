package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func nodeLogger(buffer *nodeLogBuffer, local slog.Handler) *slog.Logger {
	return slog.New(&nodeLogHandler{local: local, forward: slog.NewJSONHandler(buffer, &slog.HandlerOptions{AddSource: true}).WithGroup("attrs")})
}

func TestNodeLogHandler(t *testing.T) {
	var local automaticLogBuffer
	var level slog.LevelVar
	buffer := &nodeLogBuffer{}
	logger := nodeLogger(buffer, slog.NewTextHandler(&local, &slog.HandlerOptions{Level: &level}))
	logger.Debug("filtered", "phase", "filtered")
	level.Set(slog.LevelDebug)
	logger.Debug("enabled", "phase", "enabled")
	logger = logger.With("stack", "demo").WithGroup("request").With("service", "web")
	stamp := time.Date(2026, 10, 29, 12, 0, 0, 123456789, time.UTC)
	record := slog.NewRecord(stamp, slog.LevelError, "diagnostic", 0)
	record.AddAttrs(slog.String("image", "example:latest"), slog.String("stderr", "registry denied"), slog.Any("error", errors.New("manifest failure")), slog.Group("operation", "id", "job", "attempt", 2))
	if err := logger.Handler().Handle(context.Background(), record); nil != err {
		t.Fatal(err)
	}
	entries := buffer.take()
	if 2 != len(entries) || slog.LevelDebug != entries[0].Level || slog.LevelError != entries[1].Level || !stamp.Equal(entries[1].Time) {
		t.Fatalf("record metadata: %+v", entries)
	}
	if nil == entries[0].Source || !strings.HasSuffix(entries[0].Source.Function, ".TestNodeLogHandler") || 0 == entries[0].Source.Line || nil != entries[1].Source {
		t.Fatal("original callsite not preserved", entries)
	}
	var attrs struct {
		Service   string `json:"service"`
		Image     string `json:"image"`
		Stderr    string `json:"stderr"`
		Error     string `json:"error"`
		Operation struct {
			ID      string `json:"id"`
			Attempt int    `json:"attempt"`
		} `json:"operation"`
	}
	if err := json.Unmarshal(entries[1].Attrs["request"], &attrs); nil != err {
		t.Fatal(err)
	}
	if `"demo"` != string(entries[1].Attrs["stack"]) || "web" != attrs.Service || "example:latest" != attrs.Image || "registry denied" != attrs.Stderr || "manifest failure" != attrs.Error || "job" != attrs.Operation.ID || 2 != attrs.Operation.Attempt {
		t.Fatalf("structured fields: %+v %+v", entries[1], attrs)
	}
	if strings.Contains(local.text(), "phase=filtered") || !strings.Contains(local.text(), "request.stderr=") || !strings.Contains(local.text(), "level=ERROR") {
		t.Fatalf("local handler not retained: %s", local.text())
	}
}

func TestNodeLogOrigin(t *testing.T) {
	var local automaticLogBuffer
	buffer := &nodeLogBuffer{}
	logger := nodeLogger(buffer, slog.NewTextHandler(&local, nil))
	logger.Error("forwarded", "_from_node", true)
	logger.With("_from_node", true).WithGroup("details").Warn("bound origin")
	if 0 != len(buffer.take()) || 2 != strings.Count(local.text(), "_from_node=true") {
		t.Fatal("forwarded records looped or lost local output")
	}
}

func TestNodeLogBuffer(t *testing.T) {
	buffer := &nodeLogBuffer{}
	logger := nodeLogger(buffer, slog.NewTextHandler(io.Discard, nil))
	for i := 0; nodeLogCapacity+50 > i; i++ {
		logger.Info("queued", "index", i)
	}
	for offset := 50; nodeLogCapacity+50 > offset; offset += 20 {
		entries := buffer.take()
		if 20 != len(entries) {
			t.Fatalf("batch size: %d", len(entries))
		}
		for i, entry := range entries {
			if strconv.Itoa(offset+i) != string(entry.Attrs["index"]) {
				t.Fatalf("recent records not retained: %+v", entry)
			}
		}
	}
	var writers sync.WaitGroup
	for i := 0; 8 > i; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; nodeLogCapacity > i; i++ {
				logger.Warn("concurrent")
			}
		}()
	}
	writers.Wait()
	buffer.close()
	logger.Error("after close")
	if 0 != len(buffer.take()) {
		t.Fatal("closed buffer retained records")
	}
}

func TestNodeLogRestore(t *testing.T) {
	original := slog.Default()
	t.Cleanup(func() { slog.SetDefault(original) })
	previous := slog.New(slog.NewTextHandler(io.Discard, nil))
	slog.SetDefault(previous)
	a := &agent{logs: &nodeLogBuffer{}}
	restore := a.forwardLogs()
	installed := slog.Default()
	restore()
	if previous != slog.Default() {
		t.Fatal("previous logger not restored")
	}
	a.logs = &nodeLogBuffer{}
	restore = a.forwardLogs()
	replacement := slog.New(slog.NewTextHandler(io.Discard, nil))
	slog.SetDefault(replacement)
	restore()
	if replacement != slog.Default() {
		t.Fatal("replacement logger overwritten")
	}
	installed.Info("stale logger")
	if 0 != len(a.logs.take()) {
		t.Fatal("stale logger forwarded after shutdown")
	}
}

func TestChannelNodeLogs(t *testing.T) {
	a, _ := fixture(t)
	logger := nodeLogger(a.logs, slog.NewTextHandler(io.Discard, nil))
	logger.Warn("startup", "phase", "startup")
	server := channelServer(t, a)
	conn := openChannel(t, server)
	if "node.snapshot" != channelNext(t, conn).Event {
		t.Fatal("logs preceded runtime snapshot")
	}
	check := func(phase string, level slog.Level) {
		t.Helper()
		message := channelUntil(t, conn, "node.logs", "")
		var batch protocol.NodeLogs
		if err := json.Unmarshal(message.Payload, &batch); nil != err {
			t.Fatal(err)
		}
		if 1 != len(batch.Entries) || level != batch.Entries[0].Level || strconv.Quote(phase) != string(batch.Entries[0].Attrs["phase"]) {
			t.Fatalf("queued log: %+v", batch)
		}
	}
	check("startup", slog.LevelWarn)
	_ = conn.CloseNow()
	eventually(t, func() bool { a.events.mu.Lock(); defer a.events.mu.Unlock(); return 0 == len(a.events.subscribers) })
	logger.Error("disconnected", "phase", "reconnect")
	conn = openChannel(t, server)
	check("reconnect", slog.LevelError)
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.attach", Shell: "missing"})
	if "missing" != channelUntil(t, conn, "shell.error", "").Shell {
		t.Fatal("logging interrupted command traffic")
	}
}
