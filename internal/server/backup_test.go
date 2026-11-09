package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/yeka/zip"
)

func TestBackupExport(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if "/backup" != r.URL.Path || "Bearer agent-test-token" != r.Header.Get("Authorization") {
			t.Error("unexpected node backup request")
		}
		jsonResponse(w, 200, protocol.NodeBackup{Files: []protocol.BackupFile{
			{Path: "app/compose.yml", Data: []byte("# raw compose\nservices: {}\n")},
			{Path: "app/.env", Data: []byte("TOKEN=secret\r\n")},
		}})
	}))
	defer up.Close()
	s := testServer(t, up)
	s.config.Nodes["other"] = s.config.Nodes["home"]
	s.nodes["other"] = nodeClient(s.config.Nodes["other"])
	s.config.Nodes["home"] = NodeConfig{
		Name: "Home", URL: up.URL, Token: "file:node-token", token: "agent-test-token",
		Fingerprint: testFingerprint(up), PublicHost: "public.example",
	}
	s.config.Auth.Password = "file:password.hash"
	s.config.Auth.SessionKey = "file:session-key"
	s.config.Auth.APIKey = "file:api-key"
	s.config.Auth.apiKey = strings.Repeat("cd", 32)
	s.config.Auth.OIDC = &OIDCConfig{ClientSecret: "file:oidc-secret", clientSecret: "oidc-secret"}
	s.config.Registries = []RegistryConfig{
		{ID: "registry", Registry: "https://index.docker.io/v1/", Label: "Personal", AuthFile: "file:registry-auth", auth: map[string]string{"username": "alice", "password": "registry-password", "auth": "native-encoded", "identitytoken": "native-identity", "registrytoken": "native-registry", "email": "alice@example.com", "serveraddress": "docker.io"}, BackgroundChecks: true},
		{ID: "refresh", Registry: "https://index.docker.io/v1/refresh-token", AuthFile: "file:refresh-auth", auth: map[string]string{"username": "<token>", "password": "refresh-secret:client-id"}},
	}
	for _, nodes := range [][]string{{"home"}, {"home", "other"}} {
		body, err := json.Marshal(map[string]any{"nodes": nodes, "password": "backup-password"})
		if nil != err {
			t.Fatal(err)
		}
		w := perform(t, s, "POST", "/api/backup", string(body))
		if 200 != w.Code || "application/zip" != w.Header().Get("Content-Type") || "no-store" != w.Header().Get("Cache-Control") {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
		archive, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
		if nil != err {
			t.Fatal(err)
		}
		files := make(map[string]string)
		for _, file := range archive.File {
			if !file.IsEncrypted() {
				t.Fatalf("unencrypted entry: %s", file.Name)
			}
			file.SetPassword("wrong-password")
			if reader, err := file.Open(); nil == err {
				_, err = io.ReadAll(reader)
				_ = reader.Close()
				if nil == err {
					t.Fatal("wrong password accepted")
				}
			}
			file.SetPassword("backup-password")
			reader, err := file.Open()
			if nil != err {
				t.Fatal(err)
			}
			data, err := io.ReadAll(reader)
			_ = reader.Close()
			if nil != err {
				t.Fatal(err)
			}
			files[file.Name] = string(data)
		}
		var config Config
		if _, err := toml.Decode(files["dcompose.toml"], &config); nil != err {
			t.Fatal(err)
		}
		if s.config.Auth.passwordHash != config.Auth.Password || strings.Repeat("ab", 32) != config.Auth.SessionKey || "oidc-secret" != config.Auth.OIDC.ClientSecret || "agent-test-token" != config.Nodes["home"].Token {
			t.Fatal("backup did not resolve configuration secrets")
		}
		if s.config.Auth.apiKey != config.Auth.APIKey {
			t.Fatal("backup did not resolve API key")
		}
		if 2 != len(config.Registries) || !reflect.DeepEqual(s.config.Registries[0].auth, config.Registries[0].Auth) || "" != config.Registries[0].AuthFile || "Personal" != config.Registries[0].Label || !config.Registries[0].BackgroundChecks {
			t.Fatal("encrypted backup did not resolve registry credentials")
		}
		registries, err := validateRegistries(config.Registries, "")
		if nil != err || !reflect.DeepEqual(s.config.Registries[0].auth, registries[0].auth) {
			t.Fatal("backup registry credentials cannot be restored", err)
		}
		if "refresh-secret:client-id" != registries[1].auth["password"] || "refresh-secret:client-id" != config.Registries[1].Auth["password"] || "https://index.docker.io/v1/refresh-token" != config.Registries[1].Registry {
			t.Fatal("backup did not preserve independent OAuth auth records")
		}
		delete(files, "dcompose.toml")
		want := make(map[string]string)
		for _, id := range nodes {
			want["nodes/"+id+"/app/compose.yml"] = "# raw compose\nservices: {}\n"
			want["nodes/"+id+"/app/.env"] = "TOKEN=secret\r\n"
		}
		if !reflect.DeepEqual(want, files) {
			t.Fatal("backup included wrong nodes or modified stack files")
		}
	}
	if "file:node-token" != s.config.Nodes["home"].Token || "file:oidc-secret" != s.config.Auth.OIDC.ClientSecret || "file:api-key" != s.config.Auth.APIKey {
		t.Fatal("backup modified server configuration")
	}
	if "file:registry-auth" != s.config.Registries[0].AuthFile || "file:refresh-auth" != s.config.Registries[1].AuthFile || nil != s.config.Registries[0].Auth {
		t.Fatal("backup modified registry configuration")
	}
}

func TestBackupFailure(t *testing.T) {
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 500, protocol.NodeBackup{Error: "stack broken: cannot read .env"})
	}))
	defer up.Close()
	s := testServer(t, up)
	for _, body := range []string{
		`{"nodes":[],"password":"backup-password"}`,
		`{"nodes":["missing"],"password":"backup-password"}`,
		`{"nodes":["home","home"],"password":"backup-password"}`,
		`{"nodes":["home"],"password":"short"}`,
	} {
		if w := perform(t, s, "POST", "/api/backup", body); 400 != w.Code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := perform(t, s, "POST", "/api/backup", `{"nodes":["home"],"password":"backup-password"}`)
	var response struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || 502 != w.Code || 502 != response.Error.Code || "" == response.Error.Message || strings.Contains(w.Body.String(), "broken") || "application/zip" == w.Header().Get("Content-Type") {
		t.Fatal(w.Code, w.Body.String())
	}
	var record struct {
		Message   string `json:"msg"`
		Level     string `json:"level"`
		Node      string `json:"node"`
		Operation string `json:"operation"`
		Status    int    `json:"status"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); nil != err {
		t.Fatal(err, output.String())
	}
	if !strings.Contains(record.Node, "Home") || !strings.Contains(record.Node, up.URL) || "" == record.Operation || 500 != record.Status || !strings.Contains(record.Error, "stack broken: cannot read .env") {
		t.Fatal("missing private backup failure details", output.String())
	}
	if "ERROR" != record.Level || !strings.Contains(record.Message, record.Node) || !strings.Contains(record.Message, record.Error) || strings.Contains(output.String(), "backup-password") || strings.Contains(output.String(), "agent-test-token") {
		t.Fatal("backup message hides failure context or leaks credentials", output.String())
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/api/backup", strings.NewReader(`{"nodes":["home"],"password":"backup-password"}`)))
	if 401 != w.Code {
		t.Fatal("unauthenticated backup allowed")
	}
}

func TestBackupPath(t *testing.T) {
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 200, protocol.NodeBackup{Files: []protocol.BackupFile{{Path: "../compose.yml"}}})
	}))
	defer up.Close()
	s := testServer(t, up)
	w := perform(t, s, "POST", "/api/backup", `{"nodes":["home"],"password":"backup-password"}`)
	var response struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || 502 != w.Code || 502 != response.Error.Code || "" == response.Error.Message || strings.Contains(w.Body.String(), "../compose.yml") || "application/zip" == w.Header().Get("Content-Type") {
		t.Fatal(w.Code, w.Body.String())
	}
	var record struct {
		Message   string `json:"msg"`
		Level     string `json:"level"`
		Node      string `json:"node"`
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); nil != err || "ERROR" != record.Level || !strings.Contains(record.Node, "Home") || !strings.Contains(record.Node, up.URL) || "" == record.Operation {
		t.Fatal("missing backup diagnostic", output.String())
	}
	if !strings.Contains(record.Message, "Home") || !strings.Contains(record.Message, up.URL) {
		t.Fatal("backup message hides path failure", record.Message)
	}
}
