package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/protocol"
	"golang.org/x/crypto/bcrypt"
)

func persistedServer(t *testing.T, upstream *httptest.Server) *Server {
	t.Helper()
	s := testServer(t, upstream)
	s.config.path = filepath.Join(t.TempDir(), "dcompose.toml")
	s.config.Global.Listen = "127.0.0.1:8080"
	if err := saveConfig(s.config); nil != err {
		t.Fatal(err)
	}
	return s
}

func TestConfigSession(t *testing.T) {
	for _, test := range []struct {
		name      string
		reference string
		file      string
	}{
		{name: "generated"},
		{name: "recovered", file: "secrets/session-key"},
		{name: "reference", reference: "file:custom-key", file: "custom-key"},
		{name: "inline", reference: strings.Repeat("ab", 32)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "dcompose.toml")
			if "" != test.file {
				if err := configutil.Write(filepath.Join(dir, test.file), []byte(strings.Repeat("ab", 32)+"\n")); nil != err {
					t.Fatal(err)
				}
			}
			c := Config{Auth: AuthConfig{SessionKey: test.reference}, path: path}
			if err := saveConfig(c); nil != err {
				t.Fatal(err)
			}
			c, err := LoadConfig(path)
			if nil != err || 32 != len(c.Auth.sessionKey) {
				t.Fatal("session key not resolved", err)
			}
			reference := test.reference
			if "" == reference {
				reference = "file:secrets/session-key"
			}
			if reference != c.Auth.SessionKey {
				t.Fatal("session reference changed")
			}
			if "generated" != test.name && strings.Repeat("\xab", 32) != c.Auth.sessionKey {
				t.Fatal("existing session key replaced")
			}
			loaded, err := LoadConfig(path)
			if nil != err || c.Auth.SessionKey != loaded.Auth.SessionKey || c.Auth.sessionKey != loaded.Auth.sessionKey {
				t.Fatal("session key changed on reload", err)
			}
		})
	}
}

func TestSettingsRefs(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	s := persistedServer(t, up)
	c := s.config
	if err := configutil.Write(filepath.Join(filepath.Dir(c.path), "password"), []byte(c.Auth.passwordHash+"\n")); nil != err {
		t.Fatal(err)
	}
	if err := configutil.Write(filepath.Join(filepath.Dir(c.path), "token"), []byte("agent-test-token\n")); nil != err {
		t.Fatal(err)
	}
	c.Auth.Password = "file:password"
	node := c.Nodes["home"]
	node.Token = "file:token"
	c.Nodes["home"] = node
	if err := saveConfig(c); nil != err {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(c.path)
	if nil != err {
		t.Fatal(err)
	}
	s = New(loaded, nil, "v2.3")
	w := perform(t, s, "PUT", "/api/settings/nodes/home", fmt.Sprintf(`{"name":"Home","url":%q,"fingerprint":%q}`, up.URL, testFingerprint(up)))
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	w = perform(t, s, "GET", "/api/settings", "")
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{c.Auth.passwordHash, "file:password", "file:token"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("settings leaked secret")
		}
	}
	for _, field := range []string{`"api_version":1`, fmt.Sprintf(`"agent_api_version":%d`, protocol.AgentAPI), `"version":"v2.3"`, `"has_token":true`, `"token":"agent-test-token"`} {
		if !strings.Contains(w.Body.String(), field) {
			t.Fatal(w.Body.String())
		}
	}
	loaded, err = LoadConfig(c.path)
	if nil != err || "file:password" != loaded.Auth.Password || "file:token" != loaded.Nodes["home"].Token {
		t.Fatal(loaded, err)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/version", nil))
	if 200 != w.Code || !strings.Contains(w.Body.String(), `"version":"v2.3"`) {
		t.Fatal(w.Body.String())
	}
}

func TestNodeCRUD(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	s := persistedServer(t, nil)
	body := fmt.Sprintf(`{"name":"Home","url":%q,"token":"new-secret-token","fingerprint":%q}`, up.URL, testFingerprint(up))
	w := perform(t, s, "POST", "/api/settings/nodes", body)
	if 200 != w.Code || !strings.Contains(w.Body.String(), `"token":"new-secret-token"`) || !strings.Contains(w.Body.String(), `"has_token":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	var created safeNode
	if err := json.Unmarshal(w.Body.Bytes(), &created); nil != err || "" == created.ID {
		t.Fatal("missing generated identity", err)
	}
	if 400 != perform(t, s, "POST", "/api/settings/nodes", strings.Replace(body, `{"name":`, `{"id":"chosen","name":`, 1)).Code {
		t.Fatal("user-chosen identity accepted")
	}
	loaded, err := LoadConfig(s.config.path)
	if nil != err {
		t.Fatal(err)
	}
	node := loaded.Nodes[created.ID]
	if "new-secret-token" != node.token || !strings.HasPrefix(node.Token, "file:secrets/") {
		t.Fatal("token not persisted privately")
	}
	stat, err := os.Stat(configutil.Path(loaded.path, strings.TrimPrefix(node.Token, "file:")))
	if nil != err || 0600 != stat.Mode().Perm() {
		t.Fatal("token permissions", err)
	}
	oldConfig, oldClients := s.snapshot()
	oldClient := oldClients[created.ID]
	w = perform(t, s, "PUT", "/api/settings/nodes/"+created.ID, fmt.Sprintf(`{"name":"Updated","url":"https://127.0.0.1:9088","token":"","fingerprint":%q}`, testFingerprint(up)))
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	loaded, err = LoadConfig(s.config.path)
	if nil != err || node.Token != loaded.Nodes[created.ID].Token || "new-secret-token" != loaded.Nodes[created.ID].token {
		t.Fatal("blank token did not retain secret", err)
	}
	c, clients := s.snapshot()
	if oldClient == clients[created.ID] || "Home" != oldConfig.Nodes[created.ID].Name || "Updated" != c.Nodes[created.ID].Name {
		t.Fatal("snapshot/client not replaced")
	}
	if 200 != perform(t, s, "DELETE", "/api/settings/nodes/"+created.ID, "").Code {
		t.Fatal("delete failed")
	}
	loaded, err = LoadConfig(s.config.path)
	if nil != err || 0 != len(loaded.Nodes) {
		t.Fatal("delete not persisted", err)
	}
	_, clients = s.snapshot()
	if 0 != len(clients) {
		t.Fatal("deleted client retained")
	}
	if 404 != perform(t, s, "DELETE", "/api/settings/nodes/"+created.ID, "").Code {
		t.Fatal("missing delete accepted")
	}
}

func TestDisconnectNode(t *testing.T) {
	for _, mode := range []string{"reset", "offline", "rejected", "unsupported", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if http.MethodPost != r.Method || "/unpair" != r.URL.Path || "Bearer agent-test-token" != r.Header.Get("Authorization") || strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) {
					t.Error("incorrect unpair request")
				}
				switch mode {
				case "rejected":
					jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": "private reset failure"})
				case "unsupported":
					w.WriteHeader(http.StatusNotFound)
				case "invalid":
					jsonResponse(w, http.StatusOK, map[string]string{"status": "not-reset"})
				default:
					jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
				}
			}))
			defer up.Close()
			s := persistedServer(t, up)
			if "offline" == mode {
				up.Close()
			}
			w := perform(t, s, http.MethodDelete, "/api/settings/nodes/home", "")
			var result struct {
				Status  string `json:"status"`
				Warning string `json:"warning"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); nil != err || http.StatusOK != w.Code || "ok" != result.Status {
				t.Fatal("disconnect failed", w.Code, w.Body.String(), err)
			}
			if ("reset" != mode) != ("" != result.Warning) || strings.Contains(w.Body.String(), "private reset failure") || strings.Contains(w.Body.String(), "agent-test-token") {
				t.Fatal("incorrect reset result", w.Body.String())
			}
			expected := int32(1)
			if "offline" == mode {
				expected = 0
			}
			if expected != calls.Load() {
				t.Fatal("unexpected reset requests", calls.Load())
			}
			c, clients := s.snapshot()
			if 0 != len(c.Nodes) || 0 != len(clients) {
				t.Fatal("disconnected node retained in memory")
			}
			loaded, err := LoadConfig(s.config.path)
			if nil != err || 0 != len(loaded.Nodes) {
				t.Fatal("disconnected node retained on disk", err)
			}
		})
	}
}

func TestDisconnectSaveFailure(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	defer up.Close()
	s := persistedServer(t, up)
	s.config.path = t.TempDir()
	w := perform(t, s, http.MethodDelete, "/api/settings/nodes/home", "")
	if http.StatusInternalServerError != w.Code || 0 != calls.Load() || 1 != len(s.config.Nodes) {
		t.Fatal("failed local save reset the agent", w.Code, calls.Load())
	}
}

func TestSettingsWriteFailure(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	s := persistedServer(t, up)
	old := s.config
	s.config.path = t.TempDir()
	for _, request := range []struct{ method, path, body string }{
		{"PUT", "/api/settings/nodes/home", fmt.Sprintf(`{"name":"Changed","url":"https://127.0.0.1:9088","token":"replacement-secret","fingerprint":%q}`, testFingerprint(up))},
		{"DELETE", "/api/settings/nodes/home", ""},
		{"POST", "/api/settings/account", `{"username":"changed","password":"new-password","current_password":"test-password"}`},
	} {
		w := perform(t, s, request.method, request.path, request.body)
		if 500 != w.Code || strings.Contains(w.Body.String(), "replacement-secret") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	c, _ := s.snapshot()
	if old.Auth != c.Auth || old.Nodes["home"] != c.Nodes["home"] {
		t.Fatal("failed save changed runtime")
	}
}

func TestAccount(t *testing.T) {
	s := persistedServer(t, nil)
	oldSession := testSession(t, s)
	w := perform(t, s, "POST", "/api/settings/account", `{"username":"new-admin","password":"new-password","current_password":"wrong"}`)
	if 401 != w.Code {
		t.Fatal(w.Code)
	}
	w = perform(t, s, "POST", "/api/settings/account", `{"username":"new-admin","password":"new-password","current_password":"test-password"}`)
	if 200 != w.Code || 1 != len(w.Result().Cookies()) {
		t.Fatal(w.Code, w.Body.String())
	}
	currentSession := w.Result().Cookies()[0]
	c, err := LoadConfig(s.config.path)
	if nil != err || "new-admin" != c.Auth.Username || "file:secrets/password.hash" != c.Auth.Password || nil != bcrypt.CompareHashAndPassword([]byte(c.Auth.passwordHash), []byte("new-password")) {
		t.Fatal("credentials not persisted", err)
	}
	r := httptest.NewRequest("GET", "/api/auth/user", nil)
	r.AddCookie(oldSession)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 401 != w.Code {
		t.Fatal("old session retained")
	}
	r = httptest.NewRequest("GET", "/api/auth/user", nil)
	r.AddCookie(currentSession)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 200 != w.Code {
		t.Fatal("current session lost")
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"new-admin","password":"new-password"}`)))
	if 200 != w.Code {
		t.Fatal("new credentials rejected", w.Body.String())
	}
	oldHash := c.Auth.passwordHash
	w = perform(t, s, "POST", "/api/settings/account", `{"username":"renamed","password":"","current_password":"new-password"}`)
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	c, err = LoadConfig(s.config.path)
	if nil != err || oldHash != c.Auth.passwordHash || "renamed" != c.Auth.Username {
		t.Fatal("username-only update changed hash", err)
	}
}

func TestAccountRollback(t *testing.T) {
	s := persistedServer(t, nil)
	// A directory cannot be replaced by the atomic password-file write.
	blocked := filepath.Join(filepath.Dir(s.config.path), "blocked")
	if err := os.Mkdir(blocked, 0700); nil != err {
		t.Fatal(err)
	}
	s.config.Auth.Password = "file:blocked"
	if err := saveConfig(s.config); nil != err {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.config.path)
	w := perform(t, s, "POST", "/api/settings/account", `{"username":"changed","password":"new-password","current_password":"test-password"}`)
	after, _ := os.ReadFile(s.config.path)
	if 500 != w.Code || !bytes.Equal(before, after) || "admin" != s.config.Auth.Username {
		t.Fatal("failed secret write changed credentials", w.Code)
	}
}

func TestSetPassword(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	path := filepath.Join(t.TempDir(), "config", "dcompose.toml")
	if err := SetPassword(path, "admin", "first-password"); nil != err {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if nil != err || "file:secrets/password.hash" != c.Auth.Password {
		t.Fatal(err)
	}
	custom := filepath.Join(filepath.Dir(path), "custom.hash")
	if err := configutil.Write(custom, []byte(c.Auth.passwordHash)); nil != err {
		t.Fatal(err)
	}
	c.Auth.Password = "file:custom.hash"
	c.Nodes["home"] = NodeConfig{URL: up.URL, Token: "inline-secret", Fingerprint: testFingerprint(up)}
	if err := saveConfig(c); nil != err {
		t.Fatal(err)
	}
	if err := SetPassword(path, "changed", "second-password"); nil != err {
		t.Fatal(err)
	}
	c, err = LoadConfig(path)
	if nil != err || "file:custom.hash" != c.Auth.Password || "inline-secret" != c.Nodes["home"].Token || nil != bcrypt.CompareHashAndPassword([]byte(c.Auth.passwordHash), []byte("second-password")) {
		t.Fatal("set-password lost config or reference", err)
	}
	stat, _ := os.Stat(path)
	if 0600 != stat.Mode().Perm() {
		t.Fatal("config is not private")
	}
}

func TestPasswordBootstrap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dcompose.toml")
	data := "[auth]\nusername = 'admin'\npassword = 'file:secrets/custom.hash'\n"
	if err := os.WriteFile(path, []byte(data), 0600); nil != err {
		t.Fatal(err)
	}
	if err := SetPassword(path, "new-admin", "new-password"); nil != err {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if nil != err || "new-admin" != c.Auth.Username || "file:secrets/custom.hash" != c.Auth.Password {
		t.Fatal("bootstrap lost reference", err)
	}
}

func TestRelativeConfig(t *testing.T) {
	previous, err := os.Getwd()
	if nil != err {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); nil != err {
			t.Error(err)
		}
	})
	if err := SetPassword("config/dcompose.toml", "admin", "test-password"); nil != err {
		t.Fatal(err)
	}
	c, err := LoadConfig("config/dcompose.toml")
	if nil != err {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); nil != err {
		t.Fatal(err)
	}
	s := New(c, nil)
	w := perform(t, s, "POST", "/api/settings/account", `{"username":"renamed","password":"","current_password":"test-password"}`)
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	loaded, err := LoadConfig(filepath.Join(root, "config/dcompose.toml"))
	if nil != err || "renamed" != loaded.Auth.Username {
		t.Fatalf("account was not saved beside the loaded config: %v", err)
	}
}

func TestNodeTest(t *testing.T) {
	var requests atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if "Bearer agent-test-token" != r.Header.Get("Authorization") || strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) || "/health" != r.URL.Path {
			t.Error("incorrect health probe")
		}
		testHealth(w, r)
	}))
	defer up.Close()
	s := persistedServer(t, up)
	before, _ := os.ReadFile(s.config.path)
	w := perform(t, s, "POST", "/api/settings/nodes/test", fmt.Sprintf(`{"id":"home","url":%q,"fingerprint":%q}`, up.URL, testFingerprint(up)))
	after, _ := os.ReadFile(s.config.path)
	if 200 != w.Code || 1 != requests.Load() || !bytes.Equal(before, after) || fmt.Sprintf(`{"version":"test","api_version":%d}`, protocol.AgentAPI) != strings.TrimSpace(w.Body.String()) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestProtocol(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUpgradeRequired} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) {
					t.Error("protocol header missing")
				}
				if http.StatusUpgradeRequired == status {
					w.WriteHeader(status)
					_, _ = io.WriteString(w, "private protocol refusal")
					return
				}
				_, _ = io.WriteString(w, `{"status":"ok"}`)
			}))
			defer up.Close()
			s := testServer(t, up)
			assumeRegistriesApplied(s)
			for i, request := range []struct{ method, path, body string }{
				{"POST", "/stacks/demo/actions", `{"action":"deploy"}`},
				{"GET", "/info", ""},
				{"GET", "/health", ""},
			} {
				w := perform(t, s, request.method, "/api/nodes/home"+request.path, request.body)
				want := status
				if want != w.Code || int32(i+1) != requests.Load() {
					t.Fatal(request.path, w.Code, w.Body.String(), requests.Load())
				}
				if http.StatusUpgradeRequired == status {
					var response struct {
						Error struct {
							Code    int    `json:"code"`
							Message string `json:"message"`
						} `json:"error"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || status != response.Error.Code || "" == response.Error.Message || strings.Contains(w.Body.String(), "private protocol refusal") {
						t.Fatal("missing protocol error", w.Body.String())
					}
				}
			}
		})
	}
}

func TestNodeTestVersion(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"missing", `{"version":"test"}`, 200},
		{"different", fmt.Sprintf(`{"api_version":%d}`, protocol.AgentAPI+1), 200},
		{"refused", "private protocol refusal", 426},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if "/health" != r.URL.Path || strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) {
					t.Error("incorrect health probe")
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer up.Close()
			s := testServer(t, up)
			w := perform(t, s, "POST", "/api/settings/nodes/test", fmt.Sprintf(`{"id":"home","url":%q,"fingerprint":%q}`, up.URL, testFingerprint(up)))
			var response struct {
				Error struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || 426 != w.Code || 426 != response.Error.Code || "" == response.Error.Message || 1 != requests.Load() || strings.Contains(w.Body.String(), "private protocol refusal") {
				t.Fatal(w.Code, w.Body.String(), requests.Load())
			}
		})
	}
}

func TestProtocolEvents(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if "/channel" != r.URL.Path || strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) || "Bearer agent-test-token" != r.Header.Get("Authorization") {
			t.Error("incorrect channel handshake")
		}
		w.WriteHeader(http.StatusUpgradeRequired)
	}))
	defer up.Close()
	s := testServer(t, up)
	app := httptest.NewServer(s)
	defer app.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn := browserStream(t, app.URL+"/api/events", s)
	s.Start(context.Background())
	defer s.Close()
	// The initial connecting state is followed by the rejected handshake state.
	for states := 0; 2 > states; {
		_, data, err := conn.Read(ctx)
		if nil != err {
			t.Fatal("missing protocol state", err)
		}
		var event protocol.ChannelMessage
		if err := json.Unmarshal(data, &event); nil != err {
			t.Fatal(err)
		}
		if "node.state" != event.Event {
			continue
		}
		var state struct {
			Node    string `json:"node"`
			State   string `json:"state"`
			Message string `json:"message"`
		}
		if "event" != event.Type || nil != json.Unmarshal(event.Payload, &state) || "home" != state.Node || "unavailable" != state.State || "" == state.Message {
			t.Fatal("missing protocol state", string(data))
		}
		states++
	}
}

func TestConfigRace(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"stacks":[]}`)
	}))
	defer up.Close()
	s := persistedServer(t, up)
	var workers sync.WaitGroup
	for i := 0; 4 > i; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; 12 > j; j++ {
				for _, path := range []string{"/api/settings", "/api/nodes", "/api/nodes/home/stacks", "/api/auth/user"} {
					if 200 != perform(t, s, "GET", path, "").Code {
						t.Error("concurrent read failed", path)
					}
				}
			}
		}()
	}
	for i := 0; 12 > i; i++ {
		if 200 != perform(t, s, "PUT", "/api/settings/nodes/home", fmt.Sprintf(`{"name":"Home %d","url":%q,"fingerprint":%q}`, i, up.URL, testFingerprint(up))).Code {
			t.Fatal("concurrent write failed")
		}
	}
	workers.Wait()
}
