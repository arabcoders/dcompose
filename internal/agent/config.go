package agent

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/arabcoders/dcompose/internal/configutil"
)

type config struct {
	Global globalConfig `toml:"global"`
	Auth   authConfig   `toml:"auth"`
	TLS    struct {
		CertFile string `toml:"cert_file"`
		KeyFile  string `toml:"key_file"`
	} `toml:"tls"`
	token        [32]byte
	allowed      []netip.Prefix
	allowedHosts []string
	trusted      []netip.Prefix
	trustedHosts []string
	path         string
}

type globalConfig struct {
	NodeID             string `toml:"node_id,omitempty"`
	Paired             bool   `toml:"paired"`
	Engine             string `toml:"engine"`
	Listen             string `toml:"listen"`
	LogLevel           string `toml:"log_level"`
	StacksDir          string `toml:"stacks_dir"`
	ImageCheckInterval string `toml:"image_check_interval"`
}

type authConfig struct {
	Token          string   `toml:"token"`
	AllowedIPs     []string `toml:"allowed_ips"`
	TrustedProxies []string `toml:"trusted_proxies"`
}

func generateToken(path string) error {
	var token [32]byte
	if _, err := rand.Read(token[:]); nil != err {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); nil != err {
		return err
	}
	return writeNewFile(path, []byte(hex.EncodeToString(token[:])+"\n"), 0600)
}

func readConfig(path string) (config, error) {
	c := config{Global: globalConfig{Engine: "docker", Listen: "127.0.0.1:9077", LogLevel: "info", ImageCheckInterval: "6h"}, Auth: authConfig{AllowedIPs: []string{"0.0.0.0/0", "::/0"}}, path: path}
	meta, err := toml.DecodeFile(path, &c)
	if nil != err {
		return c, err
	}
	for _, key := range meta.Undecoded() {
		slog.Warn(fmt.Sprintf("Ignoring unknown configuration key %q in %q.", key.String(), path), "key", key.String(), "config", path)
	}
	if meta.IsDefined("global", "node_id") {
		decoded, err := hex.DecodeString(c.Global.NodeID)
		if nil != err || 0 == len(decoded) {
			return c, fmt.Errorf("global.node_id must be a nonempty hex ID")
		}
	}
	return c, nil
}

func loadConfig(path string) (config, error) {
	c, err := readConfig(path)
	if nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return c, err
		}
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			return c, err
		}
		if err := initializeConfig(path, &c); nil != err {
			return c, fmt.Errorf("cannot initialize %s: %w. The configuration directory must be writable by the runtime user", path, err)
		}
	}
	if "docker" != c.Global.Engine && "podman" != c.Global.Engine {
		return c, fmt.Errorf("unsupported engine: %s", c.Global.Engine)
	}
	if interval, err := time.ParseDuration(c.Global.ImageCheckInterval); nil != err || time.Minute > interval {
		return c, fmt.Errorf("image_check_interval must be a duration of at least 1m")
	}
	c.TLS.CertFile = configutil.Path(path, c.TLS.CertFile)
	c.TLS.KeyFile = configutil.Path(path, c.TLS.KeyFile)
	if _, _, err := net.SplitHostPort(c.Global.Listen); nil != err {
		return c, fmt.Errorf("listen: %w", err)
	}
	if !filepath.IsAbs(c.Global.StacksDir) {
		return c, fmt.Errorf("stacks_dir must be an absolute path")
	}
	c.Global.StacksDir, err = filepath.EvalSymlinks(c.Global.StacksDir)
	if nil != err {
		return c, fmt.Errorf("stacks_dir: %w", err)
	}
	info, err := os.Stat(c.Global.StacksDir)
	if nil != err || !info.IsDir() {
		return c, fmt.Errorf("stacks_dir must be an existing directory")
	}
	secret, err := resolveToken(c.Auth.Token, path)
	if nil != err {
		return c, err
	}
	c.token = sha256.Sum256([]byte(secret))
	c.allowed, c.allowedHosts, err = parseSourceEntries(c.Auth.AllowedIPs)
	if nil != err {
		return c, fmt.Errorf("auth.allowed_ips: %w", err)
	}
	c.trusted, c.trustedHosts, err = parseSourceEntries(c.Auth.TrustedProxies)
	if nil != err {
		return c, fmt.Errorf("auth.trusted_proxies: %w", err)
	}
	if "" == c.TLS.CertFile || "" == c.TLS.KeyFile {
		return c, fmt.Errorf("tls.cert_file and tls.key_file are required")
	}
	if _, err := tls.LoadX509KeyPair(c.TLS.CertFile, c.TLS.KeyFile); nil != err {
		return c, fmt.Errorf("tls: %w", err)
	}
	if "" == c.Global.NodeID {
		var random [8]byte
		if _, err := rand.Read(random[:]); nil != err {
			return c, fmt.Errorf("generate node ID: %w", err)
		}
		c.Global.NodeID = hex.EncodeToString(random[:])

		if err := saveGlobalSetting(path, "node_id", c.Global.NodeID); nil != err {
			return c, fmt.Errorf("save global.node_id: %w", err)
		}
	}
	return c, nil
}

func saveGlobalSetting(path, key string, value interface{}) error {
	// Preserve relative references and unknown keys instead of encoding runtime defaults.
	var document map[string]interface{}
	if _, err := toml.DecodeFile(path, &document); nil != err {
		return err
	}
	document["global"].(map[string]interface{})[key] = value
	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Encode(document); nil != err {
		return err
	}
	return configutil.Write(path, data.Bytes())
}

func initializeConfig(path string, c *config) error {
	c.Global.Listen = "0.0.0.0:9077"
	c.Global.StacksDir = "/opt/stacks"
	c.Auth.Token = "file:secrets/token"
	c.TLS.CertFile = "node.crt"
	c.TLS.KeyFile = "node.key"
	tokenPath := configutil.Path(path, "secrets/token")
	_, tokenErr := os.Lstat(tokenPath)
	if nil != tokenErr && !errors.Is(tokenErr, os.ErrNotExist) {
		return tokenErr
	}
	if nil == tokenErr {
		if _, err := resolveToken(c.Auth.Token, path); nil != err {
			return err
		}
	}
	certPath := configutil.Path(path, c.TLS.CertFile)
	keyPath := configutil.Path(path, c.TLS.KeyFile)
	_, certErr := os.Lstat(certPath)
	_, keyErr := os.Lstat(keyPath)
	newCert := errors.Is(certErr, os.ErrNotExist) && errors.Is(keyErr, os.ErrNotExist)
	if !newCert {
		if _, err := tls.LoadX509KeyPair(certPath, keyPath); nil != err {
			return fmt.Errorf("tls: %w", err)
		}
	}
	if err := os.MkdirAll(c.Global.StacksDir, 0750); nil != err {
		return fmt.Errorf("create stacks_dir: %w. Create /opt/stacks and give the runtime user access, or supply agent.toml with a writable global.stacks_dir", err)
	}
	if errors.Is(tokenErr, os.ErrNotExist) {
		if err := generateToken(tokenPath); nil != err {
			return err
		}
	}
	if newCert {
		if _, err := generateCert(filepath.Dir(path)); nil != err {
			return err
		}
	}
	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Encode(c); nil != err {
		return err
	}
	return configutil.Write(path, data.Bytes())
}

func resolveToken(value, path string) (string, error) {
	if "" == value {
		return "", fmt.Errorf("auth.token is required")
	}
	secret, err := configutil.Resolve(value, path)
	if nil != err {
		return "", fmt.Errorf("token: %w", err)
	}
	if "" == secret || strings.ContainsAny(secret, " \t\r\n") {
		return "", fmt.Errorf("token must contain one nonempty token")
	}
	return secret, nil
}
