package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
)

func TestViewWriteLogs(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if nil == err {
			_ = conn.CloseNow()
		}
	}))
	defer up.Close()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(up.URL, "http"), nil)
	if nil != err {
		t.Fatal(err)
	}
	_ = conn.CloseNow()
	for _, test := range []struct {
		kind, resource, shell string
	}{
		{kind: "logs.attach", resource: "demo"},
		{kind: "relay.attach", resource: "self-helper"},
		{kind: "shell.attach", shell: "terminal"},
		{kind: "shell.input", shell: "terminal"},
		{kind: "shell.resize", shell: "terminal"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			var output bytes.Buffer
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(original) })
			s := testServer(t, nil)
			node := NodeConfig{Name: "Home", URL: "https://user:secret@node.invalid/private?token=secret", token: "agent-test-token"}
			s.config.Nodes["home"] = node
			channel := &nodeChannel{
				node:    node,
				conn:    conn,
				ctx:     context.Background(),
				streams: make(map[string]*browserSubscription),
				shells:  make(map[string]map[chan protocol.ChannelMessage]struct{}),
			}
			s.channels["home"] = channel
			message := protocol.ChannelMessage{
				Type:     test.kind,
				Stream:   "browser",
				Node:     "home",
				Resource: test.resource,
				Shell:    test.shell,
				Data:     "secret-shell-input",
				Cols:     80,
				Rows:     24,
			}
			var response *protocol.ChannelMessage
			if "relay.attach" == test.kind {
				_, response = s.attachNode(context.Background(), message)
			} else if strings.HasSuffix(test.kind, ".attach") {
				_, response = s.attachBrowser(context.Background(), message)
			} else {
				response = s.browserShellCommand(
					context.Background(),
					&browserSubscription{shell: test.shell, channel: channel, conn: conn},
					message,
				)
			}
			if nil == response || http.StatusBadGateway != response.Status {
				t.Fatal("write failure changed browser error", response)
			}
			var record struct {
				Message   string `json:"msg"`
				Level     string `json:"level"`
				Node      string `json:"node"`
				Operation string `json:"operation"`
				Error     string `json:"error"`
			}
			if err := json.Unmarshal(
				output.Bytes(),
				&record,
			); nil != err || "ERROR" != record.Level || !strings.Contains(record.Node, node.Name) || !strings.Contains(record.Node, "https://node.invalid") || "" == record.Operation ||
				"" == record.Error {
				t.Fatal("missing write failure diagnostic", output.String(), err)
			}
			resource := test.resource
			if "" != test.shell {
				resource = test.shell
			}
			for _, detail := range []string{node.Name, "https://node.invalid", record.Error, resource} {
				if !strings.Contains(record.Message, detail) {
					t.Fatal("write message hides context", record.Message)
				}
			}
			if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "/private") ||
				strings.Contains(response.Error, record.Error) {
				t.Fatal("write failure leaked private details", output.String(), response)
			}
		})
	}
}

func TestViewErrorLogs(t *testing.T) {
	for _, kind := range []string{"logs.attach", "relay.attach"} {
		t.Run(kind, func(t *testing.T) {
			var output bytes.Buffer
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(original) })
			s := testServer(t, nil)
			channel := &nodeChannel{
				node:    NodeConfig{Name: "Home", URL: "https://node.invalid"},
				streams: make(map[string]*browserSubscription),
			}
			subscription := &browserSubscription{
				kind:       kind,
				resource:   "demo",
				stream:     "stream-id",
				subscriber: make(chan protocol.ChannelMessage, 1),
			}
			channel.streams[subscription.stream] = subscription
			const cause = "private diagnostic: cannot open /srv/demo/compose.yml"
			s.streamEvent(channel, protocol.ChannelMessage{Type: "stream.error", Stream: subscription.stream, Status: 404, Error: cause})
			response := <-subscription.subscriber
			if "error" != response.Type || 404 != response.Status || strings.Contains(response.Error, cause) {
				t.Fatal("stream error changed browser error", response)
			}
			var record struct {
				Message   string `json:"msg"`
				Level     string `json:"level"`
				Operation string `json:"operation"`
				Resource  string `json:"resource"`
				Stream    string `json:"stream"`
				Status    int    `json:"status"`
				Error     string `json:"error"`
			}
			if err := json.Unmarshal(
				output.Bytes(),
				&record,
			); nil != err || "ERROR" != record.Level || kind != record.Operation || "demo" != record.Resource || "stream-id" != record.Stream || 404 != record.Status ||
				cause != record.Error {
				t.Fatal("missing stream failure diagnostic", output.String(), err)
			}
			for _, detail := range []string{channel.node.Name, channel.node.URL, subscription.resource, cause, "404"} {
				if !strings.Contains(record.Message, detail) {
					t.Fatal("stream message hides context", record.Message)
				}
			}
		})
	}
}
