package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// HTTP-only proxy tests model a completed handshake without opening a socket.
func assumeRegistriesApplied(s *Server) {
	s.channels["home"] = &nodeChannel{
		ctx:              context.Background(),
		imageContext:     context.Background(),
		conn:             new(websocket.Conn),
		registryRevision: s.registrySync.Revision,
		registryApplied:  s.registrySync.Revision,
	}
	s.runtime["home"] = &nodeRuntime{state: map[string]json.RawMessage{"state": json.RawMessage(`"connected"`)}}
}

func ackRegistrySync(t *testing.T, ctx context.Context, conn *websocket.Conn) bool {
	t.Helper()
	var message protocol.ChannelMessage
	if nil != wsjson.Read(ctx, conn, &message) {
		return false
	}
	var snapshot protocol.RegistrySync
	if "registry.sync" != message.Type || "" != message.Node || nil != json.Unmarshal(message.Payload, &snapshot) ||
		"" == snapshot.Revision ||
		nil == snapshot.Registries {
		t.Error("missing initial registry snapshot")
		return false
	}
	payload, _ := json.Marshal(protocol.RegistryApplied{Revision: snapshot.Revision})
	return nil == wsjson.Write(ctx, conn, protocol.ChannelMessage{Type: "registry.applied", Payload: payload})
}

type registryDelivery struct {
	conn     *websocket.Conn
	snapshot protocol.RegistrySync
}

type registryChannelFixture struct {
	server   *httptest.Server
	syncs    chan registryDelivery
	requests atomic.Int32
}

func newRegistryChannel(t *testing.T) *registryChannelFixture {
	t.Helper()
	f := &registryChannelFixture{syncs: make(chan registryDelivery, 16)}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if "Bearer agent-test-token" != r.Header.Get("Authorization") || strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) {
			w.WriteHeader(401)
			return
		}
		if "/channel" != r.URL.Path {
			if http.MethodGet == r.Method && "/relays" == r.URL.Path {
				jsonResponse(w, http.StatusOK, map[string]any{"relays": []any{}})
				return
			}
			f.requests.Add(1)
			jsonResponse(w, http.StatusAccepted, map[string]string{"status": "accepted"})
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if nil != err {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		conn.SetReadLimit(2 << 20)
		// An eager runtime snapshot must not make the browser ready before apply.
		if nil != wsjson.Write(
			r.Context(),
			conn,
			protocol.ChannelMessage{
				Type:    "event",
				Event:   "node.snapshot",
				Payload: json.RawMessage(`{"health":{"status":"ok"},"stacks":[]}`),
			},
		) {
			return
		}
		for {
			var message protocol.ChannelMessage
			if nil != wsjson.Read(r.Context(), conn, &message) {
				return
			}
			var snapshot protocol.RegistrySync
			if "registry.sync" != message.Type || "" != message.Node || nil != json.Unmarshal(message.Payload, &snapshot) {
				t.Error("unexpected registry channel command")
				return
			}
			f.syncs <- registryDelivery{conn: conn, snapshot: snapshot}
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func nextRegistry(t *testing.T, f *registryChannelFixture) registryDelivery {
	t.Helper()
	select {
	case delivery := <-f.syncs:
		return delivery
	case <-time.After(4 * time.Second):
		t.Fatal("missing registry synchronization")
		return registryDelivery{}
	}
}

func applyRegistry(t *testing.T, delivery registryDelivery, revision string) {
	t.Helper()
	payload, _ := json.Marshal(protocol.RegistryApplied{Revision: revision})
	browserSend(t, delivery.conn, protocol.ChannelMessage{Type: "registry.applied", Payload: payload})
}

func TestRegistryBarrier(t *testing.T) {
	f := newRegistryChannel(t)
	s := persistedServer(t, f.server)
	startChannels(t, s)
	first := nextRegistry(t, f)
	if nil == first.snapshot.Registries || 0 != len(first.snapshot.Registries) {
		t.Fatal("new channel did not replace credentials with an empty list")
	}
	applyRegistry(t, first, "stale-revision")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if nil == s.awaitRegistries(ctx, "home") {
		t.Fatal("stale acknowledgement passed the barrier")
	}
	frames, initial := s.subscribeWorkspace()
	s.hubMu.Lock()
	delete(s.watchers, frames)
	s.hubMu.Unlock()
	for _, frame := range initial {
		if strings.Contains(string(frame), `"node.snapshot"`) || strings.Contains(string(frame), `"state":"connected"`) ||
			strings.Contains(string(frame), "registry.sync") {
			t.Fatal("browser became ready before registry apply")
		}
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- perform(t, s, "POST", "/api/nodes/home/stacks/demo/actions", `{"action":"update","service":"web"}`)
	}()
	select {
	case <-done:
		t.Fatal("action completed before credentials were applied")
	case <-time.After(30 * time.Millisecond):
	}
	if 0 != f.requests.Load() {
		t.Fatal("action reached node before credential apply")
	}
	applyRegistry(t, first, first.snapshot.Revision)
	select {
	case response := <-done:
		if 202 != response.Code || 1 != f.requests.Load() {
			t.Fatal("applied credentials did not release action", response.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("socket reader deadlocked waiting for acknowledgement")
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if nil != s.awaitRegistries(ctx, "home") {
		t.Fatal("current acknowledgement did not pass barrier")
	}
}

func TestRegistryResync(t *testing.T) {
	f := newRegistryChannel(t)
	s := persistedServer(t, f.server)
	startChannels(t, s)
	first := nextRegistry(t, f)
	applyRegistry(t, first, first.snapshot.Revision)
	registryRows(
		t,
		perform(
			t,
			s,
			"PUT",
			"/api/settings/registries",
			`{"registries":[{"registry":"https://index.docker.io/v1/","label":"Checks","auth":{"username":"alice","password":"sync-secret"},"background_checks":true},{"registry":"https://index.docker.io/v1/refresh-token","auth":{"username":"<token>","password":"sync-refresh:client-id"},"background_checks":false}]}`,
		),
	)
	changed := nextRegistry(t, f)
	if first.snapshot.Revision == changed.snapshot.Revision || 2 != len(changed.snapshot.Registries) ||
		"sync-secret" != changed.snapshot.Registries[0].Auth["password"] ||
		"Checks" != changed.snapshot.Registries[0].Label {
		t.Fatal("edit did not publish full effective credentials")
	}
	if "https://index.docker.io/v1/refresh-token" != changed.snapshot.Registries[1].Registry ||
		"sync-refresh:client-id" != changed.snapshot.Registries[1].Auth["password"] {
		t.Fatal("registry sync omitted an independent OAuth auth record")
	}
	loaded, err := LoadConfig(s.config.path)
	if nil != err || "sync-secret" != loaded.Registries[0].auth["password"] {
		t.Fatal("snapshot published before source persisted", err)
	}
	applyRegistry(t, changed, changed.snapshot.Revision)
	_ = changed.conn.CloseNow()
	reconnected := nextRegistry(t, f)
	if changed.snapshot.Revision != reconnected.snapshot.Revision || 2 != len(reconnected.snapshot.Registries) {
		t.Fatal("reconnect did not resend latest credentials")
	}
	registryRows(t, perform(t, s, "PUT", "/api/settings/registries", `{"registries":[]}`))
	deleted := nextRegistry(t, f)
	if 0 != len(deleted.snapshot.Registries) || nil == deleted.snapshot.Registries || first.snapshot.Revision != deleted.snapshot.Revision {
		t.Fatal("deletion did not publish empty replacement")
	}
	applyRegistry(t, deleted, reconnected.snapshot.Revision)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if nil == s.awaitRegistries(ctx, "home") {
		t.Fatal("prior credentials passed deletion barrier")
	}
	applyRegistry(t, deleted, deleted.snapshot.Revision)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if nil != s.awaitRegistries(ctx, "home") {
		t.Fatal("empty replacement did not pass barrier")
	}
	s.hubMu.Lock()
	frames := s.workspaceFramesLocked()
	s.hubMu.Unlock()
	for _, frame := range frames {
		if strings.Contains(string(frame), "sync-secret") || strings.Contains(string(frame), "sync-refresh") ||
			strings.Contains(string(frame), "registry.sync") ||
			strings.Contains(string(frame), "registry.applied") {
			t.Fatal("workspace leaked registry channel data")
		}
	}
}

func TestRegistrySyncFailure(t *testing.T) {
	f := newRegistryChannel(t)
	s := persistedServer(t, f.server)
	for _, path := range []string{"/api/nodes/home/images/check", "/api/nodes/home/stacks/demo/images/check", "/api/nodes/home/stacks/demo/actions"} {
		body := `{}`
		if strings.HasSuffix(path, "/actions") {
			body = `{"action":"pull"}`
		}
		if 503 != perform(t, s, "POST", path, body).Code || 0 != f.requests.Load() {
			t.Fatal("offline channel forwarded an action")
		}
	}
	startChannels(t, s)
	first := nextRegistry(t, f)
	frames, _ := s.subscribeWorkspace()
	defer func() {
		s.hubMu.Lock()
		delete(s.watchers, frames)
		s.hubMu.Unlock()
	}()
	browserSend(t, first.conn, protocol.ChannelMessage{Type: "registry.error", Status: 500, Error: "secret-error-sentinel"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	failed := false
	for !failed {
		select {
		case frame := <-frames:
			if strings.Contains(string(frame), "secret-error-sentinel") {
				t.Fatal("peer registry error leaked to browser")
			}
			s.hubMu.Lock()
			failed = s.channels["home"].registryFailed
			s.hubMu.Unlock()
		case <-ctx.Done():
			t.Fatal("missing registry failure state")
		}
	}
	w := perform(t, s, "POST", "/api/nodes/home/images/check", `{}`)
	if 503 != w.Code || 0 != f.requests.Load() || strings.Contains(w.Body.String(), "secret-error-sentinel") {
		t.Fatal("failed synchronization allowed a check or leaked peer details")
	}
}

func TestRegistryActions(t *testing.T) {
	for _, state := range []string{"offline", "failed"} {
		for _, action := range []string{"restart", "stop", "down", "start", "deploy", "pull", "update"} {
			t.Run(state+"/"+action, func(t *testing.T) {
				f := newRegistryChannel(t)
				s := persistedServer(t, f.server)
				if "failed" == state {
					s.channels["home"] = &nodeChannel{registryFailed: true}
				}
				body := `{"action":"` + action + `"}`
				if "restart" == action || "stop" == action {
					body = `{"action":"` + action + `","service":"web"}`
				}
				response := perform(t, s, "POST", "/api/nodes/home/stacks/demo/actions", body)
				if "restart" == action || "stop" == action || "down" == action {
					if 202 != response.Code || 1 != f.requests.Load() {
						t.Fatalf("local action blocked by registry sync: %d %s", response.Code, response.Body.String())
					}
				} else if 503 != response.Code || 0 != f.requests.Load() {
					t.Fatalf("image action bypassed registry sync: %d %s", response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestRegistryTimeout(t *testing.T) {
	f := newRegistryChannel(t)
	s := persistedServer(t, f.server)
	startChannels(t, s)
	_ = nextRegistry(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("POST", "/api/nodes/home/images/check", strings.NewReader(`{}`)).WithContext(ctx)
	r.AddCookie(testSession(t, s))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 503 != w.Code || 0 != f.requests.Load() {
		t.Fatal("unacknowledged synchronization did not stop a timed-out check")
	}
}

func TestRegistryBrowserBoundary(t *testing.T) {
	f := newWorkspace(t)
	browser := browserStream(t, f.app.URL+"/api/events", f.server)
	browserSend(
		t,
		browser,
		protocol.ChannelMessage{Type: "registry.sync", Node: "home", Payload: json.RawMessage(`{"revision":"forged","registries":[]}`)},
	)
	message := resourceMessage(t, browser)
	if "error" != message.Type || 400 != message.Status {
		t.Fatal("browser registry command was not rejected")
	}
	select {
	case <-f.channel.commands:
		t.Fatal("browser registry command reached the node")
	default:
	}
}
