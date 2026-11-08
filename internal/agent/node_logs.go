package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/arabcoders/dcompose/internal/protocol"
)

const nodeLogCapacity = 100

type nodeLogBuffer struct {
	mu      sync.Mutex
	entries []protocol.NodeLog
	closed  bool
}

// Write receives complete records from slog.JSONHandler. Encoding through the
// standard handler preserves resolved values, errors, WithAttrs and WithGroup.
func (b *nodeLogBuffer) Write(data []byte) (int, error) {
	var entry protocol.NodeLog
	if err := json.Unmarshal(data, &entry); nil != err {
		return len(data), nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return len(data), nil
	}
	if nodeLogCapacity == len(b.entries) {
		copy(b.entries, b.entries[1:])
		b.entries = b.entries[:len(b.entries)-1]
	}
	b.entries = append(b.entries, entry)
	return len(data), nil
}

func (b *nodeLogBuffer) take() []protocol.NodeLog {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := min(20, len(b.entries))
	entries := append([]protocol.NodeLog(nil), b.entries[:count]...)
	copy(b.entries, b.entries[count:])
	clear(b.entries[len(b.entries)-count:])
	b.entries = b.entries[:len(b.entries)-count]
	return entries
}

func (b *nodeLogBuffer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.entries = nil
}

type nodeLogHandler struct {
	local    slog.Handler
	forward  slog.Handler
	fromNode bool
}

func (h *nodeLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.local.Enabled(ctx, level)
}

func (h *nodeLogHandler) Handle(ctx context.Context, record slog.Record) error {
	err := h.local.Handle(ctx, record)
	fromNode := h.fromNode
	record.Attrs(func(attr slog.Attr) bool {
		fromNode = fromNode || nodeLogOrigin(attr)
		return !fromNode
	})
	if !fromNode {
		_ = h.forward.Handle(ctx, record)
	}
	return err
}

func (h *nodeLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	fromNode := h.fromNode
	for _, attr := range attrs {
		fromNode = fromNode || nodeLogOrigin(attr)
	}
	return &nodeLogHandler{local: h.local.WithAttrs(attrs), forward: h.forward.WithAttrs(attrs), fromNode: fromNode}
}

func (h *nodeLogHandler) WithGroup(name string) slog.Handler {
	return &nodeLogHandler{local: h.local.WithGroup(name), forward: h.forward.WithGroup(name), fromNode: h.fromNode}
}

func nodeLogOrigin(attr slog.Attr) bool {
	value := attr.Value.Resolve()
	return "_from_node" == attr.Key && slog.KindBool == value.Kind() && value.Bool()
}

func (a *agent) forwardLogs() func() {
	previous := slog.Default()
	logger := slog.New(&nodeLogHandler{
		local:   previous.Handler(),
		forward: slog.NewJSONHandler(a.logs, &slog.HandlerOptions{AddSource: true}).WithGroup("attrs"),
	})
	slog.SetDefault(logger)
	return func() {
		a.logs.close()
		// A caller may have installed another logger while the node was running.
		if logger == slog.Default() {
			slog.SetDefault(previous)
		}
	}
}
