package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket/wsjson"
)

func TestStatsMux(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, node, app := fixture.channel, fixture.server, fixture.node, fixture.app
	one := workspaceSnapshot(t, app.URL, s)
	two := workspaceSnapshot(t, app.URL, s)
	logs := workspaceSnapshot(t, app.URL, s)
	attach := protocol.ChannelMessage{Type: "stats.attach", Stream: "stats", Node: "home", Resource: "demo"}
	browserSend(t, one, attach)
	first := channelCommand(t, f, "stats.attach")
	browserSend(t, two, attach)
	second := channelCommand(t, f, "stats.attach")
	browserSend(t, logs, protocol.ChannelMessage{Type: "logs.attach", Stream: "logs", Node: "home", Resource: "demo"})
	logStream := channelCommand(t, f, "logs.attach")
	if "" == first.Stream || first.Stream == second.Stream || attach.Stream == first.Stream || attach.Stream == second.Stream ||
		"demo" != first.Resource || "demo" != second.Resource {
		t.Fatal("invalid stats attachment routing", first, second)
	}
	payloads := []json.RawMessage{
		json.RawMessage(
			`{"containers":[{"id":"one","name":"demo-web-1","cpu":"1%","memory":"1MiB / 1GiB","memory_percent":"0.1%","network_io":"1kB / 2kB","block_io":"0B / 0B","pids":"2"}]}`,
		),
		json.RawMessage(`{"containers":[]}`),
	}
	for index, command := range []protocol.ChannelMessage{first, second} {
		browserSend(
			t,
			node,
			protocol.ChannelMessage{Type: "stream.event", Stream: command.Stream, Event: "stack.stats", Payload: payloads[index]},
		)
	}
	if got := resourceMessage(
		t,
		one,
	); "event" != got.Type || "stats" != got.Stream || "stack.stats" != got.Event ||
		string(payloads[0]) != string(got.Payload) {
		t.Fatal("wrong first stats delivery", got)
	}
	if got := resourceMessage(t, two); "stats" != got.Stream || "stack.stats" != got.Event || string(payloads[1]) != string(got.Payload) {
		t.Fatal("stats crossed browser subscriptions", got)
	}
	browserSend(
		t,
		node,
		protocol.ChannelMessage{
			Type:    "stream.event",
			Stream:  logStream.Stream,
			Event:   "message",
			Payload: json.RawMessage(`{"message":"logs"}`),
		},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, logs, &message); nil != err {
			t.Fatal(err)
		}
		if "stack.stats" == message.Event || ("" != message.Stream && "logs" != message.Stream) {
			t.Fatal("stats reached log reader", message)
		}
		if "logs" == message.Stream {
			if "message" != message.Event {
				t.Fatal("wrong log event", message)
			}
			break
		}
	}
	browserSend(t, logs, protocol.ChannelMessage{Type: "workspace.sync"})
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, logs, &message); nil != err {
			t.Fatal(err)
		}
		if "stack.stats" == message.Event || strings.Contains(string(message.Payload), "memory_percent") {
			t.Fatal("stats entered workspace snapshot", message)
		}
		if "node.snapshot" == message.Event {
			break
		}
	}
	browserSend(t, one, protocol.ChannelMessage{Type: "stream.detach", Stream: "stats"})
	if detach := channelCommand(t, f, "stream.detach"); first.Stream != detach.Stream {
		t.Fatal("detached another stats subscription", detach)
	}
	browserSend(t, node, protocol.ChannelMessage{Type: "stream.event", Stream: first.Stream, Event: "stack.stats", Payload: payloads[0]})
	sendRuntime(t, node, "stack.removed", `{"name":"demo"}`)
	for {
		var message protocol.ChannelMessage
		if err := wsjson.Read(ctx, one, &message); nil != err {
			t.Fatal(err)
		}
		if "stats" == message.Stream || "stack.stats" == message.Event {
			t.Fatal("detached stats still delivered", message)
		}
		if "stack.removed" == message.Event {
			break
		}
	}
	_ = two.CloseNow()
	if detach := channelCommand(t, f, "stream.detach"); second.Stream != detach.Stream {
		t.Fatal("browser close did not detach stats", detach)
	}
	if 1 != f.count.Load() {
		t.Fatal("stats opened another node connection", f.count.Load())
	}
}

func TestStatsRouting(t *testing.T) {
	s := testServer(t, nil)
	channel := &nodeChannel{streams: make(map[string]*browserSubscription)}
	for _, kind := range []string{"stats.attach", "logs.attach", "relay.attach"} {
		subscription := &browserSubscription{kind: kind, subscriber: make(chan protocol.ChannelMessage, 4)}
		channel.streams[kind] = subscription
		s.streamEvent(
			channel,
			protocol.ChannelMessage{
				Type:    "stream.event",
				Stream:  kind,
				Event:   "stack.stats",
				Payload: json.RawMessage(`{"containers":[]}`),
			},
		)
		if "stats.attach" == kind {
			if 1 != len(subscription.subscriber) {
				t.Fatal("stats event was not delivered")
			}
			if got := <-subscription.subscriber; "event" != got.Type || "stack.stats" != got.Event {
				t.Fatal("missing stats event", got)
			}
		}
		s.streamEvent(
			channel,
			protocol.ChannelMessage{Type: "stream.event", Stream: kind, Event: "stack.stats", Payload: json.RawMessage(`{`)},
		)
		if "stats.attach" == kind {
			for _, event := range []string{"message", "relay.output", "done"} {
				s.streamEvent(
					channel,
					protocol.ChannelMessage{Type: "stream.event", Stream: kind, Event: event, Payload: json.RawMessage(`{}`)},
				)
			}
		}
		if 0 != len(subscription.subscriber) {
			t.Fatal("unexpected stats routing", kind)
		}
	}
}

func TestStatsValidation(t *testing.T) {
	for _, test := range []struct {
		name, stream, node, resource string
		paused, runtimePaused        bool
		status                       int
	}{
		{name: "stream", node: "home", resource: "demo", status: 400},
		{name: "node", stream: "stats", node: "../home", resource: "demo", status: 400},
		{name: "stack", stream: "stats", node: "home", resource: "../demo", status: 400},
		{name: "empty", stream: "stats", node: "home", status: 400},
		{name: "missing", stream: "stats", node: "missing", resource: "demo", status: 404},
		{name: "paused", stream: "stats", node: "home", resource: "demo", paused: true, status: 409},
		{name: "runtime", stream: "stats", node: "home", resource: "demo", runtimePaused: true, status: 409},
		{name: "offline", stream: "stats", node: "home", resource: "demo", status: 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := testServer(t, nil)
			s.config.Nodes["home"] = NodeConfig{Paused: test.paused}
			if test.runtimePaused {
				s.runtime["home"] = &nodeRuntime{state: map[string]json.RawMessage{"state": json.RawMessage(`"paused"`)}}
			}
			subscription, response := s.attachBrowser(
				context.Background(),
				protocol.ChannelMessage{Type: "stats.attach", Stream: test.stream, Node: test.node, Resource: test.resource},
			)
			if nil != subscription || nil == response || "error" != response.Type || test.status != response.Status ||
				test.stream != response.Stream {
				t.Fatal("invalid stats attachment accepted", subscription, response)
			}
		})
	}
}

func TestStatsAuth(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, app := fixture.channel, fixture.server, fixture.app
	browser := workspaceSnapshot(t, app.URL, s)
	s.configMu.Lock()
	s.config.Auth.Username = "changed"
	s.configMu.Unlock()
	browserSend(t, browser, protocol.ChannelMessage{Type: "stats.attach", Stream: "stats", Node: "home", Resource: "demo"})
	if got := resourceMessage(t, browser); "error" != got.Type || http.StatusUnauthorized != got.Status || 0 != len(f.commands) {
		t.Fatal("revoked session attached stats", got)
	}
}

func TestStatsError(t *testing.T) {
	fixture := newWorkspace(t)
	f, s, node, app := fixture.channel, fixture.server, fixture.node, fixture.app
	browser := workspaceSnapshot(t, app.URL, s)
	browserSend(t, browser, protocol.ChannelMessage{Type: "stats.attach", Stream: "stats", Node: "home", Resource: "demo"})
	attach := channelCommand(t, f, "stats.attach")
	browserSend(
		t,
		node,
		protocol.ChannelMessage{Type: "stream.error", Stream: attach.Stream, Status: 404, Error: "private stats diagnostic"},
	)
	if got := resourceMessage(
		t,
		browser,
	); "error" != got.Type || "stats" != got.Stream || 404 != got.Status ||
		strings.Contains(got.Error, "private") {
		t.Fatal("stats error changed status or leaked details", got)
	}
	if detach := channelCommand(t, f, "stream.detach"); attach.Stream != detach.Stream {
		t.Fatal("failed stats stream was not detached", detach)
	}
}
