package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func assertNodeOrder(t *testing.T, s *Server, want ...string) {
	t.Helper()
	for _, path := range []string{"/api/nodes", "/api/settings"} {
		w := perform(t, s, "GET", path, "")
		var response struct {
			Nodes []safeNode `json:"nodes"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || 200 != w.Code {
			t.Fatal(path, w.Code, w.Body.String(), err)
		}
		got := make([]string, 0, len(response.Nodes))
		for _, node := range response.Nodes {
			got = append(got, node.ID)
		}
		if !slices.Equal(want, got) {
			t.Fatal(path, "node order", got, "want", want)
		}
	}
}

func TestOrderedNodes(t *testing.T) {
	c := Config{Nodes: map[string]NodeConfig{
		"a": {Name: "Zulu"}, "b": {Name: "alpha"}, "c": {Name: "Alpha"}, "d": {Name: "Beta"},
	}}
	if got := orderedNodeIDs(c); !slices.Equal([]string{"b", "c", "d", "a"}, got) {
		t.Fatal("default name order", got)
	}
	c.Global.NodeOrder = []string{"a", "missing", "a", "d"}
	if got := orderedNodeIDs(c); !slices.Equal([]string{"a", "d", "b", "c"}, got) {
		t.Fatal("saved order and alphabetical remainder", got)
	}
	cloned := cloneConfig(c)
	cloned.Global.NodeOrder[0] = "b"
	if "a" != c.Global.NodeOrder[0] {
		t.Fatal("clone shares node order")
	}
}

func TestNodeOrder(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	s := persistedServer(t, up)
	w := perform(t, s, "POST", "/api/settings/nodes", fmt.Sprintf(`{"name":"Alpha","url":%q,"token":"new-token","fingerprint":%q}`, up.URL, testFingerprint(up)))
	var other safeNode
	if err := json.Unmarshal(w.Body.Bytes(), &other); nil != err || 200 != w.Code || "" == other.ID {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	assertNodeOrder(t, s, other.ID, "home")
	if 0 != len(s.config.Global.NodeOrder) {
		t.Fatal("adding a node created a manual order")
	}
	w = perform(t, s, "PUT", "/api/settings/node-order", fmt.Sprintf(`{"nodes":["home",%q]}`, other.ID))
	if 200 != w.Code || `{"status":"ok"}` != strings.TrimSpace(w.Body.String()) {
		t.Fatal(w.Code, w.Body.String())
	}
	loaded, err := LoadConfig(s.config.path)
	if nil != err || !slices.Equal([]string{"home", other.ID}, loaded.Global.NodeOrder) {
		t.Fatal("order not persisted", err)
	}
	s = New(loaded, nil)
	assertNodeOrder(t, s, "home", other.ID)
	w = perform(t, s, "PUT", "/api/settings/nodes/home", fmt.Sprintf(`{"name":"Zulu","url":%q,"fingerprint":%q}`, up.URL, testFingerprint(up)))
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	assertNodeOrder(t, s, "home", other.ID)
	w = perform(t, s, "POST", "/api/settings/nodes", fmt.Sprintf(`{"name":"0 Before","url":%q,"token":"new-token","fingerprint":%q}`, up.URL, testFingerprint(up)))
	var added safeNode
	if err := json.Unmarshal(w.Body.Bytes(), &added); nil != err || 200 != w.Code || "" == added.ID {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	assertNodeOrder(t, s, "home", other.ID, added.ID)
	before, _ := s.snapshot()
	w = perform(t, s, "DELETE", "/api/settings/nodes/"+other.ID, "")
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	if !slices.Equal([]string{"home", other.ID, added.ID}, before.Global.NodeOrder) {
		t.Fatal("delete mutated an existing snapshot")
	}
	loaded, err = LoadConfig(s.config.path)
	if nil != err || !slices.Equal([]string{"home", added.ID}, loaded.Global.NodeOrder) {
		t.Fatal("append/delete not persisted", err)
	}
	assertNodeOrder(t, New(loaded, nil), "home", added.ID)
}

func TestNodeOrderInvalid(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	s := persistedServer(t, up)
	c := cloneConfig(s.config)
	c.Nodes["other"] = c.Nodes["home"]
	c.Global.NodeOrder = []string{"home", "other"}
	s.applyConfig(c)
	if err := saveConfig(c); nil != err {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.path)
	if nil != err {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"nodes":["home","home"]}`, `{"nodes":["home","unknown"]}`, `{"nodes":["home"]}`,
		`{"nodes":["home","other","extra"]}`, `{}`, `null`, `{"nodes":null}`, `{"nodes":[]}`,
		`{"nodes":"home"}`, `{"nodes":[1,"other"]}`, `{"nodes":["home","other"],"extra":true}`,
		`{"nodes":["home","other"]} {}`, `{"nodes":[`,
	} {
		if w := perform(t, s, "PUT", "/api/settings/node-order", body); 400 != w.Code {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	after, err := os.ReadFile(c.path)
	if nil != err || !bytes.Equal(before, after) || !slices.Equal(c.Global.NodeOrder, s.config.Global.NodeOrder) {
		t.Fatal("invalid order changed config", err)
	}
	s.config.path = t.TempDir()
	w := perform(t, s, "PUT", "/api/settings/node-order", `{"nodes":["other","home"]}`)
	if 500 != w.Code || strings.Contains(w.Body.String(), s.config.path) || !slices.Equal(c.Global.NodeOrder, s.config.Global.NodeOrder) {
		t.Fatal("failed save changed order or exposed path", w.Code, w.Body.String())
	}
	assertNodeOrder(t, s, "home", "other")
}

func TestNodeOrderAuth(t *testing.T) {
	s := persistedServer(t, nil)
	r := httptest.NewRequest("PUT", "/api/settings/node-order", strings.NewReader(`{"nodes":[]}`))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 401 != w.Code {
		t.Fatal("unauthenticated order accepted", w.Code)
	}
	s.config.Auth.apiKey = strings.Repeat("ab", 32)
	r = httptest.NewRequest("PUT", "/api/settings/node-order", strings.NewReader(`{"nodes":[]}`))
	r.Header.Set("Authorization", "Bearer "+s.config.Auth.apiKey)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if http.StatusOK != w.Code {
		t.Fatal("bearer order rejected", w.Code, w.Body.String())
	}
}

func TestNodeOrderChannel(t *testing.T) {
	f := fakeChannel(t)
	s := persistedServer(t, f.server)
	startChannels(t, s)
	node := nextConnection(t, f)
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	browser := workspaceSnapshot(t, app.URL, s)
	browserSend(t, browser, protocol.ChannelMessage{Type: "logs.attach", Stream: "logs", Node: "home", Resource: "demo"})
	attach := channelCommand(t, f, "logs.attach")
	_, clients := s.snapshot()
	s.hubMu.Lock()
	channel, connection, runtime := s.channels["home"], s.channels["home"].conn, s.runtime["home"]
	s.hubMu.Unlock()
	frames, _ := s.subscribeWorkspace()
	t.Cleanup(func() {
		s.hubMu.Lock()
		delete(s.watchers, frames)
		s.hubMu.Unlock()
	})
	w := perform(t, s, "PUT", "/api/settings/node-order", `{"nodes":["home"]}`)
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case frame := <-frames:
		var message protocol.ChannelMessage
		if err := json.Unmarshal(frame, &message); nil != err || "nodes.changed" != message.Event {
			t.Fatal("missing nodes.changed", string(frame), err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing nodes.changed")
	}
	_, currentClients := s.snapshot()
	s.hubMu.Lock()
	unchanged := channel == s.channels["home"] && connection == s.channels["home"].conn && runtime == s.runtime["home"]
	s.hubMu.Unlock()
	if !unchanged || clients["home"] != currentClients["home"] || 1 != f.count.Load() {
		t.Fatal("order changed node connections or runtime")
	}
	browserSend(t, node, protocol.ChannelMessage{Type: "stream.event", Stream: attach.Stream, Event: "message", Payload: json.RawMessage(`{"message":"still attached"}`)})
	if got := resourceMessage(t, browser); "logs" != got.Stream || !strings.Contains(string(got.Payload), "still attached") {
		t.Fatal("order detached logs", got)
	}
	// An unchanged order does not write the config or emit another change.
	s.configMu.Lock()
	s.config.path = t.TempDir()
	s.configMu.Unlock()
	w = perform(t, s, "PUT", "/api/settings/node-order", `{"nodes":["home"]}`)
	if 200 != w.Code || 0 != len(frames) {
		t.Fatal("unchanged order was reapplied", w.Code, w.Body.String())
	}
}
