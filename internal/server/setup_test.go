package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestSetup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "dcompose.toml")
	c, err := LoadConfig(path)
	if nil != err || !c.Auth.setupRequired() || "0.0.0.0:8080" != c.Global.Listen || "info" != c.Global.LogLevel {
		t.Fatal("fresh configuration", err)
	}
	s := New(c, nil)
	w := perform(t, s, "GET", "/api/auth/status", "")
	if 200 != w.Code || !strings.Contains(w.Body.String(), `"setup_required":true`) ||
		!strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"/api/nodes", "/api/settings", "/api/auth/user"} {
		if 401 != perform(t, s, "GET", path, "").Code {
			t.Fatal("setup exposed protected endpoint", path)
		}
	}
	if 404 != perform(t, s, "GET", "/api/auth/oidc/login", "").Code {
		t.Fatal("OIDC available before setup")
	}
	r := httptest.NewRequest("POST", "/api/auth/setup", strings.NewReader(`{"username":"admin","password":"test-password"}`))
	r.Header.Set("Origin", "https://attacker.example")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 403 != w.Code {
		t.Fatal("cross-origin setup accepted")
	}
	for _, body := range []string{`{}`, `{"username":"admin","password":""}`, `{"username":" ","password":"password"}`, fmt.Sprintf(`{"username":"admin","password":%q}`, strings.Repeat("a", 73))} {
		if 400 != perform(t, s, "POST", "/api/auth/setup", body).Code {
			t.Fatal("invalid setup accepted")
		}
	}
	w = perform(t, s, "POST", "/api/auth/setup", `{"username":"admin","password":"test-password"}`)
	if 201 != w.Code || 1 != len(w.Result().Cookies()) {
		t.Fatal(w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || http.SameSiteStrictMode != cookie.SameSite {
		t.Fatal("unprotected setup session")
	}
	r = httptest.NewRequest("GET", "/api/auth/status", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"setup_required":false`) || !strings.Contains(w.Body.String(), `"authenticated":true`) {
		t.Fatal(w.Body.String())
	}
	c, err = LoadConfig(path)
	if nil != err || c.Auth.setupRequired() || "file:secrets/password.hash" != c.Auth.Password ||
		nil != bcrypt.CompareHashAndPassword([]byte(c.Auth.passwordHash), []byte("test-password")) {
		t.Fatal("setup not persisted", err)
	}
	data, err := os.ReadFile(path)
	if nil != err || strings.Contains(string(data), c.Auth.passwordHash) {
		t.Fatal("config leaked hash", err)
	}
	for _, file := range []string{path, filepath.Join(filepath.Dir(path), "secrets/password.hash")} {
		info, err := os.Stat(file)
		if nil != err || 0600 != info.Mode().Perm() {
			t.Fatal("credentials not private", err)
		}
	}
	s = New(c, nil)
	w = perform(t, s, "POST", "/api/auth/setup", `{"username":"other","password":"password"}`)
	if 409 != w.Code || "admin" != s.config.Auth.Username || 0 != len(w.Result().Cookies()) {
		t.Fatal("setup reopened on restart", w.Code, w.Body.String())
	}
	w = perform(t, s, "POST", "/api/auth/login", `{"username":"admin","password":"test-password"}`)
	if 200 != w.Code {
		t.Fatal("saved account cannot sign in", w.Code, w.Body.String())
	}
}

func TestSetupInvalidConfig(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.MinCost)
	for name, data := range map[string]string{
		"malformed":     "[auth",
		"username_only": "[auth]\nusername = 'admin'", "password_only": fmt.Sprintf("[auth]\npassword = %q", hash),
		"invalid_hash":        "[auth]\nusername = 'admin'\npassword = 'invalid'",
		"missing_password":    "[auth]\nusername = 'admin'\npassword = 'file:missing'",
		"invalid_hash_file":   "[auth]\nusername = 'admin'\npassword = 'file:invalid.hash'",
		"unreadable_file":     "[auth]\nusername = 'admin'\npassword = 'file:unreadable.hash'",
		"unreadable_password": "[auth]\nusername = 'admin'\npassword = 'file:.'",
		"missing_node_token":  fmt.Sprintf("[nodes.home]\nurl = '%s'\nfingerprint = '%s'\ntoken = 'file:missing'", up.URL, testFingerprint(up)),
		"missing_oidc_secret": "[auth.oidc]\nissuer = 'https://id.example'\nclient_id = 'app'\nclient_secret = 'file:missing'\nredirect_uri = 'https://app.example/api/auth/oidc/callback'",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dcompose.toml")
			if err := os.WriteFile(path, []byte(data), 0600); nil != err {
				t.Fatal(err)
			}
			if "invalid_hash_file" == name {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), "invalid.hash"), []byte("invalid"), 0600); nil != err {
					t.Fatal(err)
				}
			}
			if "unreadable_file" == name {
				if 0 == os.Geteuid() {
					t.Skip("root can read files with mode 0000")
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), "unreadable.hash"), hash, 0000); nil != err {
					t.Fatal(err)
				}
			}
			if _, err := LoadConfig(path); nil == err {
				t.Fatal("invalid configuration enabled setup")
			}
			unchanged, _ := os.ReadFile(path)
			if data != string(unchanged) {
				t.Fatal("failed load replaced config")
			}
		})
	}
}

func TestSetupPermissions(t *testing.T) {
	if 0 == os.Geteuid() {
		t.Skip("root can write directories with mode 0500")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0500); nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0700); nil != err {
			t.Error(err)
		}
	})
	_, err := LoadConfig(filepath.Join(dir, "dcompose.toml"))
	var pathErr *os.PathError
	if !errors.Is(err, os.ErrPermission) || !errors.As(err, &pathErr) {
		t.Fatal("missing permission error", err)
	}
}

func TestSetupSaveFailure(t *testing.T) {
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	c, err := LoadConfig(filepath.Join(t.TempDir(), "dcompose.toml"))
	if nil != err {
		t.Fatal(err)
	}
	c.path = t.TempDir()
	s := New(c, nil)
	w := perform(t, s, "POST", "/api/auth/setup", `{"username":"admin","password":"setup-secret-password"}`)
	if 500 != w.Code || !s.config.Auth.setupRequired() || 0 != len(w.Result().Cookies()) {
		t.Fatal("failed setup changed account", w.Code)
	}
	var record struct {
		Message string `json:"msg"`
		Level   string `json:"level"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(
		output.Bytes(),
		&record,
	); nil != err || "ERROR" != record.Level || "" == record.Error || !strings.Contains(record.Message, c.path) || !strings.Contains(record.Message, record.Error) ||
		strings.Contains(output.String(), "setup-secret-password") {
		t.Fatal("setup message hides failure context or leaks password", output.String(), err)
	}
}

func TestSetupKeepsConfig(t *testing.T) {
	up := httptest.NewTLSServer(nil)
	defer up.Close()
	path := filepath.Join(t.TempDir(), "dcompose.toml")
	data := fmt.Sprintf(
		"[global]\nlisten = '127.0.0.1:8085'\norigin = 'https://app.example'\nsecure_cookie = true\n[nodes.home]\nurl = '%s'\nfingerprint = '%s'\ntoken = 'node-secret'\n",
		up.URL,
		testFingerprint(up),
	)
	if err := os.WriteFile(path, []byte(data), 0600); nil != err {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if nil != err {
		t.Fatal(err)
	}
	w := perform(t, New(c, nil), "POST", "/api/auth/setup", `{"username":"admin","password":"password"}`)
	if 201 != w.Code || !w.Result().Cookies()[0].Secure {
		t.Fatal(w.Code, w.Body.String())
	}
	c, err = LoadConfig(path)
	if nil != err || "127.0.0.1:8085" != c.Global.Listen || "https://app.example" != c.Global.Origin ||
		"node-secret" != c.Nodes["home"].token {
		t.Fatal("setup lost existing configuration", err)
	}
}
