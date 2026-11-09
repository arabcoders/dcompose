package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/yeka/zip"
)

func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Nodes    []string `json:"nodes"`
		Password string   `json:"password"`
	}
	if !decodeBody(w, r, &input, 16<<10) {
		return
	}
	if 8 > utf8.RuneCountInString(input.Password) {
		apiError(w, http.StatusBadRequest, "Use a backup password of at least 8 characters.")
		return
	}
	if 0 == len(input.Nodes) {
		apiError(w, http.StatusBadRequest, "Choose at least one node.")
		return
	}
	config, clients := s.snapshot()
	selected := make(map[string]bool, len(input.Nodes))
	for _, id := range input.Nodes {
		if _, ok := config.Nodes[id]; !ok || selected[id] {
			apiError(w, http.StatusBadRequest, "Choose valid, distinct nodes.")
			return
		}
		if config.Nodes[id].Paused {
			apiError(w, http.StatusConflict, errNodePaused.Error())
			return
		}
		selected[id] = true
	}
	data, err := backupConfig(config)
	if nil != err {
		apiError(w, http.StatusInternalServerError, "Could not export DCompose configuration.")
		return
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	add := func(name string, data []byte) error {
		file, err := archive.Encrypt(name, input.Password, zip.AES256Encryption)
		if nil != err {
			return err
		}
		_, err = file.Write(data)
		return err
	}
	if err := add("dcompose.toml", data); nil != err {
		apiError(w, http.StatusInternalServerError, "Could not create backup archive.")
		return
	}
	for _, id := range input.Nodes {
		node := config.Nodes[id]
		backup, err := collectNodeBackup(r.Context(), node, clients[id])
		if nil != err {
			var upstream nodeHTTPError
			_ = errors.As(err, &upstream)
			detail := safeTransportError(err)
			slog.Error(fmt.Sprintf("Could not back up %s: %s", nodeLogLabel(node), detail), "node", nodeLogLabel(node), "operation", "backup", "status", upstream.status, "error", detail)
			apiError(w, http.StatusBadGateway, fmt.Sprintf("Could not back up node %s. Try again.", node.Name))
			return
		}
		for _, file := range backup.Files {
			if !fs.ValidPath(file.Path) {
				slog.Error(fmt.Sprintf("Could not back up %s: the node returned an invalid relative file path.", nodeLogLabel(node)), "node", nodeLogLabel(node), "operation", "backup")
				apiError(w, http.StatusBadGateway, fmt.Sprintf("Could not back up node %s. Check the server logs.", node.Name))
				return
			}
			if err := add("nodes/"+id+"/"+file.Path, file.Data); nil != err {
				apiError(w, http.StatusInternalServerError, "Could not create backup archive.")
				return
			}
		}
	}
	if err := archive.Close(); nil != err {
		apiError(w, http.StatusInternalServerError, "Could not create backup archive.")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="dcompose-backup.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buffer.Bytes())
}

func backupConfig(c Config) ([]byte, error) {
	c = cloneConfig(c)
	c.Auth.Password = c.Auth.passwordHash
	c.Auth.SessionKey = hex.EncodeToString([]byte(c.Auth.sessionKey))
	c.Auth.APIKey = c.Auth.apiKey
	if nil != c.Auth.OIDC {
		oidc := *c.Auth.OIDC
		oidc.ClientSecret = oidc.clientSecret
		c.Auth.OIDC = &oidc
	}
	for id, node := range c.Nodes {
		node.Token = node.token
		c.Nodes[id] = node
	}
	for i := range c.Registries {
		c.Registries[i].Auth = c.Registries[i].auth
		c.Registries[i].AuthFile = ""
	}
	return encodeConfig(c)
}

func collectNodeBackup(ctx context.Context, node NodeConfig, client *http.Client) (protocol.NodeBackup, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var backup protocol.NodeBackup
	response, err := nodeRequest(ctx, node, client, http.MethodGet, "/backup", nil)
	if nil != err {
		return backup, err
	}
	defer func() { _ = response.Body.Close() }()
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<20))
	if err := decoder.Decode(&backup); nil != err {
		return backup, nodeHTTPError{operation: "read backup files", status: response.StatusCode, message: err.Error()}
	}
	if err := decoder.Decode(new(any)); io.EOF != err {
		return backup, nodeHTTPError{operation: "read backup files", status: response.StatusCode, message: fmt.Sprintf("unexpected trailing data: %v", err)}
	}
	if http.StatusOK != response.StatusCode {
		return backup, nodeHTTPError{operation: "backup", status: response.StatusCode, message: backup.Error}
	}
	if nil == backup.Files {
		return backup, nodeHTTPError{operation: "backup", status: response.StatusCode, message: "node did not return backup files"}
	}
	return backup, nil
}
