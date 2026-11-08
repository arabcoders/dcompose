package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); nil != err {
		t.Fatal(err)
	}
}

func configFixture(t *testing.T, extra string, auth string) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := generateCert(dir); nil != err {
		t.Fatal(err)
	}
	token := filepath.Join(dir, "token")
	write(t, token, "test-secret\n")
	path := filepath.Join(dir, "config.toml")
	write(t, path, fmt.Sprintf("[tls]\ncert_file = 'node.crt'\nkey_file = 'node.key'\n[auth]\ntoken = %q\n%s\n[global]\nstacks_dir = %q\n%s", "file:"+token, auth, dir, extra))
	return path
}

func TestToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node", "token")
	if err := generateToken(path); nil != err {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if nil != err {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(string(data))
	decoded, err := hex.DecodeString(secret)
	if nil != err || 32 != len(decoded) {
		t.Fatal("expected a 256-bit hex token")
	}
	info, err := os.Stat(path)
	if nil != err || 0600 != info.Mode().Perm() {
		t.Fatal("token file must have mode 0600")
	}
	configPath := filepath.Join(dir, "config.toml")
	if _, err := generateCert(dir); nil != err {
		t.Fatal(err)
	}
	write(t, configPath, fmt.Sprintf("[tls]\ncert_file = 'node.crt'\nkey_file = 'node.key'\n[global]\nstacks_dir = %q\n[auth]\ntoken = %q", dir, "file:"+path))
	c, err := loadConfig(configPath)
	if nil != err || sha256.Sum256([]byte(secret)) != c.token {
		t.Fatalf("generated token cannot be loaded: %v", err)
	}
	if err := generateToken(path); nil == err {
		t.Fatal("overwrote existing token")
	}
	unchanged, err := os.ReadFile(path)
	if nil != err || string(data) != string(unchanged) {
		t.Fatal("existing token changed")
	}
	other := filepath.Join(dir, "other-token")
	if err := generateToken(other); nil != err {
		t.Fatal(err)
	}
	otherData, err := os.ReadFile(other)
	if nil != err || string(data) == string(otherData) {
		t.Fatal("expected independently generated tokens")
	}
}

func TestConfig(t *testing.T) {
	c, err := loadConfig(configFixture(t, "", ""))
	if nil != err {
		t.Fatal(err)
	}
	if "docker" != c.Global.Engine || "127.0.0.1:9077" != c.Global.Listen || "info" != c.Global.LogLevel || "6h" != c.Global.ImageCheckInterval || 2 != len(c.allowed) || sha256.Sum256([]byte("test-secret")) != c.token {
		t.Fatalf("unexpected config: %+v", c)
	}
	c, err = loadConfig(configFixture(t, "log_level = 'warn'", "allowed_ips = []"))
	if nil != err || 0 != len(c.allowed) || "warn" != c.Global.LogLevel {
		t.Fatalf("explicit config: %+v, %v", c, err)
	}
	for name, extra := range map[string]string{
		"bad_listen":   "listen = 'no-port'",
		"bad_tls":      "",
		"bad_interval": "image_check_interval = 'invalid'", "short_interval": "image_check_interval = '5s'",
		"unsupported_engine": "engine = 'unknown'", "empty_engine": "engine = ''",
	} {
		t.Run(name, func(t *testing.T) {
			path := configFixture(t, extra, "")
			if "bad_tls" == name {
				write(t, filepath.Join(filepath.Dir(path), "node.crt"), "invalid certificate")
			}
			if _, err := loadConfig(path); nil == err {
				t.Fatal("accepted invalid config")
			}
		})
	}
	if _, err := loadConfig(configFixture(t, "", "allowed_ips = ['https://bad.example']")); nil == err {
		t.Fatal("accepted invalid allowed IP")
	}
	for _, text := range []string{
		fmt.Sprintf("stacks_dir = %q\ntoken = 'secret'", t.TempDir()),
		"[global]\nstacks_dir = 'relative'\n[auth]\ntoken = 'file:/missing'",
		fmt.Sprintf("[global]\nstacks_dir = %q", t.TempDir()),
		fmt.Sprintf("[global]\nstacks_dir = %q\n[auth]\ntoken = 'file:/missing'", t.TempDir()),
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		write(t, path, text)
		if _, err := loadConfig(path); nil == err {
			t.Fatal("accepted invalid paths")
		}
	}
	path := configFixture(t, "", "")
	data, _ := os.ReadFile(path)
	write(t, path, strings.Replace(string(data), "cert_file = 'node.crt'\nkey_file = 'node.key'\n", "", 1))
	if _, err := loadConfig(path); nil == err {
		t.Fatal("accepted missing TLS credentials")
	}
	path = configFixture(t, "", "")
	write(t, filepath.Join(filepath.Dir(path), "token"), " \n")
	if _, err := loadConfig(path); nil == err {
		t.Fatal("accepted empty token")
	}
}

func TestConfigSources(t *testing.T) {
	resolver := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		t.Error("startup attempted DNS resolution")
		return nil, context.Canceled
	}}
	t.Cleanup(func() { net.DefaultResolver = resolver })
	c, err := loadConfig(configFixture(t, "", "allowed_ips = ['192.0.2.3', 'dash_service.invalid', 'node.invalid.']\ntrusted_proxies = ['198.51.100.0/24', 'proxy_service.invalid']"))
	if nil != err {
		t.Fatal(err)
	}
	if !slices.Equal(c.allowed, []netip.Prefix{netip.MustParsePrefix("192.0.2.3/32")}) || !slices.Equal(c.allowedHosts, []string{"dash_service.invalid", "node.invalid."}) || !slices.Equal(c.trusted, []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}) || !slices.Equal(c.trustedHosts, []string{"proxy_service.invalid"}) || 2 != len(c.Auth.TrustedProxies) {
		t.Fatalf("source entries were not separated: %+v", c)
	}
	if a := newAgent(context.Background(), c); 0 != len(a.dns.entries) {
		t.Fatal("startup populated the DNS cache")
	}
	if _, err := loadConfig(configFixture(t, "", "trusted_proxies = ['proxy.invalid:80']")); nil == err {
		t.Fatal("accepted invalid trusted proxy")
	}
}

func TestSourceEntries(t *testing.T) {
	prefixes, hosts, err := parseSourceEntries([]string{"192.0.2.3", "2001:db8::/64", "dash_service", "Node.Example."})
	if nil != err || !slices.Equal(prefixes, []netip.Prefix{netip.MustParsePrefix("192.0.2.3/32"), netip.MustParsePrefix("2001:db8::/64")}) || !slices.Equal(hosts, []string{"dash_service", "node.example."}) {
		t.Fatalf("valid source entries: %v %v %v", prefixes, hosts, err)
	}
	for _, entry := range []string{"", "https://node.example", "node.example:80", "*.example", "bad..example", "-bad.example", "bad-.example", strings.Repeat("a", 64) + ".example"} {
		t.Run(entry, func(t *testing.T) {
			if _, _, err := parseSourceEntries([]string{entry}); nil == err {
				t.Fatalf("accepted invalid source %q", entry)
			}
		})
	}
}

func TestConfigToken(t *testing.T) {
	for _, test := range []struct {
		name, value, content string
		valid                bool
	}{
		{"inline", "inline-secret", "", true},
		{"relative", "file:token", "test-secret\n", true},
		{"missing", "file:missing", "", false},
		{"directory", "file:.", "", false},
		{"empty", "file:token", "", false},
		{"blank", "file:token", "\n", false},
		{"spaces", "file:token", " test-secret \n", false},
		{"multiline", "file:token", "test-secret\n\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := configFixture(t, "", "")
			dir := filepath.Dir(path)
			write(t, filepath.Join(dir, "token"), test.content)
			write(t, path, fmt.Sprintf("[tls]\ncert_file = 'node.crt'\nkey_file = 'node.key'\n[global]\nstacks_dir = %q\n[auth]\ntoken = %q", dir, test.value))
			t.Chdir(t.TempDir())
			c, err := loadConfig(path)
			if !test.valid {
				if nil == err {
					t.Fatal("accepted invalid token reference")
				}
				return
			}
			if nil != err {
				t.Fatal(err)
			}
			secret := "test-secret"
			if "inline" == test.name {
				secret = test.value
			}
			if test.value != c.Auth.Token || sha256.Sum256([]byte(secret)) != c.token {
				t.Fatal("token reference or resolved credential changed")
			}
		})
	}
}

func TestConfigRelativePaths(t *testing.T) {
	path := configFixture(t, "", "")
	dir := filepath.Dir(path)
	t.Chdir(t.TempDir())
	c, err := loadConfig(path)
	if nil != err {
		t.Fatal(err)
	}
	if filepath.Join(dir, "node.crt") != c.TLS.CertFile || filepath.Join(dir, "node.key") != c.TLS.KeyFile || dir != c.Global.StacksDir {
		t.Fatalf("paths did not resolve beside the loaded TOML: %+v", c)
	}
}

func TestConfigImageInterval(t *testing.T) {
	c, err := loadConfig(configFixture(t, "image_check_interval = '30m'", ""))
	if nil != err || "30m" != c.Global.ImageCheckInterval {
		t.Fatalf("image interval: %s %v", c.Global.ImageCheckInterval, err)
	}
	var output bytes.Buffer
	if err := toml.NewEncoder(&output).Encode(c); nil != err {
		t.Fatal(err)
	}
	var stored config
	metadata, err := toml.Decode(output.String(), &stored)
	if nil != err {
		t.Fatal(err)
	}
	if metadata.IsDefined("global", "image_check_config_file") || !metadata.IsDefined("global", "image_check_interval") || "30m" != stored.Global.ImageCheckInterval {
		t.Fatal("obsolete credential option remains or image scheduling option was removed")
	}
}

func TestAuth(t *testing.T) {
	c, err := loadConfig(configFixture(t, "", "allowed_ips = ['127.0.0.1', '::1', '192.0.2.0/24', '2001:db8::/64']"))
	if nil != err {
		t.Fatal(err)
	}
	a := newAgent(context.Background(), c)
	for _, test := range []struct {
		peer, token string
		code        int
	}{
		{"127.0.0.1:1", "Bearer test-secret", 200},
		{"[::1]:1", "Bearer test-secret", 200},
		{"[::ffff:127.0.0.1]:1", "Bearer test-secret", 200},
		{"[2001:db8::a]:1", "Bearer test-secret", 200},
		{"192.0.2.8:1", "Bearer test-secret", 200},
		{"127.0.0.1:1", "Bearer wrong", 401},
		{"127.0.0.1:1", "", 401},
		{"127.0.0.1:1", "Basic test-secret", 401},
		{"203.0.113.1:1", "Bearer test-secret", 403},
		{"[2001:db9::a]:1", "Bearer test-secret", 403},
		{"bad-peer", "Bearer test-secret", 403},
	} {
		r := httptest.NewRequest("GET", "/health", nil)
		r.RemoteAddr = test.peer
		r.Header.Set("Authorization", test.token)
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		r.Header.Set("Forwarded", "for=127.0.0.1")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if test.code != w.Code {
			t.Fatalf("%s %q: got %d, want %d", test.peer, test.token, w.Code, test.code)
		}
	}
	for _, path := range []string{"/health", "/stacks", "/stacks/foo", "/operations/1", "/stacks/foo/logs", "/not-found"} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "127.0.0.1:1"
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if 401 != w.Code {
			t.Fatalf("unauthenticated route %s: %d", path, w.Code)
		}
	}
	a.config.allowed = nil
	r := httptest.NewRequest("GET", "/health", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if 403 != w.Code {
		t.Fatal("empty allowlist did not fail closed")
	}
}
