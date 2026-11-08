package registry

import (
	"encoding/base64"
	"errors"
	"maps"
	"sort"
	"strings"

	"github.com/arabcoders/dcompose/internal/protocol"
)

// DockerConfig preserves imported native auth records in managed config views.
type DockerConfig struct {
	Auths map[string]map[string]string `json:"auths"`
}

// BuildConfigs selects background and pull/explicit credentials consistently.
func BuildConfigs(credentials []protocol.RegistryCredential) (DockerConfig, DockerConfig, error) {
	background := DockerConfig{Auths: make(map[string]map[string]string)}
	pull := DockerConfig{Auths: make(map[string]map[string]string)}
	ordered := append([]protocol.RegistryCredential(nil), credentials...)
	// Pull-only credentials take priority for explicit checks and pulls.
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].BackgroundChecks != ordered[j].BackgroundChecks {
			return !ordered[i].BackgroundChecks
		}
		return ordered[i].ID < ordered[j].ID
	})
	for _, credential := range ordered {
		registry := credential.Registry
		alias := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(registry, "https://"), "http://"), "/")
		if "docker.io" == alias || "index.docker.io" == alias || "index.docker.io/v1" == alias || "registry-1.docker.io" == alias {
			registry = "https://index.docker.io/v1/"
		}
		if "" == registry || 0 == len(credential.Auth) {
			return background, pull, errors.New("invalid registry credential")
		}
		auth := maps.Clone(credential.Auth)
		if _, exists := auth["auth"]; !exists && "" != auth["password"] {
			auth["auth"] = base64.StdEncoding.EncodeToString([]byte(auth["username"] + ":" + auth["password"]))
		}
		if _, exists := pull.Auths[registry]; !exists {
			pull.Auths[registry] = auth
		}
		if credential.BackgroundChecks {
			if _, exists := background.Auths[registry]; !exists {
				background.Auths[registry] = auth
			}
		}
	}
	return background, pull, nil
}

// RegistryHost matches managed registry views to canonical image references.
func RegistryHost(ref string) string {
	ref = strings.TrimPrefix(strings.TrimPrefix(ref, "https://"), "http://")
	return canonicalHost(strings.SplitN(ref, "/", 2)[0])
}
