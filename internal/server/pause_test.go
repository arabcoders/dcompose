package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/protocol"
)

func savePause(t *testing.T, s *Server, paused *bool) {
	t.Helper()
	c, _ := s.snapshot()
	node := c.Nodes["home"]
	body := nodeConnectionRequest{Name: node.Name, URL: node.URL, Token: node.token, Fingerprint: node.Fingerprint, PublicHost: node.PublicHost, Paused: paused}
	data, err := json.Marshal(body)
	if nil != err {
		t.Fatal(err)
	}
	w := perform(t, s, "PUT", "/api/settings/nodes/home", string(data))
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestNodePauseSaved(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer up.Close()
	s := persistedServer(t, up)
	c := cloneConfig(s.config)
	node := c.Nodes["home"]
	node.Token = "file:secrets/node.token"
	if err := configutil.Write(filepath.Join(filepath.Dir(c.path), "secrets/node.token"), []byte(node.token)); nil != err {
		t.Fatal(err)
	}
	c.Nodes["home"] = node
	s.configMu.Lock()
	s.applyConfig(c)
	s.configMu.Unlock()
	paused := true
	savePause(t, s, &paused)
	savePause(t, s, nil)
	loaded, err := LoadConfig(c.path)
	if nil != err || !loaded.Nodes["home"].Paused || node.Token != loaded.Nodes["home"].Token || node.token != loaded.Nodes["home"].token {
		t.Fatal("pause or token reference was not preserved", err)
	}
	for _, path := range []string{"/api/nodes", "/api/settings"} {
		w := perform(t, s, "GET", path, "")
		var response struct {
			Nodes []safeNode `json:"nodes"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || 1 != len(response.Nodes) || !response.Nodes[0].Paused {
			t.Fatal("missing public paused flag", path, err)
		}
	}
	paused = false
	savePause(t, s, &paused)
	loaded, err = LoadConfig(c.path)
	if nil != err || loaded.Nodes["home"].Paused || node.Token != loaded.Nodes["home"].Token {
		t.Fatal("resume not persisted", err)
	}
	if (nodeInput{}).node(NodeConfig{}).Paused || 0 != calls.Load() {
		t.Fatal("new node paused by default or settings contacted agent", calls.Load())
	}
}

func TestPausedRequests(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer up.Close()
	s := persistedServer(t, up)
	paused := true
	savePause(t, s, &paused)
	startChannels(t, s)
	for _, request := range []struct{ method, path, body string }{
		{"GET", "/api/nodes/home/health", ""},
		{"GET", "/api/nodes/home/stacks", ""},
		{"POST", "/api/nodes/home/stacks/demo/actions", `{"action":"deploy"}`},
		{"POST", "/api/nodes/home/images/check", `{}`},
		{"POST", "/api/nodes/home/stacks/demo/images/check", `{}`},
		{"POST", "/api/backup", `{"nodes":["home"],"password":"backup-password"}`},
		{"POST", "/api/settings/nodes/test", fmt.Sprintf(`{"id":"home","url":%q,"fingerprint":%q,"paused":false}`, up.URL, testFingerprint(up))},
		{"DELETE", "/api/settings/nodes/home", ""},
	} {
		w := perform(t, s, request.method, request.path, request.body)
		if http.StatusConflict != w.Code {
			t.Fatal(request.path, w.Code, w.Body.String())
		}
	}
	c, clients := s.snapshot()
	node := c.Nodes["home"]
	ctx := context.Background()
	if _, err := nodeRequest(ctx, node, clients["home"], "GET", "/info", nil); !errors.Is(err, errNodePaused) {
		t.Fatal("unguarded node request", err)
	}
	if _, err := checkNode(ctx, node, clients["home"]); !errors.Is(err, errNodePaused) {
		t.Fatal("unguarded health check", err)
	}
	if _, err := collectNodeBackup(ctx, node, clients["home"]); !errors.Is(err, errNodePaused) {
		t.Fatal("unguarded backup", err)
	}
	if err := resetNodePairing(ctx, node, clients["home"]); !errors.Is(err, errNodePaused) {
		t.Fatal("unguarded unpair", err)
	}
	if _, err := dialChannel(&nodeChannel{node: node}); !errors.Is(err, errNodePaused) {
		t.Fatal("unguarded channel dial", err)
	}
	request := httptest.NewRequest("GET", "https://registry.example/v2/", nil).WithContext(registryNodeContext(ctx, "home"))
	if _, err := (registryTransport{server: s}).RoundTrip(request); !errors.Is(err, errNodePaused) {
		t.Fatal("unguarded registry request", err)
	}
	if _, failure := s.attachNode(ctx, protocol.ChannelMessage{Type: "relay.attach", Node: "home", Resource: "update"}); nil == failure || 409 != failure.Status {
		t.Fatal("unguarded stream attachment", failure)
	}
	s.discoverRelays(ctx, "home")
	s.startRelay("home", "update")
	s.relaySelfUpdate(ctx, relayKey{"home", "update"})
	s.hubMu.Lock()
	channels, relays := len(s.channels), len(s.relays)
	s.hubMu.Unlock()
	if 0 != calls.Load() || 0 != channels || 0 != relays {
		t.Fatal("paused node performed outbound work", calls.Load(), channels, relays)
	}
}

func TestPauseChannel(t *testing.T) {
	f := fakeChannel(t)
	s := persistedServer(t, f.server)
	startChannels(t, s)
	nextConnection(t, f)
	awaitCache(t, s, `"stacks"`, "")
	watcher, _ := s.subscribeWorkspace()
	subscription, failure := s.attachNode(context.Background(), protocol.ChannelMessage{Type: "logs.attach", Node: "home", Resource: "demo"})
	if nil != failure {
		t.Fatal(failure)
	}
	shell, failure := s.attachNode(context.Background(), protocol.ChannelMessage{Type: "shell.attach", Node: "home", Shell: "terminal"})
	if nil != failure {
		t.Fatal(failure)
	}
	channel := subscription.channel
	paused := true
	savePause(t, s, &paused)
	if nil == channel.ctx.Err() || nil == channel.imageContext.Err() {
		t.Fatal("pause did not cancel active node work")
	}
	s.hubMu.Lock()
	if nil != s.channels["home"] || nil != channel.conn || 0 != len(channel.streams) || 0 != len(channel.shells) || nil != s.runtime["home"].snapshot {
		t.Fatal("pause retained channel or cached runtime")
	}
	for key := range s.images.stacks {
		if "home" == key.node {
			t.Fatal("pause retained node image work")
		}
	}
	s.hubMu.Unlock()
	select {
	case message := <-subscription.subscriber:
		if "error" != message.Type || 409 != message.Status {
			t.Fatal("active stream did not receive paused status", message)
		}
	case <-time.After(time.Second):
		t.Fatal("active stream was not closed")
	}
	if message := <-shell.subscriber; "shell.error" != message.Type || 409 != message.Status {
		t.Fatal("active shell did not receive paused status", message)
	}
	if failure := s.browserShellCommand(context.Background(), shell, protocol.ChannelMessage{Type: "shell.input", Stream: "terminal", Data: "ls"}); nil == failure || 409 != failure.Status {
		t.Fatal("paused shell accepted input", failure)
	}
	newWatcher, initial := s.subscribeWorkspace()
	if 1 != len(initial) || !strings.Contains(string(initial[0]), `"state":"paused"`) {
		t.Fatal("new subscriber missing paused state", initial)
	}
	found := false
	for 0 < len(watcher) {
		found = strings.Contains(string(<-watcher), `"state":"paused"`) || found
	}
	if !found {
		t.Fatal("existing subscriber missing paused state")
	}
	select {
	case <-f.connections:
		t.Fatal("paused node reconnected")
	case <-time.After(1200 * time.Millisecond):
	}
	if 1 != f.count.Load() {
		t.Fatal("paused node retried", f.count.Load())
	}
	paused = false
	savePause(t, s, &paused)
	nextConnection(t, f)
	awaitCache(t, s, `"stacks"`, "")
	s.hubMu.Lock()
	delete(s.watchers, watcher)
	delete(s.watchers, newWatcher)
	resumed := s.channels["home"]
	s.hubMu.Unlock()
	if nil == resumed || channel == resumed || nil != resumed.ctx.Err() || 2 != f.count.Load() {
		t.Fatal("resume did not restart the node channel")
	}
}
