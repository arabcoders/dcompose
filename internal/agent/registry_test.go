package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func registryFixture(t *testing.T, a *agent) string {
	t.Helper()
	dir := t.TempDir()
	if err := a.initRegistries(dir); nil != err {
		t.Fatal(err)
	}
	return dir
}

func applyRegistryFixture(t *testing.T, a *agent) {
	t.Helper()
	if err := a.applyRegistries(registrySnapshot()); nil != err {
		t.Fatal(err)
	}
}

func registrySnapshot() protocol.RegistrySync {
	return protocol.RegistrySync{Revision: "revision-1", Registries: []protocol.RegistryCredential{
		{ID: "background", Registry: "docker.io", Label: "Checks", Auth: loginAuth("background", "background-secret"), BackgroundChecks: true},
		{ID: "pull", Registry: "docker.io", Auth: loginAuth("pull", "pull-secret")},
		{ID: "token", Registry: "git.example.test", Auth: map[string]string{"identitytoken": "identity-secret"}},
	}}
}

func loginAuth(username, password string) map[string]string {
	return map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(username + ":" + password))}
}

func readDockerConfig(t *testing.T, path string) dockerConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if nil != err {
		t.Fatal(err)
	}
	var config dockerConfig
	if err := json.Unmarshal(data, &config); nil != err {
		t.Fatal(err)
	}
	return config
}

func TestRegistryInitial(t *testing.T) {
	isolatedRegistry(t)
	hostConfig := filepath.Join(os.Getenv("DOCKER_CONFIG"), "config.json")
	write(t, hostConfig, `{"auths":{"docker.io":{"auth":"host-secret"}},"credsStore":"host-helper"}`)
	t.Setenv("REGISTRY_AUTH_FILE", hostConfig)
	a, _ := fixture(t)
	if "" == a.pullConfigDir() || filepath.Dir(hostConfig) == a.pullConfigDir() {
		t.Fatal("initial node uses host credentials")
	}
	if 0 != len(readDockerConfig(t, filepath.Join(a.pullConfigDir(), "config.json")).Auths) {
		t.Fatal("initial managed view is not empty")
	}
	if _, err := os.Stat(filepath.Join(a.registries.root, "current", "background")); !os.IsNotExist(err) {
		t.Fatal("unused background view was created")
	}
}

func TestRegistryViews(t *testing.T) {
	background, pull, err := registryConfigs(registrySnapshot().Registries)
	if nil != err {
		t.Fatal(err)
	}
	hub := "https://index.docker.io/v1/"
	if 1 != len(background.Auths) || 2 != len(pull.Auths) {
		t.Fatalf("auth counts: %d %d", len(background.Auths), len(pull.Auths))
	}
	for _, test := range []struct{ value, expected string }{
		{background.Auths[hub]["auth"], "background:background-secret"},
		{pull.Auths[hub]["auth"], "pull:pull-secret"},
	} {
		decoded, err := base64.StdEncoding.DecodeString(test.value)
		if nil != err || test.expected != string(decoded) {
			t.Fatal("incorrect account selected")
		}
	}
	if "identity-secret" != pull.Auths["git.example.test"]["identitytoken"] || "" != pull.Auths["git.example.test"]["auth"] {
		t.Fatal("identity token was encoded as a password")
	}
	// Stable IDs, not synchronization input order, settle equal-purpose ties.
	credentials := []protocol.RegistryCredential{
		{ID: "z", Registry: "docker.io", Auth: loginAuth("z", "secret-z")},
		{ID: "a", Registry: "https://index.docker.io/v1/", Auth: loginAuth("a", "secret-a")},
	}
	_, selected, err := registryConfigs(credentials)
	if nil != err || selected.Auths[hub]["auth"] != base64.StdEncoding.EncodeToString([]byte("a:secret-a")) {
		t.Fatal("unstable account selection")
	}
}

func TestRegistryKeys(t *testing.T) {
	credentials := []protocol.RegistryCredential{
		{ID: "hub", Registry: "https://index.docker.io/v1/", Auth: loginAuth("hub", "hub-secret"), BackgroundChecks: true},
		{ID: "access", Registry: "https://index.docker.io/v1/access-token", Auth: map[string]string{"identitytoken": "access-secret"}, BackgroundChecks: true},
		{ID: "refresh", Registry: "https://index.docker.io/v1/refresh-token", Auth: map[string]string{"registrytoken": "refresh-secret"}},
		{ID: "git", Registry: "git.example.test", Auth: map[string]string{"auth": "original-auth", "username": "git", "password": "git-secret", "email": "git@example.com", "serveraddress": "git.example.test", "identitytoken": "identity", "registrytoken": "bearer"}},
	}
	background, pull, err := registryConfigs(credentials)
	if nil != err || 2 != len(background.Auths) || 4 != len(pull.Auths) {
		t.Fatal("imported auth entries were collapsed")
	}
	for _, credential := range credentials {
		if _, exists := pull.Auths[credential.Registry]; !exists {
			t.Fatalf("original auth key was changed: %s", credential.Registry)
		}
		_, backgroundEntry := background.Auths[credential.Registry]
		if credential.BackgroundChecks != backgroundEntry {
			t.Fatal("entry purpose setting was not independent")
		}
		if !maps.Equal(credential.Auth, pull.Auths[credential.Registry]) {
			t.Fatal("native auth fields were changed")
		}
	}
	a, _ := fixture(t)
	dir := registryFixture(t, a)
	snapshot := protocol.RegistrySync{Revision: "native", Registries: credentials}
	if err := a.applyRegistries(snapshot); nil != err {
		t.Fatal(err)
	}
	stored := readDockerConfig(t, filepath.Join(a.pullConfigDir(), "config.json"))
	if !maps.Equal(credentials[3].Auth, stored.Auths[credentials[3].Registry]) {
		t.Fatal("node config changed native auth")
	}
	data, err := os.ReadFile(filepath.Join(dir, "managed-registry", "current", "snapshot.json"))
	var restored protocol.RegistrySync
	if nil != err || nil != json.Unmarshal(data, &restored) || !maps.Equal(credentials[3].Auth, restored.Registries[3].Auth) {
		t.Fatal("private snapshot changed native auth")
	}
	if "access-secret" != pull.Auths[credentials[1].Registry]["identitytoken"] || "refresh-secret" != pull.Auths[credentials[2].Registry]["registrytoken"] {
		t.Fatal("native token representation was lost")
	}
}

func TestRegistryNativeLogin(t *testing.T) {
	auth := map[string]string{"username": "native", "password": "password:token", "email": "native@example.com", "serveraddress": "native.example", "identitytoken": "identity", "registrytoken": "bearer"}
	credentials := []protocol.RegistryCredential{{ID: "native", Registry: "native.example", Auth: auth, BackgroundChecks: true}}
	background, pull, err := registryConfigs(credentials)
	if nil != err {
		t.Fatal(err)
	}
	expected := maps.Clone(auth)
	expected["auth"] = base64.StdEncoding.EncodeToString([]byte("native:password:token"))
	if !maps.Equal(expected, pull.Auths["native.example"]) || !maps.Equal(expected, background.Auths["native.example"]) {
		t.Fatal("native password login is not Docker-compatible or native fields changed")
	}
	if _, exists := auth["auth"]; exists {
		t.Fatal("runtime config changed stored native auth")
	}
	credentials[0].Auth = map[string]string{"identitytoken": "identity", "registrytoken": "bearer"}
	_, pull, err = registryConfigs(credentials)
	if nil != err || !maps.Equal(credentials[0].Auth, pull.Auths["native.example"]) {
		t.Fatal("token-only auth was changed")
	}
}

func TestRegistryPersistence(t *testing.T) {
	a, _ := fixture(t)
	dir := registryFixture(t, a)
	if err := a.applyRegistries(registrySnapshot()); nil != err {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "managed-registry")
	for _, path := range []string{root, filepath.Join(root, "current"), filepath.Join(root, "current", "pull")} {
		info, err := os.Stat(path)
		if nil != err || 0700 != info.Mode().Perm() {
			t.Fatalf("directory permissions: %s %v", path, err)
		}
	}
	for _, path := range []string{filepath.Join(a.pullConfigDir(), "config.json"), filepath.Join(root, "current", "snapshot.json")} {
		info, err := os.Stat(path)
		if nil != err || 0600 != info.Mode().Perm() {
			t.Fatalf("file permissions: %s %v", path, err)
		}
	}
	restarted := newAgent(a.ctx, a.config)
	if err := restarted.initRegistries(dir); nil != err || a.pullConfigDir() != restarted.pullConfigDir() {
		t.Fatalf("restore: %v", err)
	}
	if !maps.Equal(readDockerConfig(t, filepath.Join(restarted.pullConfigDir(), "config.json")).Auths["https://index.docker.io/v1/"], loginAuth("pull", "pull-secret")) {
		t.Fatal("restart lost managed pull credentials")
	}
	if err := a.applyRegistries(protocol.RegistrySync{Revision: "empty", Registries: []protocol.RegistryCredential{}}); nil != err {
		t.Fatal(err)
	}
	if err := restarted.initRegistries(dir); nil != err || "" == restarted.pullConfigDir() {
		t.Fatal("empty snapshot restored host credential fallback")
	}
	if 0 != len(readDockerConfig(t, filepath.Join(restarted.pullConfigDir(), "config.json")).Auths) {
		t.Fatal("deleted credentials remain in pull view")
	}
	entries, err := os.ReadDir(root)
	if nil != err || 2 != len(entries) {
		t.Fatal("replaced credential generations were retained")
	}
}

func TestRegistryChannel(t *testing.T) {
	a, _ := fixture(t)
	registryFixture(t, a)
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	commands := make(chan protocol.ChannelMessage, 1)
	outgoing := make(chan protocol.ChannelMessage, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.channelCommands(ctx, "test", commands, outgoing, cancel)
	}()
	defer func() { cancel(); <-done }()
	for _, valid := range []bool{true, false} {
		snapshot := registrySnapshot()
		if !valid {
			snapshot.Registries[0].Auth = nil
		}
		payload, _ := json.Marshal(snapshot)
		commands <- protocol.ChannelMessage{Type: "registry.sync", Payload: payload}
		select {
		case reply := <-outgoing:
			data, _ := json.Marshal(reply)
			if strings.Contains(string(data), "secret") || strings.Contains(string(data), "username") {
				t.Fatal("credential data escaped into channel response")
			}
			if valid {
				var applied protocol.RegistryApplied
				if "registry.applied" != reply.Type || nil != json.Unmarshal(reply.Payload, &applied) || snapshot.Revision != applied.Revision {
					t.Fatalf("ack: %+v", reply)
				}
				if 0 == len(readDockerConfig(t, filepath.Join(a.pullConfigDir(), "config.json")).Auths) {
					t.Fatal("ack preceded credential installation")
				}
			} else if "registry.error" != reply.Type || 400 > reply.Status {
				t.Fatalf("error: %+v", reply)
			}
		case <-time.After(time.Second):
			t.Fatal("missing registry response")
		}
	}
}

func TestRegistryApplyFailure(t *testing.T) {
	a, _ := fixture(t)
	configFile := filepath.Join(a.pullConfigDir(), "config.json")
	blocked := filepath.Join(t.TempDir(), "blocked")
	write(t, blocked, "blocked")
	a.registries.root = filepath.Join(blocked, "managed-registry")
	if err := a.applyRegistries(registrySnapshot()); nil == err {
		t.Fatal("failed file application replaced managed credentials")
	}
	if 0 != len(readDockerConfig(t, configFile).Auths) {
		t.Fatal("failed application changed the active empty view")
	}
}
