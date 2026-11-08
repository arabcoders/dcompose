package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"strings"
	"unicode"

	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/protocol"
)

type RegistryConfig struct {
	ID               string            `toml:"id" json:"id"`
	Registry         string            `toml:"registry" json:"registry"`
	Label            string            `toml:"label" json:"label"`
	Auth             map[string]string `toml:"auth,omitempty" json:"-"`
	AuthFile         string            `toml:"auth_file,omitempty" json:"-"`
	BackgroundChecks bool              `toml:"background_checks" json:"background_checks"`
	auth             map[string]string
}

type registryInput struct {
	ID               string            `json:"id"`
	Registry         string            `json:"registry"`
	Label            string            `json:"label"`
	Auth             map[string]string `json:"auth"`
	BackgroundChecks bool              `json:"background_checks"`
}

type safeRegistry struct {
	ID               string            `json:"id"`
	Registry         string            `json:"registry"`
	Label            string            `json:"label"`
	Auth             map[string]string `json:"auth"`
	BackgroundChecks bool              `json:"background_checks"`
	HasAuth          bool              `json:"has_auth"`
	Username         string            `json:"username"`
}

func registrySecretField(field string) bool {
	return "username" != field && "email" != field && "serveraddress" != field
}

func publicRegistries(registries []RegistryConfig) interface{} {
	rows := make([]safeRegistry, 0, len(registries))
	for _, registry := range registries {
		auth := maps.Clone(registry.auth)
		for field := range auth {
			if registrySecretField(field) {
				auth[field] = ""
			}
		}
		username := registry.auth["username"]
		if "" == username {
			data, err := base64.StdEncoding.DecodeString(registry.auth["auth"])
			if decoded, _, ok := strings.Cut(string(data), ":"); nil == err && ok {
				username = decoded
			}
		}
		rows = append(rows, safeRegistry{ID: registry.ID, Registry: registry.Registry, Label: registry.Label, Auth: auth, BackgroundChecks: registry.BackgroundChecks, HasAuth: 0 < len(registry.auth), Username: username})
	}
	return struct {
		Registries []safeRegistry `json:"registries"`
	}{rows}
}

func validateRegistryKey(value string) error {
	// Docker auth keys are data, not request URLs. Keep their original spelling
	// and paths, including independent Docker Hub access/refresh-token records.
	if "" == strings.TrimSpace(value) || 0 <= strings.IndexFunc(value, unicode.IsControl) || strings.Contains(value, "@") {
		return fmt.Errorf("registry auth key must be nonempty and contain no control characters or user information")
	}
	return nil
}

func validateRegistries(rows []RegistryConfig, path string) ([]RegistryConfig, error) {
	registries := make([]RegistryConfig, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, registry := range rows {
		if !identity.MatchString(registry.ID) || seen[registry.ID] {
			return nil, fmt.Errorf("registry IDs must be nonempty and distinct")
		}
		seen[registry.ID] = true
		if err := validateRegistryKey(registry.Registry); nil != err {
			return nil, err
		}
		if nil == registry.auth {
			registry.auth = maps.Clone(registry.Auth)
			if "" != registry.AuthFile {
				data, err := configutil.Resolve(registry.AuthFile, path)
				if nil != err || nil != json.Unmarshal([]byte(data), &registry.auth) {
					return nil, fmt.Errorf("cannot read registry auth file")
				}
			}
		}
		if "" == registry.auth["auth"] && "" == registry.auth["password"] && "" == registry.auth["identitytoken"] && "" == registry.auth["registrytoken"] {
			return nil, fmt.Errorf("registry authentication is required")
		}
		if "" != registry.AuthFile {
			registry.Auth = nil
		}
		registries = append(registries, registry)
	}
	return registries, nil
}

func registrySnapshot(registries []RegistryConfig) protocol.RegistrySync {
	rows := make([]protocol.RegistryCredential, 0, len(registries))
	for _, registry := range registries {
		rows = append(rows, protocol.RegistryCredential{ID: registry.ID, Registry: registry.Registry, Label: registry.Label, Auth: maps.Clone(registry.auth), BackgroundChecks: registry.BackgroundChecks})
	}
	data, _ := json.Marshal(rows)
	revision := sha256.Sum256(data)
	return protocol.RegistrySync{Revision: hex.EncodeToString(revision[:]), Registries: rows}
}

func (s *Server) saveRegistries(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Registries []registryInput `json:"registries"`
	}
	if !decodeBody(w, r, &input, 1<<20) {
		return
	}
	if nil == input.Registries {
		apiError(w, 400, "Provide a registries array.")
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	existing := make(map[string]RegistryConfig, len(s.config.Registries))
	for _, registry := range s.config.Registries {
		existing[registry.ID] = registry
	}
	c := cloneConfig(s.config)
	c.Registries = make([]RegistryConfig, 0, len(input.Registries))
	for _, row := range input.Registries {
		if "" == row.ID {
			var id [16]byte
			if _, err := rand.Read(id[:]); nil != err {
				apiError(w, 500, "Could not generate a registry ID. Try again.")
				return
			}
			row.ID = hex.EncodeToString(id[:])
		}
		old := existing[row.ID]
		registry := RegistryConfig{ID: row.ID, Registry: row.Registry, Label: row.Label, auth: maps.Clone(row.Auth), BackgroundChecks: row.BackgroundChecks}
		for field, value := range registry.auth {
			if "" == value && registrySecretField(field) {
				registry.auth[field] = old.auth[field]
			}
		}
		// An existing encoded Docker login is edited with ordinary login fields.
		// Keep its native shape, including records that never had those fields.
		if _, encoded := row.Auth["auth"]; encoded && "" == row.Auth["auth"] && "" != old.auth["auth"] {
			username, suppliedUsername := row.Auth["username"]
			password := row.Auth["password"]
			data, err := base64.StdEncoding.DecodeString(old.auth["auth"])
			oldUsername, oldPassword, ok := strings.Cut(string(data), ":")
			expectedUsername, nativeUsername := old.auth["username"]
			if !nativeUsername {
				expectedUsername = oldUsername
			}
			editUsername := suppliedUsername && username != expectedUsername
			editPassword := "" != password && password != old.auth["password"]
			if editUsername || editPassword {
				if (nil != err || !ok) && (!suppliedUsername || "" == password) {
					apiError(w, 400, "Provide a valid Docker login before editing its username or password.")
					return
				}
				if !suppliedUsername {
					username = oldUsername
				}
				if "" == password {
					password = old.auth["password"]
					if "" == password {
						password = oldPassword
					}
				}
				if username != oldUsername || password != oldPassword {
					registry.auth["auth"] = base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
				}
			}
			if !nativeUsername {
				delete(registry.auth, "username")
			}
			if _, exists := old.auth["password"]; !exists {
				delete(registry.auth, "password")
			}
		}
		if maps.Equal(registry.auth, old.auth) {
			registry.AuthFile = old.AuthFile
		}
		c.Registries = append(c.Registries, registry)
	}
	rows, err := validateRegistries(c.Registries, c.path)
	if nil != err {
		apiError(w, 400, err.Error())
		return
	}
	c.Registries = rows
	var created []string
	saved := false
	defer func() {
		if !saved {
			for _, path := range created {
				_ = os.Remove(path)
			}
		}
	}()
	for i := range c.Registries {
		registry := &c.Registries[i]
		if "" != registry.AuthFile {
			continue
		}
		var suffix [16]byte
		if _, err := rand.Read(suffix[:]); nil != err {
			apiError(w, 500, "Could not save registry credentials.")
			return
		}
		registry.AuthFile = "file:secrets/registry-" + hex.EncodeToString(suffix[:]) + ".json"
		path := configutil.Path(c.path, strings.TrimPrefix(registry.AuthFile, "file:"))
		data, _ := json.Marshal(registry.auth)
		if err := configutil.Write(path, data); nil != err {
			apiError(w, 500, "Could not save registry credentials. Check configuration directory permissions.")
			return
		}
		created = append(created, path)
	}
	if err := saveConfig(c); nil != err {
		apiError(w, 500, "Could not save registry settings. Check configuration directory permissions.")
		return
	}
	saved = true
	s.applyConfig(c)
	jsonResponse(w, 200, publicRegistries(c.Registries))
}
