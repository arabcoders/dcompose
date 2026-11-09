package server

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/arabcoders/dcompose/internal/configutil"
	"golang.org/x/crypto/bcrypt"
)

type Config struct {
	Global     GlobalConfig          `toml:"global"`
	Auth       AuthConfig            `toml:"auth"`
	Nodes      map[string]NodeConfig `toml:"nodes"`
	Registries []RegistryConfig      `toml:"registries,omitempty" json:"-"`
	path       string
}

type GlobalConfig struct {
	Listen           string   `toml:"listen"`
	LogLevel         string   `toml:"log_level"`
	Origin           string   `toml:"origin"`
	SecureCookie     bool     `toml:"secure_cookie"`
	NodeOrder        []string `toml:"node_order,omitempty"`
	WebsocketOrigins []string `toml:"websocket_origins,omitempty"`
}

type AuthConfig struct {
	Username     string      `toml:"username"`
	Password     string      `toml:"password" json:"-"`
	SessionKey   string      `toml:"session_key,omitempty" json:"-"`
	APIKey       string      `toml:"api_key,omitempty" json:"-"`
	OIDC         *OIDCConfig `toml:"oidc,omitempty" json:"-"`
	passwordHash string
	sessionKey   string
	apiKey       string
}

type OIDCConfig struct {
	Issuer       string `toml:"issuer"`
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret" json:"-"`
	RedirectURI  string `toml:"redirect_uri"`
	clientSecret string
}

func (c AuthConfig) setupRequired() bool {
	return "" == c.Username && "" == c.Password
}

type NodeConfig struct {
	Name        string `toml:"name" json:"name"`
	URL         string `toml:"url" json:"url"`
	Token       string `toml:"token" json:"-"`
	Fingerprint string `toml:"fingerprint" json:"fingerprint"`
	PublicHost  string `toml:"public_host" json:"public_host"`
	Paused      bool   `toml:"paused,omitempty" json:"paused,omitempty"`
	token       string
}

var identity = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
var stackName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func LoadConfig(path string) (Config, error) {
	c, err := readConfig(path)
	if nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return c, err
		}
		if _, statErr := os.Lstat(c.path); !errors.Is(statErr, os.ErrNotExist) {
			return c, err
		}
		if err := saveConfig(c); nil != err {
			return c, fmt.Errorf("cannot initialize %s: %w. Make the configuration directory writable by the runtime user (Docker: set DCOMPOSE_UID and DCOMPOSE_GID to its owner)", c.path, err)
		}
	}
	c, err = validateConfig(c)
	if nil != err {
		return c, err
	}
	changed := false
	for _, secret := range []struct {
		name      string
		reference *string
	}{
		{"session-key", &c.Auth.SessionKey},
		{"api-key", &c.Auth.APIKey},
	} {
		if "" != *secret.reference {
			continue
		}
		path := "secrets/" + secret.name
		reference := "file:" + path
		if _, err := configutil.Resolve(reference, c.path); nil != err {
			if !errors.Is(err, os.ErrNotExist) {
				return c, fmt.Errorf("cannot read %s: %w", secret.name, err)
			}
			var token [32]byte
			if _, err := rand.Read(token[:]); nil != err {
				return c, fmt.Errorf("cannot generate %s", secret.name)
			}
			if err := configutil.Write(configutil.Path(c.path, path), []byte(hex.EncodeToString(token[:]))); nil != err {
				return c, fmt.Errorf("cannot save %s: %w", secret.name, err)
			}
		}
		*secret.reference = reference
		changed = true
	}
	if changed {
		c, err = validateConfig(c)
		if nil != err {
			return c, err
		}
		if err := saveConfig(c); nil != err {
			return c, fmt.Errorf("cannot save authentication configuration: %w", err)
		}
	}
	return c, nil
}

func readConfig(path string) (Config, error) {
	path, err := filepath.Abs(path)
	if nil != err {
		return Config{}, err
	}
	c := Config{Global: GlobalConfig{Listen: "0.0.0.0:8080", LogLevel: "info"}, Nodes: make(map[string]NodeConfig), path: path}
	meta, err := toml.DecodeFile(path, &c)
	if nil != err {
		return c, err
	}
	for _, key := range meta.Undecoded() {
		slog.Warn(fmt.Sprintf("Ignoring unknown configuration key %q in %q.", key.String(), path), "key", key.String(), "config", path)
	}
	if meta.IsDefined("auth", "session_key") && "" == c.Auth.SessionKey {
		return c, fmt.Errorf("auth.session_key must be 32 bytes encoded as 64 hexadecimal characters or a file reference")
	}
	if meta.IsDefined("auth", "api_key") && "" == c.Auth.APIKey {
		return c, fmt.Errorf("auth.api_key must be 32 bytes encoded as 64 hexadecimal characters or a file reference")
	}
	return c, nil
}

func validateConfig(c Config) (Config, error) {
	hash, err := configutil.Resolve(c.Auth.Password, c.path)
	if nil != err {
		return c, fmt.Errorf("cannot read auth.password")
	}
	c.Auth.passwordHash = hash
	key, err := configutil.Resolve(c.Auth.SessionKey, c.path)
	if nil != err {
		return c, fmt.Errorf("cannot read auth.session_key")
	}
	if "" != c.Auth.SessionKey {
		decoded, err := hex.DecodeString(key)
		if nil != err || 32 != len(decoded) {
			return c, fmt.Errorf("auth.session_key must be 32 bytes encoded as 64 hexadecimal characters or a file reference")
		}
		c.Auth.sessionKey = string(decoded)
	}
	c.Auth.apiKey, err = configutil.Resolve(c.Auth.APIKey, c.path)
	if nil != err {
		return c, fmt.Errorf("cannot read auth.api_key")
	}
	if "" != c.Auth.APIKey {
		decoded, err := hex.DecodeString(c.Auth.apiKey)
		if nil != err || 32 != len(decoded) {
			return c, fmt.Errorf("auth.api_key must be 32 bytes encoded as 64 hexadecimal characters or a file reference")
		}
	}
	return validateResolvedConfig(c)
}

func validateResolvedConfig(c Config) (Config, error) {
	registries, err := validateRegistries(c.Registries, c.path)
	if nil != err {
		return c, err
	}
	c.Registries = registries
	if !c.Auth.setupRequired() {
		if "" == strings.TrimSpace(c.Auth.Username) {
			return c, fmt.Errorf("auth.username is required")
		}
		if _, err := bcrypt.Cost([]byte(c.Auth.passwordHash)); nil != err {
			return c, fmt.Errorf("auth.password must be a bcrypt hash or file reference")
		}
	}
	if "" != c.Global.Origin {
		u, err := url.Parse(c.Global.Origin)
		if nil != err || ("https" != u.Scheme && "http" != u.Scheme) || "" == u.Host || "" != u.Path || nil != u.User || "" != u.RawQuery || "" != u.Fragment {
			return c, fmt.Errorf("global.origin must be an HTTP(S) origin without a path")
		}
	}
	for _, origin := range c.Global.WebsocketOrigins {
		if _, valid := validWebsocketOrigin(origin); !valid {
			return c, fmt.Errorf("global.websocket_origins must contain exact HTTP(S) origins without user information, paths, queries, fragments or wildcards")
		}
	}
	if nil != c.Auth.OIDC {
		oidc := *c.Auth.OIDC
		if "" == oidc.clientSecret {
			secret, err := configutil.Resolve(oidc.ClientSecret, c.path)
			if nil != err {
				return c, fmt.Errorf("cannot read auth.oidc.client_secret")
			}
			oidc.clientSecret = secret
		}
		if "" == strings.TrimSpace(oidc.ClientID) || "" == strings.TrimSpace(oidc.clientSecret) {
			return c, fmt.Errorf("auth.oidc requires client_id and client_secret")
		}
		for name, value := range map[string]string{"issuer": oidc.Issuer, "redirect_uri": oidc.RedirectURI} {
			u, err := url.Parse(value)
			if nil != err || ("https" != u.Scheme && "http" != u.Scheme) || "" == u.Host || nil != u.User || "" != u.RawQuery || "" != u.Fragment {
				return c, fmt.Errorf("auth.oidc.%s must be an HTTP(S) URL", name)
			}
		}
		c.Auth.OIDC = &oidc
	}
	nodes := make(map[string]NodeConfig, len(c.Nodes))
	for id, node := range c.Nodes {
		resolved, err := validateNode(id, node, c.path)
		if nil != err {
			return c, err
		}
		nodes[id] = resolved
	}
	c.Nodes = nodes
	return c, nil
}

func validateNode(id string, node NodeConfig, path string) (NodeConfig, error) {
	if !identity.MatchString(id) {
		return node, fmt.Errorf("node ID must contain only letters, digits, underscores or hyphens")
	}
	u, err := url.Parse(node.URL)
	if nil != err || "" == u.Host || nil != u.User || "" != u.RawQuery || "" != u.Fragment || "https" != u.Scheme {
		return node, fmt.Errorf("node URL must use HTTPS without credentials, a query or a fragment")
	}
	pin, err := base64.StdEncoding.DecodeString(node.Fingerprint)
	if nil != err || 32 != len(pin) {
		return node, fmt.Errorf("node requires a Base64-encoded SHA-256 public-key fingerprint")
	}
	if "" == node.token {
		node.token, err = configutil.Resolve(node.Token, path)
		if nil != err {
			return node, fmt.Errorf("cannot read node token file; check its path and permissions")
		}
	}
	if "" == node.token || strings.ContainsAny(node.token, "\r\n") {
		return node, fmt.Errorf("node token must not be empty or contain line breaks")
	}
	if "" == node.Name {
		node.Name = id
	}
	if "" == node.PublicHost {
		node.PublicHost = u.Hostname()
	}
	return node, nil
}

func encodeConfig(c Config) ([]byte, error) {
	var data bytes.Buffer
	err := toml.NewEncoder(&data).Encode(c)
	return data.Bytes(), err
}

func saveConfig(c Config) error {
	data, err := encodeConfig(c)
	if nil != err {
		return err
	}
	return configutil.Write(c.path, data)
}

// SetPassword creates or updates credentials without replacing existing secret references.
func SetPassword(path, username, password string) error {
	if "" == password {
		return fmt.Errorf("password must not be empty")
	}
	c, err := readConfig(path)
	if nil != err {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			return err
		}
		c.Nodes = make(map[string]NodeConfig)
	}
	_, err = changeCredentials(c, username, password)
	return err
}

func changeCredentials(c Config, username, password string) (Config, error) {
	if "" == strings.TrimSpace(username) {
		return c, fmt.Errorf("username is required")
	}
	if "" == password {
		c.Auth.Username = username
		return c, saveConfig(c)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if nil != err {
		return c, fmt.Errorf("password must not exceed 72 bytes")
	}
	oldData, readErr := os.ReadFile(c.path)
	if nil != readErr && !errors.Is(readErr, os.ErrNotExist) {
		return c, readErr
	}
	c.Auth.Username = username
	c.Auth.passwordHash = string(hash)
	if !strings.HasPrefix(c.Auth.Password, "file:") {
		c.Auth.Password = "file:secrets/password.hash"
	}
	c, err = validateResolvedConfig(c)
	if nil != err {
		return c, err
	}
	if err := saveConfig(c); nil != err {
		return c, err
	}
	secretPath := configutil.Path(c.path, strings.TrimPrefix(c.Auth.Password, "file:"))
	if err := configutil.Write(secretPath, hash); nil != err {
		var rollback error
		if nil == readErr {
			rollback = configutil.Write(c.path, oldData)
		} else {
			rollback = os.Remove(c.path)
		}
		return c, errors.Join(fmt.Errorf("cannot save password file"), rollback)
	}
	return c, nil
}

func cloneConfig(c Config) Config {
	c.Registries = slices.Clone(c.Registries)
	for i := range c.Registries {
		c.Registries[i].Auth = maps.Clone(c.Registries[i].Auth)
		c.Registries[i].auth = maps.Clone(c.Registries[i].auth)
	}
	c.Global.NodeOrder = slices.Clone(c.Global.NodeOrder)
	c.Global.WebsocketOrigins = slices.Clone(c.Global.WebsocketOrigins)
	nodes := make(map[string]NodeConfig, len(c.Nodes))
	maps.Copy(nodes, c.Nodes)
	c.Nodes = nodes
	return c
}

func orderedNodeIDs(c Config) []string {
	ids := make([]string, 0, len(c.Nodes))
	seen := make(map[string]bool, len(c.Nodes))
	for _, id := range c.Global.NodeOrder {
		if _, exists := c.Nodes[id]; exists && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	remaining := make([]string, 0, len(c.Nodes)-len(ids))
	for id := range c.Nodes {
		if !seen[id] {
			remaining = append(remaining, id)
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		left, right := strings.ToLower(c.Nodes[remaining[i]].Name), strings.ToLower(c.Nodes[remaining[j]].Name)
		if left == right {
			return remaining[i] < remaining[j]
		}
		return left < right
	})
	return append(ids, remaining...)
}
