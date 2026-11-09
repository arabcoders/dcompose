package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/arabcoders/dcompose/internal/engine/registry"
	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/crypto/bcrypt"
)

func testFingerprint(upstream *httptest.Server) string {
	sum := sha256.Sum256(upstream.Certificate().RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func testServer(t *testing.T, upstream *httptest.Server) *Server {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if nil != err {
		t.Fatal(err)
	}
	c := Config{Auth: AuthConfig{Username: "admin", Password: string(hash), passwordHash: string(hash), SessionKey: strings.Repeat("ab", 32), sessionKey: strings.Repeat("\xab", 32)}, Nodes: map[string]NodeConfig{}}
	if nil != upstream {
		c.Nodes["home"] = NodeConfig{Name: "Home", URL: upstream.URL, Token: "agent-test-token", token: "agent-test-token", PublicHost: "public.example", Fingerprint: testFingerprint(upstream)}
	}
	s := New(c, fstest.MapFS{"index.html": {Data: []byte("<html>SPA</html>")}, "assets/app.js": {Data: []byte("script")}})
	s.registryClient = registry.NewClient(&http.Client{Transport: registryTestTransport(func(r *http.Request) (*http.Response, error) {
		t.Error("unexpected registry HTTP request", r.Method, r.URL.Host, r.URL.Path)
		return nil, errors.New("registry HTTP fixture was not configured")
	})})
	return s
}

func testSession(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	c, _ := s.snapshot()
	token, err := sessionToken(c.Auth, time.Now().Add(time.Hour))
	if nil != err {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "session", Value: token}
}

func perform(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(testSession(t, s))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func testHealth(w http.ResponseWriter, r *http.Request) bool {
	if "/health" != r.URL.Path {
		return false
	}
	_, _ = fmt.Fprintf(w, `{"status":"ok","version":"test","api_version":%d}`, protocol.AgentAPI)
	return true
}

func TestAuth(t *testing.T) {
	s := testServer(t, nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/nodes", nil))
	if 401 != w.Code || "Session" != w.Header().Get("WWW-Authenticate") {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"admin","password":"test-password"}`))
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 200 != w.Code {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if 1 != len(cookies) || !cookies[0].HttpOnly || http.SameSiteStrictMode != cookies[0].SameSite {
		t.Fatal("missing protected session cookie")
	}
	r = httptest.NewRequest("GET", "/api/auth/user", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 200 != w.Code || `{"account":{"username":"admin"},"authenticated":true}` != strings.TrimSpace(w.Body.String()) {
		t.Fatal(w.Body.String())
	}
	r = httptest.NewRequest("POST", "/api/auth/logout", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 200 != w.Code || 1 != len(w.Result().Cookies()) || 0 <= w.Result().Cookies()[0].MaxAge {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("GET", "/api/auth/user", nil)
	r.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 200 != w.Code {
		t.Fatal("logout revoked a copied stateless session")
	}
}

func TestAuthFailure(t *testing.T) {
	s := testServer(t, nil)
	w := perform(t, s, "POST", "/api/auth/login", `{"username":"admin","password":"wrong"}`)
	if 401 != w.Code || strings.Contains(w.Body.String(), "test-password") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = perform(t, s, "POST", "/api/auth/login", `{"username":"admin","password":"wrong"}`)
	if 429 != w.Code {
		t.Fatal("login attempts not paced")
	}
}

func TestOrigin(t *testing.T) {
	s := testServer(t, nil)
	for _, path := range []string{"/api/auth/login", "/api/auth/logout", "/api/nodes/home/stacks/demo/actions"} {
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set("Origin", "https://attacker.example")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if 403 != w.Code {
			t.Fatal(path, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://dcompose.example/api/auth/logout", nil)
	r.Header.Set("Origin", "https://dcompose.example")
	r.AddCookie(testSession(t, s))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 200 != w.Code {
		t.Fatal(w.Code)
	}
}

func TestNodes(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	s := testServer(t, up)
	node := s.config.Nodes["home"]
	node.URL = "https://private.example:9077"
	s.config.Nodes["home"] = node
	w := perform(t, s, "GET", "/api/nodes", "")
	if 200 != w.Code {
		t.Fatal(w.Code)
	}
	for _, secret := range []string{"agent-test-token", "private.example", "fingerprint", "token_file"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if `{"nodes":[{"id":"home","name":"Home","public_host":"public.example"}]}` != strings.TrimSpace(w.Body.String()) {
		t.Fatal("unexpected node contract", w.Body.String())
	}
}

func TestForward(t *testing.T) {
	var requests atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if "Bearer agent-test-token" != r.Header.Get("Authorization") || strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) || "" != r.Header.Get("Cookie") {
			t.Error("incorrect upstream credentials")
		}
		if "/stacks/demo/actions" != r.URL.Path || "POST" != r.Method || "" != r.URL.RawQuery {
			t.Error(r.Method, r.URL.String())
		}
		data, _ := io.ReadAll(r.Body)
		if `{"action":"deploy"}` != string(data) {
			t.Errorf("body %s", data)
		}
		w.Header().Set("Set-Cookie", "evil=1")
		w.WriteHeader(202)
		_, _ = io.WriteString(w, `{"status":"accepted"}`)
	}))
	defer up.Close()
	s := testServer(t, up)
	assumeRegistriesApplied(s)
	w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/actions?token=browser-secret", `{"action":"deploy"}`)
	if 1 != requests.Load() || 202 != w.Code || "" != w.Header().Get("Set-Cookie") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestFiltering(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"name":"demo","status":"running","services":[],"compose":"secret","env":"secret"}`)
	}))
	defer up.Close()
	s := testServer(t, up)
	for _, path := range []string{"/stacks/demo/status"} {
		w := perform(t, s, "GET", "/api/nodes/home"+path, "")
		if 200 != w.Code || strings.Contains(w.Body.String(), "secret") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestNodeInfo(t *testing.T) {
	info := readFixture(t, "node-info.json")
	var requests atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if "Bearer agent-test-token" != r.Header.Get("Authorization") || strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) || "" != r.Header.Get("Cookie") {
			t.Error("incorrect upstream credentials or API version")
		}
		requests.Add(1)
		data, _ := io.ReadAll(r.Body)
		if "/info" != r.URL.Path || http.MethodGet != r.Method || "" != r.URL.RawQuery || 0 != len(data) {
			t.Errorf("info request: %s %s %s", r.Method, r.URL, data)
		}
		_, _ = io.WriteString(w, info)
	}))
	defer up.Close()
	s := testServer(t, up)
	w := perform(t, s, http.MethodGet, "/api/nodes/home/info?command=destroy&token=browser-secret", "untrusted")
	if 200 != w.Code || 1 != requests.Load() || !strings.Contains(w.Body.String(), `"runtime":`) || strings.Contains(w.Body.String(), `"docker":`) || !strings.Contains(w.Body.String(), `"cpu":"2.50%"`) || !strings.Contains(w.Body.String(), `"memory_bytes":16777216000`) {
		t.Fatalf("info: %d %s", w.Code, w.Body)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w := perform(t, s, method, "/api/nodes/home/info", `{"command":"destroy"}`)
		if 404 != w.Code || 1 != requests.Load() {
			t.Fatalf("info method: %s %d %s", method, w.Code, w.Body)
		}
	}
	for _, path := range []string{"/api/nodes/home/info/extra", "/api/nodes/home/info;destroy", "/api/nodes/unknown/info"} {
		if w := perform(t, s, http.MethodGet, path, ""); 404 != w.Code || 1 != requests.Load() {
			t.Fatalf("info path: %s %d %s", path, w.Code, w.Body)
		}
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/home/info", nil))
	if 401 != w.Code || 1 != requests.Load() {
		t.Fatalf("unauthenticated info: %d %s", w.Code, w.Body)
	}
}

func TestNodeInfoError(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":"secret Docker stderr"}`)
	}))
	defer up.Close()
	s := testServer(t, up)
	w := perform(t, s, http.MethodGet, "/api/nodes/home/info", "")
	var response struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || 502 != w.Code || 502 != response.Error.Code || "" == response.Error.Message || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("info error: %d %s", w.Code, w.Body)
	}
}

func TestInvalidPaths(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	s := testServer(t, up)
	for _, path := range []string{"/api/nodes/home/stacks/../health", "/api/nodes/home/stacks/UPPER", "/api/nodes/home/stacks/demo/logs/extra", "/api/nodes/home/anything", "/api/nodes/unknown/health", "/api/nodes/home/stacks/demo%2fhealth"} {
		w := perform(t, s, "GET", path, "")
		if 404 != w.Code {
			t.Fatal(path, w.Code)
		}
	}
	for _, body := range []string{`{"action":"destroy"}`, `{"action":"deploy","service":"db"}`, `{"action":"deploy","extra":true}`, `{"action":1}`, `{"action":"deploy"} {}`} {
		w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/actions", body)
		if 400 != w.Code {
			t.Fatal(body, w.Code)
		}
	}
}

func TestBinaryRemoved(t *testing.T) {
	var requests atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		testHealth(w, r)
	}))
	defer up.Close()
	s := testServer(t, up)
	if w := perform(t, s, "PUT", "/api/nodes/home/binary", "untrusted"); 404 != w.Code {
		t.Fatal(w.Code, w.Body)
	}
	if 0 != requests.Load() {
		t.Fatal("obsolete route contacted agent")
	}
}

func TestPin(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { testHealth(w, r) }))
	defer up.Close()
	s := testServer(t, up)
	node := s.config.Nodes["home"]
	if 200 != perform(t, s, "GET", "/api/nodes/home/health", "").Code {
		t.Fatal("valid SPKI pin rejected")
	}
	node.Fingerprint = base64.StdEncoding.EncodeToString(make([]byte, 32))
	s.nodes["home"] = nodeClient(node)
	w := perform(t, s, "GET", "/api/nodes/home/health", "")
	if 502 != w.Code || strings.Contains(w.Body.String(), "agent-test-token") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRedirect(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer target.Close()
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/leak", http.StatusFound)
	}))
	defer up.Close()
	s := testServer(t, up)
	if 502 != perform(t, s, "GET", "/api/nodes/home/health", "").Code {
		t.Fatal("redirect accepted")
	}
	if 0 != requests.Load() {
		t.Fatal("redirect target received a request")
	}
}

func TestStatic(t *testing.T) {
	s := testServer(t, nil)
	for _, path := range []string{"/", "/stacks", "/stacks/home/demo"} {
		w := perform(t, s, "GET", path, "")
		if 200 != w.Code || "<html>SPA</html>" != w.Body.String() {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/missing", "/api", "/assets/missing.js"} {
		w := perform(t, s, "GET", path, "")
		if 404 != w.Code || strings.Contains(w.Body.String(), "SPA") {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	if "script" != perform(t, s, "GET", "/assets/app.js", "").Body.String() {
		t.Fatal("asset missing")
	}
}

func TestAggregate(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if http.MethodGet == r.Method && "/relays" == r.URL.Path {
			jsonResponse(w, http.StatusOK, map[string]any{"relays": []any{}})
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if nil != err {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if !ackRegistrySync(t, r.Context(), conn) {
			return
		}
		_ = wsjson.Write(r.Context(), conn, protocol.ChannelMessage{Type: "event", Event: "node.snapshot", Payload: json.RawMessage(`{"node":"forged","health":{"status":"ok"},"stacks":[]}`)})
		_, _, _ = conn.Read(r.Context())
	}))
	defer up.Close()
	app := testServer(t, up)
	node := NodeConfig{URL: "https://127.0.0.1:1", token: "unavailable-token", Fingerprint: testFingerprint(up)}
	app.config.Nodes["offline"] = node
	app.nodes["offline"] = nodeClient(node)
	app.Start(context.Background())
	defer app.Close()
	s := httptest.NewServer(app)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn := browserStream(t, s.URL+"/api/events", app)
	seenSnapshot, seenOffline := false, false
	for {
		_, data, err := conn.Read(ctx)
		if nil != err {
			t.Fatal("missing multiplexed events", err)
		}
		line := string(data)
		if strings.Contains(line, "forged") || strings.Contains(line, "unavailable-token") {
			t.Fatal("untrusted identity/secret leaked")
		}
		if strings.Contains(line, `"health"`) && strings.Contains(line, `"node":"home"`) {
			seenSnapshot = true
		}
		if strings.Contains(line, `"state":"unavailable"`) && strings.Contains(line, `"node":"offline"`) {
			seenOffline = true
		}
		if seenSnapshot && seenOffline {
			return
		}
	}
}

func TestFrames(t *testing.T) {
	for _, input := range []struct{ event, data string }{{"unknown", "{}"}, {"node.snapshot", "[]"}, {"stack.runtime", `{"name":3}`}, {"node.snapshot", "invalid"}, {"node.state", `{"state":"bad"}`}} {
		if _, err := runtimePayload(input.event, []byte(input.data)); nil == err {
			t.Fatal("accepted invalid frame", input.event)
		}
	}
	payload, err := runtimePayload("stack.runtime", []byte(`{"name":"demo","status":"error","services":[],"error":"secret"}`))
	if nil != err || strings.Contains(string(payload["error"]), "secret") {
		t.Fatal(payload, err)
	}
}

func TestReconnect(t *testing.T) {
	var attempts atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if http.MethodGet == r.Method && "/relays" == r.URL.Path {
			jsonResponse(w, http.StatusOK, map[string]any{"relays": []any{}})
			return
		}
		attempt := attempts.Add(1)
		if 1 == attempt {
			w.WriteHeader(503)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if nil != err {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		if !ackRegistrySync(t, r.Context(), conn) {
			return
		}
		_ = wsjson.Write(r.Context(), conn, protocol.ChannelMessage{Type: "event", Event: "node.snapshot", Payload: json.RawMessage(`{"health":{"status":"ok"},"stacks":[]}`)})
		_, _, _ = conn.Read(r.Context())
	}))
	defer up.Close()
	app := testServer(t, up)
	app.Start(context.Background())
	defer app.Close()
	s := httptest.NewServer(app)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn := browserStream(t, s.URL+"/api/events", app)
	unavailable := false
	for {
		_, data, err := conn.Read(ctx)
		if nil != err {
			t.Fatal("node did not reconnect", err)
		}
		if strings.Contains(string(data), `"state":"unavailable"`) {
			unavailable = true
		}
		if strings.Contains(string(data), `"health"`) {
			if !unavailable || 2 > attempts.Load() {
				t.Fatal("missing reconnect state")
			}
			return
		}
	}
}

func TestResponses(t *testing.T) {
	cases := []struct {
		name, path, body string
		status, want     int
	}{
		{"invalid", "/health", "not-json", 200, 502},
		{"trailing", "/health", `{} {}`, 200, 502},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer up.Close()
			s := testServer(t, up)
			w := perform(t, s, "GET", "/api/nodes/home"+test.path, "")
			if test.want != w.Code {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestConfig(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("test-agent-token\n"), 0600); nil != err {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	config := fmt.Sprintf("[global]\nlisten = '127.0.0.1:8080'\n[auth]\nusername = 'admin'\npassword = '%s'\n[nodes.home]\nurl = '%s'\nfingerprint = '%s'\ntoken = 'file:token'\n", hash, up.URL, testFingerprint(up))
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(config), 0600); nil != err {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if nil != err || "test-agent-token" != c.Nodes["home"].token || "127.0.0.1" != c.Nodes["home"].PublicHost {
		t.Fatal(c.Global.Listen, err)
	}
	data, _ := json.Marshal(c.Nodes["home"])
	if strings.Contains(string(data), "test-agent-token") {
		t.Fatal("token serializable")
	}
	for _, invalid := range []string{strings.Replace(config, "https://", "http://", 1), strings.Replace(config, "token = 'file:token'", "unknown = true", 1), strings.Replace(config, testFingerprint(up), "", 1)} {
		if err := os.WriteFile(path, []byte(invalid), 0600); nil != err {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); nil == err {
			t.Fatal("invalid config accepted")
		}
	}
}
