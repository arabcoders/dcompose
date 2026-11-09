package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/configutil"
)

func registryRows(t *testing.T, response *httptest.ResponseRecorder) []safeRegistry {
	t.Helper()
	var data struct {
		Registries []safeRegistry `json:"registries"`
	}
	if 200 != response.Code || nil != json.Unmarshal(response.Body.Bytes(), &data) || nil == data.Registries {
		t.Fatal("invalid registry response", response.Code)
	}
	return data.Registries
}

func TestRegistryAddress(t *testing.T) {
	for _, input := range []string{"docker.io", "index.docker.io", "registry-1.docker.io", "https://index.docker.io/v1/", "https://index.docker.io/v1/access-token", "https://index.docker.io/v1/refresh-token", "HTTPS://GHCR.IO/namespace", "localhost:5000", "[::1]:5000", "registry.example/path"} {
		if err := validateRegistryKey(input); nil != err {
			t.Errorf("auth key %q: %v", input, err)
		}
	}
	for _, input := range []string{"", " ", "https://user:password@ghcr.io", "ghcr.io\n", "ghcr.io\x00", "ghcr.io\tpath"} {
		if err := validateRegistryKey(input); nil == err {
			t.Errorf("accepted address %q", input)
		}
	}
}

func TestRegistries(t *testing.T) {
	s := persistedServer(t, nil)
	auth := map[string]string{
		"auth":          base64.StdEncoding.EncodeToString([]byte("encoded-user:encoded-password")),
		"username":      "alice",
		"password":      "private-password",
		"identitytoken": "private-identity",
		"registrytoken": "private-token",
		"email":         "alice@example.com",
		"serveraddress": "https://index.docker.io/v1/",
	}
	body, _ := json.Marshal(
		map[string]any{
			"registries": []registryInput{
				{Registry: "https://index.docker.io/v1/", Label: "Personal / checks", Auth: auth, BackgroundChecks: true},
				{Registry: "registry-1.docker.io", Auth: map[string]string{"registrytoken": "second-token"}},
			},
		},
	)
	w := perform(t, s, "PUT", "/api/settings/registries", string(body))
	rows := registryRows(t, w)
	if 2 != len(rows) || rows[0].ID == rows[1].ID || "" == rows[0].ID || "https://index.docker.io/v1/" != rows[0].Registry ||
		!rows[0].HasAuth ||
		!rows[0].BackgroundChecks ||
		rows[1].BackgroundChecks ||
		"Personal / checks" != rows[0].Label ||
		"alice" != rows[0].Username {
		t.Fatal("registry metadata not retained")
	}
	for field, value := range rows[0].Auth {
		if registrySecretField(field) && "" != value {
			t.Fatal("secret field was not masked")
		}
	}
	if len(auth) != len(rows[0].Auth) || "alice@example.com" != rows[0].Auth["email"] {
		t.Fatal("native fields lost in public view")
	}
	for _, path := range []string{"/api/settings", "/api/settings/registries"} {
		response := perform(t, s, "GET", path, "")
		for _, secret := range []string{auth["auth"], "private-password", "private-identity", "private-token", "second-token", "file:secrets/"} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatal("public settings leaked registry secrets")
			}
		}
	}
	loaded, err := LoadConfig(s.config.path)
	if nil != err || 2 != len(loaded.Registries) || !maps.Equal(auth, loaded.Registries[0].auth) ||
		"Personal / checks" != loaded.Registries[0].Label {
		t.Fatal("native registry persistence failed", err)
	}
	for _, registry := range loaded.Registries {
		if !strings.HasPrefix(registry.AuthFile, "file:secrets/") || nil != registry.Auth {
			t.Fatal("auth not stored in a private file")
		}
		stat, err := os.Stat(configutil.Path(loaded.path, strings.TrimPrefix(registry.AuthFile, "file:")))
		if nil != err || 0600 != stat.Mode().Perm() {
			t.Fatal("registry file permissions", err)
		}
	}
	encoded, err := json.Marshal(loaded)
	if nil != err || strings.Contains(string(encoded), "private-password") || strings.Contains(string(encoded), "registry-") {
		t.Fatal("config JSON leaked registry storage")
	}
	cloned := cloneConfig(loaded)
	cloned.Registries[0].auth["username"] = "changed"
	if "alice" != loaded.Registries[0].auth["username"] {
		t.Fatal("registry snapshot was not cloned")
	}
	// Masked auth fields round-trip without exposing or changing secrets.
	input := []registryInput{{ID: rows[0].ID, Registry: rows[0].Registry, Label: "", Auth: rows[0].Auth, BackgroundChecks: false}}
	body, _ = json.Marshal(map[string]any{"registries": input})
	registryRows(t, perform(t, s, "PUT", "/api/settings/registries", string(body)))
	current, _ := s.snapshot()
	if !maps.Equal(auth, current.Registries[0].auth) || loaded.Registries[0].AuthFile != current.Registries[0].AuthFile ||
		"" != current.Registries[0].Label ||
		current.Registries[0].BackgroundChecks {
		t.Fatal("blank secret preservation or metadata editing failed")
	}
}

func TestRegistryReplace(t *testing.T) {
	s := persistedServer(t, nil)
	rows := registryRows(
		t,
		perform(
			t,
			s,
			"PUT",
			"/api/settings/registries",
			`{"registries":[{"registry":"ghcr.io","auth":{"username":"alice","password":"original-secret"},"background_checks":true}]}`,
		),
	)
	id := rows[0].ID
	old, _ := s.snapshot()
	for _, body := range []string{
		`{"registries":[{"registry":"ghcr.io","auth":{"username":"alice"}}]}`,
		`{"registries":[{"id":"unknown","registry":"ghcr.io","auth":{"password":""}}]}`,
		fmt.Sprintf(`{"registries":[{"id":%q,"registry":"ghcr.io","auth":{"identitytoken":""}}]}`, id),
		fmt.Sprintf(`{"registries":[{"id":%q,"registry":"ghcr.io","auth":{"password":""}},{"id":%q,"registry":"other.example","auth":{"password":""}}]}`, id, id),
		`{"registries":[{"registry":"ghcr.io","auth":{"password":42}}]}`,
		`{}`, `{"registries":null}`,
	} {
		if 400 != perform(t, s, "PUT", "/api/settings/registries", body).Code {
			t.Fatal("invalid registry replacement accepted")
		}
	}
	body := fmt.Sprintf(
		`{"registries":[{"id":%q,"registry":"registry.example:5000","auth":{"username":"renamed","password":""},"background_checks":false}]}`,
		id,
	)
	registryRows(t, perform(t, s, "PUT", "/api/settings/registries", body))
	current, _ := s.snapshot()
	if "original-secret" != current.Registries[0].auth["password"] || "alice" != old.Registries[0].auth["username"] {
		t.Fatal("ID-based preservation or immutable snapshot failed")
	}
	body = fmt.Sprintf(`{"registries":[{"id":%q,"registry":"registry.example:5000","auth":{"identitytoken":"replacement-token"}}]}`, id)
	registryRows(t, perform(t, s, "PUT", "/api/settings/registries", body))
	current, _ = s.snapshot()
	if "replacement-token" != current.Registries[0].auth["identitytoken"] || old.Registries[0].AuthFile == current.Registries[0].AuthFile {
		t.Fatal("auth replacement failed")
	}
	if 0 != len(registryRows(t, perform(t, s, "PUT", "/api/settings/registries", `{"registries":[]}`))) {
		t.Fatal("registry deletion failed")
	}
	loaded, err := LoadConfig(s.config.path)
	if nil != err || 0 != len(loaded.Registries) || 1 != len(old.Registries) {
		t.Fatal("deletion not persisted or old snapshot changed", err)
	}
}

func TestRegistryLogin(t *testing.T) {
	s := persistedServer(t, nil)
	rows := registryRows(
		t,
		perform(
			t,
			s,
			"PUT",
			"/api/settings/registries",
			`{"registries":[{"registry":"docker.io","auth":{"auth":"YWxpY2U6cGFzczp3b3Jk"}}]}`,
		),
	)
	if "alice" != rows[0].Username {
		t.Fatal("encoded username not available for editing")
	}
	body := fmt.Sprintf(`{"registries":[{"id":%q,"registry":"docker.io","auth":{"auth":"","username":"bob","password":""}}]}`, rows[0].ID)
	registryRows(t, perform(t, s, "PUT", "/api/settings/registries", body))
	current, _ := s.snapshot()
	if 1 != len(current.Registries[0].auth) ||
		base64.StdEncoding.EncodeToString([]byte("bob:pass:word")) != current.Registries[0].auth["auth"] {
		t.Fatal("username edit did not preserve password or native shape")
	}
	body = fmt.Sprintf(
		`{"registries":[{"id":%q,"registry":"docker.io","auth":{"auth":"","username":"bob","password":"new:password"}}]}`,
		rows[0].ID,
	)
	registryRows(t, perform(t, s, "PUT", "/api/settings/registries", body))
	current, _ = s.snapshot()
	if 1 != len(current.Registries[0].auth) ||
		base64.StdEncoding.EncodeToString([]byte("bob:new:password")) != current.Registries[0].auth["auth"] {
		t.Fatal("password replacement failed")
	}
}

func TestRegistryDuplicates(t *testing.T) {
	s := persistedServer(t, nil)
	rows := registryRows(
		t,
		perform(
			t,
			s,
			"PUT",
			"/api/settings/registries",
			`{"registries":[{"registry":"docker.io","auth":{"password":"first"}},{"registry":"docker.io","auth":{"password":"second"}}]}`,
		),
	)
	if 2 != len(rows) || rows[0].ID == rows[1].ID {
		t.Fatal("distinct logins for the same registry were not retained")
	}
}

func TestRegistrySaveFailure(t *testing.T) {
	s := persistedServer(t, nil)
	rows := registryRows(
		t,
		perform(
			t,
			s,
			"PUT",
			"/api/settings/registries",
			`{"registries":[{"registry":"https://index.docker.io/v1/refresh-token","auth":{"password":"original-secret"}}]}`,
		),
	)
	before, _ := s.snapshot()
	beforeRevision := s.registrySync.Revision
	s.config.path = t.TempDir()
	body := fmt.Sprintf(
		`{"registries":[{"id":%q,"registry":"https://index.docker.io/v1/refresh-token","auth":{"password":"failed-replacement"}}]}`,
		rows[0].ID,
	)
	w := perform(t, s, "PUT", "/api/settings/registries", body)
	after, _ := s.snapshot()
	if 500 != w.Code || strings.Contains(w.Body.String(), "failed-replacement") ||
		!reflect.DeepEqual(before.Registries, after.Registries) ||
		beforeRevision != s.registrySync.Revision {
		t.Fatal("failed save replaced active registry credentials")
	}
	data, err := os.ReadFile(configutil.Path(before.path, strings.TrimPrefix(before.Registries[0].AuthFile, "file:")))
	if nil != err || !strings.Contains(string(data), "original-secret") {
		t.Fatal("failed save overwrote previous secret", err)
	}
	created, err := filepath.Glob(configutil.Path(s.config.path, "secrets/registry-*.json"))
	if nil != err || 0 != len(created) {
		t.Fatal("failed save retained new secret files", err)
	}
}

func TestRegistryRevision(t *testing.T) {
	rows := []RegistryConfig{
		{
			ID:               "a",
			Registry:         "docker.io",
			Label:            "Checks",
			auth:             map[string]string{"password": "secret"},
			AuthFile:         "file:first",
			BackgroundChecks: true,
		},
	}
	first := registrySnapshot(rows)
	rows[0].AuthFile = "file:second"
	if first.Revision != registrySnapshot(rows).Revision {
		t.Fatal("storage path changed effective revision")
	}
	rows[0].auth["password"] = "new-secret"
	if first.Revision == registrySnapshot(rows).Revision || "secret" != first.Registries[0].Auth["password"] {
		t.Fatal("secret replacement did not change revision or changed old snapshot")
	}
	rows[0].auth["password"] = "secret"
	rows[0].BackgroundChecks = false
	if first.Revision == registrySnapshot(rows).Revision || first.Revision == registrySnapshot(nil).Revision ||
		nil == registrySnapshot(nil).Registries {
		t.Fatal("effective settings or empty snapshot revision invalid")
	}
}

func TestRegistryAuthKeys(t *testing.T) {
	s := persistedServer(t, nil)
	body := `{"registries":[{"registry":"https://index.docker.io/v1/","auth":{"auth":"hub-secret"},"background_checks":true},{"registry":"https://index.docker.io/v1/access-token","auth":{"identitytoken":"access-secret"}},{"registry":"https://index.docker.io/v1/refresh-token","auth":{"password":"refresh-secret:client-id"},"background_checks":true},{"registry":"registry.example/path","auth":{"registrytoken":"registry-bearer"}}]}`
	rows := registryRows(t, perform(t, s, "PUT", "/api/settings/registries", body))
	if 4 != len(rows) || "https://index.docker.io/v1/" != rows[0].Registry ||
		"https://index.docker.io/v1/access-token" != rows[1].Registry ||
		"https://index.docker.io/v1/refresh-token" != rows[2].Registry {
		t.Fatal("original auth keys not preserved")
	}
	loaded, err := LoadConfig(s.config.path)
	if nil != err || 4 != len(loaded.Registries) || "refresh-secret:client-id" != loaded.Registries[2].auth["password"] {
		t.Fatal("OAuth auth record persistence failed", err)
	}
	before, _ := s.snapshot()
	body = fmt.Sprintf(
		`{"registries":[{"id":%q,"registry":"https://index.docker.io/v1/refresh-token","auth":{"password":""},"background_checks":false}]}`,
		rows[2].ID,
	)
	registryRows(t, perform(t, s, "PUT", "/api/settings/registries", body))
	after, _ := s.snapshot()
	if 1 != len(after.Registries) || 4 != len(before.Registries) || "refresh-secret:client-id" != after.Registries[0].auth["password"] ||
		after.Registries[0].BackgroundChecks {
		t.Fatal("independent auth row editing or deletion failed")
	}
}
