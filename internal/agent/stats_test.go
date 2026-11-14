package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/protocol"
)

type statsEngine struct {
	engine.Driver
	stats func(context.Context, engine.Stack) ([]engine.ContainerStats, error)
}

func (e *statsEngine) Stats(ctx context.Context, stack engine.Stack) ([]engine.ContainerStats, error) {
	return e.stats(ctx, stack)
}

func TestStreamStats(t *testing.T) {
	a, _ := fixture(t)
	dir := seed(t, a, "demo", "compose.yaml")
	samples := []engine.ContainerStats{
		{
			ID:            "old-id",
			Name:          "demo-web-1",
			CPU:           "1%",
			Memory:        "1MiB / 8MiB",
			MemoryPercent: "12.5%",
			NetworkIO:     "1kB / 2kB",
			BlockIO:       "3kB / 4kB",
			PIDs:          "5",
		},
		{ID: "new-id", Name: "demo-web-1", CPU: "2%"},
	}
	calls := 0
	a.engine = &statsEngine{stats: func(ctx context.Context, stack engine.Stack) ([]engine.ContainerStats, error) {
		deadline, bounded := ctx.Deadline()
		if !bounded || 0 >= time.Until(deadline) || 10*time.Second < time.Until(deadline) {
			t.Fatal("unbounded sample")
		}
		if "demo" != stack.Name || dir != stack.Dir || filepath.Join(dir, "compose.yaml") != stack.ComposeFile {
			t.Fatalf("sample stack: %+v", stack)
		}
		result := samples[calls]
		calls++
		return []engine.ContainerStats{result}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	received := []engine.ContainerStats{}
	err := a.streamStats(ctx, "demo", func(e streamEvent) bool {
		var payload struct {
			Containers []engine.ContainerStats `json:"containers"`
		}
		if "stack.stats" != e.name || nil != json.Unmarshal(e.data, &payload) || 1 != len(payload.Containers) {
			t.Fatalf("stats event: %+v", e)
		}
		received = append(received, payload.Containers[0])
		return 2 > len(received)
	})
	if nil != err || !reflect.DeepEqual(samples, received) {
		t.Fatalf("fresh stats samples: %+v, %v", received, err)
	}
}

func TestStreamStatsEmpty(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	a.engine = &statsEngine{stats: func(context.Context, engine.Stack) ([]engine.ContainerStats, error) {
		return nil, nil
	}}
	received := false
	err := a.streamStats(context.Background(), "demo", func(e streamEvent) bool {
		var payload struct {
			Containers []engine.ContainerStats `json:"containers"`
		}
		if nil != json.Unmarshal(e.data, &payload) || nil == payload.Containers || 0 != len(payload.Containers) {
			t.Fatalf("empty stats payload: %s", e.data)
		}
		received = true
		return false
	})
	if nil != err || !received {
		t.Fatalf("empty stats event missing: %v", err)
	}
}

func statsCommands(t *testing.T, a *agent) (chan<- protocol.ChannelMessage, <-chan protocol.ChannelMessage, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	commands := make(chan protocol.ChannelMessage, 8)
	outgoing := make(chan protocol.ChannelMessage, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.channelCommands(ctx, "stats-test", commands, outgoing, cancel)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("stats channel did not stop")
		}
	})
	return commands, outgoing, cancel
}

func TestChannelStatsCancel(t *testing.T) {
	for _, action := range []string{"detach", "disconnect", "replace"} {
		t.Run(action, func(t *testing.T) {
			a, _ := fixture(t)
			seed(t, a, "demo", "compose.yaml")
			started := make(chan struct{}, 2)
			stopped := make(chan struct{}, 2)
			a.engine = &statsEngine{stats: func(ctx context.Context, _ engine.Stack) ([]engine.ContainerStats, error) {
				started <- struct{}{}
				<-ctx.Done()
				stopped <- struct{}{}
				return nil, ctx.Err()
			}}
			commands, outgoing, cancel := statsCommands(t, a)
			attach := protocol.ChannelMessage{Type: "stats.attach", Resource: "demo", Stream: "stats"}
			commands <- attach
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("stats collection did not start immediately")
			}
			switch action {
			case "detach":
				commands <- protocol.ChannelMessage{Type: "stream.detach", Stream: "stats"}
			case "replace":
				commands <- attach
			case "disconnect":
				cancel()
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("in-flight stats sample was not canceled")
			}
			if "replace" == action {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("replacement stats collection did not start")
				}
			}
			cancel()
			select {
			case message := <-outgoing:
				t.Fatalf("canceled stats emitted an event: %+v", message)
			default:
			}
		})
	}
}

func TestChannelStatsErrors(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	dir := seed(t, a, "removed", "compose.yaml")
	if err := os.Remove(filepath.Join(dir, "compose.yaml")); nil != err {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	a.engine = &statsEngine{stats: func(context.Context, engine.Stack) ([]engine.ContainerStats, error) {
		return nil, &engine.Error{Code: 502, Message: "Could not load stack statistics.", Cause: errors.New("private-runtime-secret")}
	}}
	commands, outgoing, _ := statsCommands(t, a)
	for _, test := range []struct {
		resource, stream string
		status           int
	}{
		{"demo", "", 400},
		{"", "stats", 400},
		{"missing", "stats", 404},
		{"removed", "stats", 404},
		{"../private", "stats", 400},
		{"demo", "stats", 502},
	} {
		commands <- protocol.ChannelMessage{Type: "stats.attach", Resource: test.resource, Stream: test.stream}
		select {
		case message := <-outgoing:
			if "stream.error" != message.Type || test.stream != message.Stream || test.status != message.Status || "" == message.Error ||
				strings.Contains(message.Error, "private") {
				t.Fatalf("stats error: %+v", message)
			}
		case <-time.After(time.Second):
			t.Fatal("stats error missing")
		}
	}
	if strings.Contains(logs.String(), "private-runtime-secret") {
		t.Fatal("stats diagnostics exposed a runtime secret")
	}
	if !strings.Contains(logs.String(), "demo") || !strings.Contains(logs.String(), a.config.Global.Engine) ||
		!strings.Contains(logs.String(), a.config.Global.NodeID) {
		t.Fatal("stats diagnostic omitted stack, engine or node")
	}
}

func TestChannelStats(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	a.engine = &statsEngine{Driver: a.engine, stats: func(context.Context, engine.Stack) ([]engine.ContainerStats, error) {
		return []engine.ContainerStats{{ID: "demo-id", Name: "demo-web-1", CPU: "1%"}}, nil
	}}
	conn := openChannel(t, channelServer(t, a))
	channelSend(t, conn, protocol.ChannelMessage{Type: "stats.attach", Resource: "demo", Stream: "stats"})
	message := channelUntil(t, conn, "stream.event", "stack.stats")
	var payload struct {
		Containers []engine.ContainerStats `json:"containers"`
	}
	if "stats" != message.Stream || nil != json.Unmarshal(message.Payload, &payload) || 1 != len(payload.Containers) ||
		"demo-id" != payload.Containers[0].ID {
		t.Fatalf("channel stats payload: %+v", message)
	}
	channelSend(t, conn, protocol.ChannelMessage{Type: "stream.detach", Stream: "stats"})
}
