package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
	"time"
)

type ConsoleHandler struct {
	attrs  slog.Handler
	output *consoleOutput
}

type consoleOutput struct {
	mu     sync.Mutex
	attrs  bytes.Buffer
	writer io.Writer
}

func NewConsoleHandler(writer io.Writer, level slog.Leveler) *ConsoleHandler {
	output := &consoleOutput{writer: writer}
	return &ConsoleHandler{
		attrs:  slog.NewTextHandler(&output.attrs, &slog.HandlerOptions{Level: level}),
		output: output,
	}
}

func (h *ConsoleHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.attrs.Enabled(ctx, level)
}

func (h *ConsoleHandler) Handle(ctx context.Context, record slog.Record) error {
	h.output.mu.Lock()
	defer h.output.mu.Unlock()
	h.output.attrs.Reset()

	attrs := record.Clone()
	attrs.Time = time.Time{}
	attrs.Level = slog.LevelInfo
	attrs.Message = ""
	if err := h.attrs.Handle(ctx, attrs); nil != err {
		return err
	}

	line := []byte(record.Time.UTC().Format(time.RFC3339Nano))
	line = append(line, ' ')
	line = append(line, record.Level.String()...)
	line = append(line, ' ')
	line = append(line, record.Message...)
	line = append(line, h.output.attrs.Bytes()[len(`level=INFO msg=""`):]...)
	_, err := h.output.writer.Write(line)
	return err
}

func (h *ConsoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ConsoleHandler{attrs: h.attrs.WithAttrs(attrs), output: h.output}
}

func (h *ConsoleHandler) WithGroup(name string) slog.Handler {
	return &ConsoleHandler{attrs: h.attrs.WithGroup(name), output: h.output}
}
