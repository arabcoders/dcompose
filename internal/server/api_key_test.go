package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestAPIKeyConfig(t *testing.T) {
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	key := strings.Repeat("cd", 32)
	for _, test := range []struct {
		name, reference, file string
	}{
		{name: "generated"},
		{name: "recovered", file: "secrets/api-key"},
		{name: "reference", reference: "file:custom-key", file: "custom-key"},
		{name: "inline", reference: key},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dcompose.toml")
			if "" != test.file {
				if err := configutil.Write(configutil.Path(path, test.file), []byte(key+"\n")); nil != err {
					t.Fatal(err)
				}
			}
			if "generated" != test.name {
				if err := saveConfig(Config{Auth: AuthConfig{APIKey: test.reference}, path: path}); nil != err {
					t.Fatal(err)
				}
			}
			c, err := LoadConfig(path)
			if nil != err {
				t.Fatal(err)
			}
			decoded, err := hex.DecodeString(c.Auth.apiKey)
			if nil != err || 32 != len(decoded) || 64 != len(c.Auth.apiKey) {
				t.Fatal("API key must remain encoded hexadecimal text", err)
			}
			reference := test.reference
			if "" == reference {
				reference = "file:secrets/api-key"
			}
			if reference != c.Auth.APIKey || ("generated" != test.name && key != c.Auth.apiKey) {
				t.Fatal("API key reference or existing value changed")
			}
			stored, err := readConfig(path)
			if nil != err || reference != stored.Auth.APIKey {
				t.Fatal("API key reference was not persisted", err)
			}
			files := []string{path}
			if filename, ok := strings.CutPrefix(reference, "file:"); ok {
				secretPath := configutil.Path(path, filename)
				files = append(files, secretPath)
				data, err := os.ReadFile(secretPath)
				if nil != err || c.Auth.apiKey != strings.TrimSuffix(string(data), "\n") {
					t.Fatal("API key file does not match resolved value", err)
				}
				data, err = os.ReadFile(path)
				if nil != err || strings.Contains(string(data), c.Auth.apiKey) {
					t.Fatal("configuration exposed resolved API key", err)
				}
			}
			for _, file := range files {
				info, err := os.Stat(file)
				if nil != err || 0600 != info.Mode().Perm() {
					t.Fatal("API key or configuration is not private", err)
				}
			}
			c.Global.LogLevel = "debug"
			if err := saveConfig(c); nil != err {
				t.Fatal(err)
			}
			loaded, err := LoadConfig(path)
			if nil != err || reference != loaded.Auth.APIKey || c.Auth.apiKey != loaded.Auth.apiKey {
				t.Fatal("API key changed after save and restart", err)
			}
			if strings.Contains(output.String(), c.Auth.apiKey) {
				t.Fatal("startup exposed API key in logs")
			}
		})
	}
}

func TestAPIKeyInvalid(t *testing.T) {
	for _, test := range []struct {
		name, reference, file string
	}{
		{name: "empty"},
		{name: "short", reference: strings.Repeat("ab", 31)},
		{name: "long", reference: strings.Repeat("ab", 33)},
		{name: "hex", reference: strings.Repeat("zz", 32)},
		{name: "missing", reference: "file:missing"},
		{name: "empty_reference", reference: "file:"},
		{name: "empty_file", reference: "file:key"},
		{name: "invalid_file", reference: "file:key", file: strings.Repeat("zz", 32)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dcompose.toml")
			data := fmt.Sprintf("[auth]\napi_key = %q\n", test.reference)
			if err := os.WriteFile(path, []byte(data), 0600); nil != err {
				t.Fatal(err)
			}
			if "file:key" == test.reference {
				if err := configutil.Write(configutil.Path(path, "key"), []byte(test.file)); nil != err {
					t.Fatal(err)
				}
			}
			_, err := LoadConfig(path)
			if nil == err {
				t.Fatal("invalid API key accepted")
			}
			if "" != test.file && strings.Contains(err.Error(), test.file) {
				t.Fatal("API key exposed in error")
			}
			if "" != test.reference && !strings.HasPrefix(test.reference, "file:") && strings.Contains(err.Error(), test.reference) {
				t.Fatal("inline API key exposed in error")
			}
			unchanged, err := os.ReadFile(path)
			if nil != err || data != string(unchanged) {
				t.Fatal("failed load replaced configuration", err)
			}
		})
	}
}

func TestAPIKeyAuth(t *testing.T) {
	key := strings.Repeat("cd", 32)
	for _, test := range []struct {
		name, header, cookie, query string
		explicit, setup, noSession  bool
		status                      int
	}{
		{name: "bearer", header: "Bearer " + key, explicit: true, status: 200},
		{name: "cookie", cookie: "valid", status: 200},
		{name: "absent", status: 401},
		{name: "bad_cookie", cookie: "invalid", status: 401},
		{name: "wrong", header: "Bearer " + strings.Repeat("ef", 32), explicit: true, status: 401},
		{name: "no_fallback", header: "Bearer wrong", cookie: "valid", explicit: true, status: 401},
		{name: "empty_header", cookie: "valid", explicit: true, status: 401},
		{name: "scheme", header: "Basic " + key, cookie: "valid", explicit: true, status: 401},
		{name: "missing_key", header: "Bearer", cookie: "valid", explicit: true, status: 401},
		{name: "extra", header: "Bearer " + key + " extra", cookie: "valid", explicit: true, status: 401},
		{name: "bad_cookie_bearer", header: "Bearer " + key, cookie: "invalid", explicit: true, status: 200},
		{name: "independent", header: "Bearer " + key, explicit: true, noSession: true, status: 200},
		{name: "query", query: "?api_key=" + key + "&apikey=" + key + "&token=" + key, status: 401},
		{name: "setup", header: "Bearer " + key, explicit: true, setup: true, status: 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := testServer(t, nil)
			s.config.Auth.APIKey, s.config.Auth.apiKey = key, key
			r := httptest.NewRequest("GET", "/api/nodes"+test.query, nil)
			if "valid" == test.cookie {
				r.AddCookie(testSession(t, s))
			} else if "invalid" == test.cookie {
				r.AddCookie(&http.Cookie{Name: "session", Value: "invalid"})
			}
			if test.setup {
				s.config.Auth.Username, s.config.Auth.Password = "", ""
			}
			if test.noSession {
				s.config.Auth.SessionKey, s.config.Auth.sessionKey = "", ""
			}
			if test.explicit {
				r.Header["Authorization"] = []string{test.header}
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if test.status != w.Code {
				t.Fatal("unexpected authentication status", w.Code)
			}
			if 401 == test.status {
				challenge := "Session"
				if test.explicit {
					challenge = "Bearer"
				}
				if challenge != w.Header().Get("WWW-Authenticate") {
					t.Fatal("incorrect authentication challenge")
				}
			}
		})
	}
}

func TestAPIKeyOrigin(t *testing.T) {
	s := testServer(t, nil)
	s.config.Auth.APIKey = strings.Repeat("cd", 32)
	s.config.Auth.apiKey = s.config.Auth.APIKey
	for _, test := range []struct {
		name, origin, site string
		status             int
	}{
		{name: "absent", status: 200},
		{name: "same", origin: "https://app.example", status: 200},
		{name: "foreign", origin: "https://evil.example", status: 403},
		{name: "fetch", site: "cross-site", status: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://app.example/api/auth/logout", nil)
			r.Header.Set("Authorization", "Bearer "+s.config.Auth.apiKey)
			r.Header.Set("Origin", test.origin)
			r.Header.Set("Sec-Fetch-Site", test.site)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if test.status != w.Code {
				t.Fatal("Bearer changed origin policy", w.Code)
			}
		})
	}
}

func TestAPIKeyHidden(t *testing.T) {
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	s := persistedServer(t, nil)
	s.config.Auth.APIKey = "file:secrets/api-key"
	s.config.Auth.apiKey = strings.Repeat("cd", 32)
	if err := configutil.Write(configutil.Path(s.config.path, "secrets/api-key"), []byte(s.config.Auth.apiKey)); nil != err {
		t.Fatal(err)
	}
	if err := saveConfig(s.config); nil != err {
		t.Fatal(err)
	}
	c, err := LoadConfig(s.config.path)
	if nil != err {
		t.Fatal(err)
	}
	s = New(c, nil)
	w := perform(t, s, "GET", "/api/settings", "")
	if 200 != w.Code {
		t.Fatal(w.Code)
	}
	for _, hidden := range []string{c.Auth.APIKey, c.Auth.apiKey, `"api_key"`, `"APIKey"`} {
		if strings.Contains(w.Body.String(), hidden) {
			t.Fatal("settings exposed API key")
		}
	}
	auth := c.Auth
	auth.APIKey = auth.apiKey
	data, err := json.Marshal(auth)
	if nil != err || strings.Contains(string(data), auth.APIKey) || strings.Contains(string(data), "APIKey") {
		t.Fatal("API key is JSON serializable", err)
	}
	w = perform(t, s, "POST", "/api/settings/account", `{"username":"renamed","password":"","current_password":"test-password"}`)
	if 200 != w.Code {
		t.Fatal("account save failed", w.Code)
	}
	loaded, err := LoadConfig(c.path)
	if nil != err || c.Auth.APIKey != loaded.Auth.APIKey || c.Auth.apiKey != loaded.Auth.apiKey {
		t.Fatal("account save changed API key", err)
	}
	r := httptest.NewRequest("GET", "/api/nodes", nil)
	r.Header.Set("Authorization", "Bearer "+c.Auth.apiKey+"-invalid")
	s.ServeHTTP(httptest.NewRecorder(), r)
	if strings.Contains(output.String(), c.Auth.apiKey) || strings.Contains(output.String(), c.Auth.APIKey) {
		t.Fatal("API key exposed in logs")
	}
}

func TestAPIKeyWebsocket(t *testing.T) {
	f := fakeChannel(t)
	s := testServer(t, f.server)
	s.config.Auth.APIKey = strings.Repeat("cd", 32)
	s.config.Auth.apiKey = s.config.Auth.APIKey
	s.config.Auth.SessionKey, s.config.Auth.sessionKey = "", ""
	startChannels(t, s)
	nextConnection(t, f)
	app := httptest.NewServer(s)
	t.Cleanup(app.Close)
	for _, test := range []struct {
		name, origin string
		status       int
	}{
		{name: "absent", status: 101},
		{name: "same", origin: app.URL, status: 101},
		{name: "foreign", origin: "https://evil.example", status: 403},
		{name: "scheme", origin: strings.Replace(app.URL, "http://", "https://", 1), status: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			headers := make(http.Header)
			headers.Set("Authorization", "Bearer "+s.config.Auth.apiKey)
			if "" != test.origin {
				headers.Set("Origin", test.origin)
			}
			conn, response, err := websocket.Dial(
				ctx,
				"ws"+strings.TrimPrefix(app.URL, "http")+"/api/events",
				&websocket.DialOptions{HTTPHeader: headers},
			)
			if nil != conn {
				defer func() { _ = conn.CloseNow() }()
			}
			if nil == response || test.status != response.StatusCode {
				t.Fatal("unexpected Bearer websocket handshake", response, err)
			}
			if 403 == test.status {
				if nil == err {
					t.Fatal("cross-origin websocket accepted")
				}
				return
			}
			if nil != err {
				t.Fatal(err)
			}
			for {
				var message protocol.ChannelMessage
				if err := wsjson.Read(ctx, conn, &message); nil != err {
					t.Fatal("missing Bearer workspace snapshot", err)
				}
				if "error" == message.Type {
					t.Fatal("Bearer websocket rejected", message.Status)
				}
				if "node.snapshot" == message.Event {
					return
				}
			}
		})
	}
}
