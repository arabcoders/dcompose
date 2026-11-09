package logging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConsoleMessage(t *testing.T) {
	stamp := time.Date(2026, 10, 29, 14, 0, 0, 123456789, time.FixedZone("local", 2*60*60))
	for _, message := range []string{`node "local" path "C:\stacks\compose.yaml"`, `quoted "value" and literal \"value\"`} {
		var output bytes.Buffer
		handler := NewConsoleHandler(&output, nil)
		record := slog.NewRecord(stamp, slog.LevelWarn, message, 0)
		if err := handler.Handle(context.Background(), record); nil != err {
			t.Fatal(err)
		}
		if expected := "2026-10-29T12:00:00.123456789Z WARN " + message + "\n"; expected != output.String() {
			t.Fatalf("message changed: %q", output.String())
		}
	}
}

func TestConsoleAttrs(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(NewConsoleHandler(&output, nil))
	logger = logger.With("node", "local", "msg", "bound").WithGroup("request").With("operation", "deploy").WithGroup("details")
	logger.Info("diagnostic", "attempt", int64(9_007_199_254_740_993), "maximum", ^uint64(0), "path", `C:\stacks\compose.yaml`, "error", errors.New("failure"), "codes", []int{401, 403}, "automatic", true, slog.Group("nested", "value", 2), slog.Group("", "inline", 3))
	for _, attr := range []string{`node=local`, `msg=bound`, `request.operation=deploy`, `request.details.attempt=9007199254740993`, `request.details.maximum=18446744073709551615`, `request.details.path=C:\stacks\compose.yaml`, `request.details.error=failure`, `request.details.codes="[401 403]"`, `request.details.automatic=true`, `request.details.nested.value=2`, `request.details.inline=3`} {
		if !strings.Contains(output.String(), " "+attr) {
			t.Fatalf("missing attribute %q: %s", attr, output.String())
		}
	}
}

func TestConsoleLevel(t *testing.T) {
	var output bytes.Buffer
	var level slog.LevelVar
	logger := slog.New(NewConsoleHandler(&output, &level)).With("node", "local").WithGroup("request")
	ctx := context.Background()
	if logger.Enabled(ctx, slog.LevelDebug) || !logger.Enabled(ctx, slog.LevelInfo) {
		t.Fatal("initial level")
	}
	logger.Debug("hidden")
	if 0 != output.Len() {
		t.Fatal("disabled record written")
	}
	level.Set(slog.LevelDebug)
	if !logger.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("runtime level not applied")
	}
	logger.Debug("visible")
	if 0 == output.Len() {
		t.Fatal("enabled record missing")
	}
	level.Set(slog.LevelError)
	if logger.Enabled(ctx, slog.LevelWarn) || !logger.Enabled(ctx, slog.LevelError) {
		t.Fatal("updated level not applied")
	}
}

func TestConsoleConcurrent(t *testing.T) {
	var output bytes.Buffer
	handler := NewConsoleHandler(&output, nil)
	stamp := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var workers sync.WaitGroup
	for worker := 0; 8 > worker; worker++ {
		workers.Go(func() {
			bound := handler.WithAttrs([]slog.Attr{slog.Int("worker", worker)}).WithGroup("entry")
			for index := 0; 20 > index; index++ {
				record := slog.NewRecord(stamp, slog.LevelInfo, fmt.Sprintf(`node "%d" path "C:\%d"`, worker, index), 0)
				record.AddAttrs(slog.Int("index", index))
				if err := bound.Handle(context.Background(), record); nil != err {
					t.Error(err)
				}
			}
		})
	}
	workers.Wait()
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if 160 != len(lines) {
		t.Fatal("missing lines", len(lines))
	}
	actual := make(map[string]int)
	for _, line := range lines {
		actual[line]++
	}
	for worker := 0; 8 > worker; worker++ {
		for index := 0; 20 > index; index++ {
			expected := fmt.Sprintf(`2026-10-29T12:00:00Z INFO node "%d" path "C:\%d" worker=%d entry.index=%d`, worker, index, worker, index)
			if 1 != actual[expected] {
				t.Fatalf("mixed or missing line: %q", expected)
			}
		}
	}
}

type consoleWriter struct {
	writes int
	err    error
}

func (w *consoleWriter) Write(data []byte) (int, error) {
	w.writes++
	if nil != w.err {
		return 0, w.err
	}
	return len(data), nil
}

func TestConsoleWriter(t *testing.T) {
	wantErr := errors.New("writer failure")
	writer := &consoleWriter{err: wantErr}
	handler := NewConsoleHandler(writer, nil).WithAttrs([]slog.Attr{slog.String("node", "local")}).WithGroup("request")
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "diagnostic", 0)
	if err := handler.Handle(context.Background(), record); !errors.Is(err, wantErr) || 1 != writer.writes {
		t.Fatal("writer failure not propagated atomically", err, writer.writes)
	}
	writer.err = nil
	if err := handler.Handle(context.Background(), record); nil != err || 2 != writer.writes {
		t.Fatal("writer did not recover", err, writer.writes)
	}
}
