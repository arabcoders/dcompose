package agent

import (
	"path/filepath"
	"testing"
)

func isolatedRegistry(t *testing.T) {
	t.Helper()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	t.Setenv("REGISTRY_AUTH_FILE", filepath.Join(t.TempDir(), "auth.json"))
}
