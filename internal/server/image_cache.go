package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/engine"
)

type imageCooldownSnapshot struct {
	Cooldowns []engine.RegistryCooldown `json:"cooldowns"`
}

func (s *Server) loadImageCooldowns() {
	if "" == s.config.path {
		return
	}
	s.images.cooldownFile = filepath.Join(filepath.Dir(s.config.path), "cache", "registry-cooldowns.json")
	data, err := os.ReadFile(s.images.cooldownFile)
	if os.IsNotExist(err) {
		return
	}
	var snapshot imageCooldownSnapshot
	if nil == err {
		err = json.Unmarshal(data, &snapshot)
	}
	if nil != err {
		slog.Warn(fmt.Sprintf("Could not restore registry metadata cooldowns from %q: %v", s.images.cooldownFile, err))
		return
	}
	for _, cooldown := range snapshot.Cooldowns {
		s.registryClient.ApplyCooldown(cooldown)
	}
	s.images.persisted = data
}

func (s *Server) saveImageCooldowns() {
	c := s.images
	c.persistMu.Lock()
	defer c.persistMu.Unlock()
	if "" == c.cooldownFile {
		return
	}
	data, err := json.Marshal(imageCooldownSnapshot{Cooldowns: s.registryClient.Cooldowns()})
	if nil != err || bytes.Equal(data, c.persisted) {
		return
	}
	if err := configutil.Write(c.cooldownFile, data); nil != err {
		slog.Warn(fmt.Sprintf("Could not save registry metadata cooldowns to %q: %v. Limits remain active in memory.", c.cooldownFile, err))
		return
	}
	c.persisted = data
}
