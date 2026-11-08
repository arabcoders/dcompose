package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/engine/registry"
	"github.com/arabcoders/dcompose/internal/protocol"
)

type managedRegistries struct {
	mu   sync.Mutex
	root string
}

type dockerConfig = registry.DockerConfig

func registryConfigs(credentials []protocol.RegistryCredential) (dockerConfig, dockerConfig, error) {
	return registry.BuildConfigs(credentials)
}

// Load the last applied snapshot before workers or helper operations start.
func (a *agent) initRegistries(configDir string) error {
	a.registries = &managedRegistries{root: filepath.Join(configDir, "managed-registry")}
	data, err := os.ReadFile(filepath.Join(a.registries.root, "current", "snapshot.json"))
	if os.IsNotExist(err) {
		return a.applyRegistries(protocol.RegistrySync{})
	}
	if nil != err {
		return errors.New("could not load managed registry credentials")
	}
	var snapshot protocol.RegistrySync
	if err := json.Unmarshal(data, &snapshot); nil != err {
		return errors.New("could not load managed registry credentials")
	}
	_, _, err = registryConfigs(snapshot.Registries)
	if nil != err {
		return err
	}
	return nil
}

func (a *agent) applyRegistries(snapshot protocol.RegistrySync) error {
	if nil == a.registries {
		return errors.New("managed registry storage is unavailable")
	}
	a.registries.mu.Lock()
	defer a.registries.mu.Unlock()
	_, pull, err := registryConfigs(snapshot.Registries)
	if nil != err {
		return err
	}
	root := a.registries.root
	if err := os.MkdirAll(root, 0700); nil != err {
		return errors.New("could not prepare managed registry storage")
	}
	if err := os.Chmod(root, 0700); nil != err {
		return errors.New("could not secure managed registry storage")
	}
	dir, err := os.MkdirTemp(root, "generation-")
	if nil != err {
		return errors.New("could not prepare managed registry storage")
	}
	applied := false
	defer func() {
		if !applied {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := os.Mkdir(filepath.Join(dir, "pull"), 0700); nil != err {
		return errors.New("could not prepare managed registry view")
	}
	data, err := json.Marshal(pull)
	if nil != err || nil != configutil.Write(filepath.Join(dir, "pull", "config.json"), data) {
		return errors.New("could not write managed registry view")
	}
	data, err = json.Marshal(snapshot)
	if nil != err || nil != configutil.Write(filepath.Join(dir, "snapshot.json"), data) {
		return errors.New("could not write managed registry snapshot")
	}
	// One rename publishes the pull config and the durable snapshot.
	link := dir + "-link"
	if err := os.Symlink(filepath.Base(dir), link); nil != err {
		return errors.New("could not publish managed registry snapshot")
	}
	defer os.Remove(link)
	previous, _ := os.Readlink(filepath.Join(root, "current"))
	if err := os.Rename(link, filepath.Join(root, "current")); nil != err {
		return errors.New("could not publish managed registry snapshot")
	}
	applied = true
	if filepath.Base(previous) == previous && strings.HasPrefix(previous, "generation-") {
		_ = os.RemoveAll(filepath.Join(root, previous))
	}
	return nil
}

func (a *agent) pullConfigDir() string {
	return filepath.Join(a.registries.root, "current", "pull")
}
