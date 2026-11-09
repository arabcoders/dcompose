package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/protocol"
	"golang.org/x/crypto/bcrypt"
)

type safeNode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint"`
	PublicHost  string `json:"public_host"`
	HasToken    bool   `json:"has_token"`
	Token       string `json:"token"`
	Paused      bool   `json:"paused,omitempty"`
}

func publicNode(id string, node NodeConfig) safeNode {
	return safeNode{ID: id, Name: node.Name, URL: node.URL, Fingerprint: node.Fingerprint, PublicHost: node.PublicHost, HasToken: "" != node.token, Token: node.token, Paused: node.Paused}
}

func (s *Server) publicSettings(c Config) any {
	nodes := make([]safeNode, 0, len(c.Nodes))
	for _, id := range orderedNodeIDs(c) {
		nodes = append(nodes, publicNode(id, c.Nodes[id]))
	}
	return struct {
		Username        string     `json:"username"`
		Nodes           []safeNode `json:"nodes"`
		Version         string     `json:"version"`
		APIVersion      int        `json:"api_version"`
		AgentAPIVersion int        `json:"agent_api_version"`
	}{c.Auth.Username, nodes, s.version, protocol.ServerAPI, protocol.AgentAPI}
}

type nodeInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
	PublicHost  string `json:"public_host"`
	Paused      *bool  `json:"paused"`
}

type nodeConnectionRequest struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
	PublicHost  string `json:"public_host"`
	Paused      *bool  `json:"paused"`
}

func (input nodeInput) node(existing NodeConfig) NodeConfig {
	node := NodeConfig{Name: input.Name, URL: input.URL, Fingerprint: input.Fingerprint, PublicHost: input.PublicHost, Token: existing.Token, token: existing.token, Paused: existing.Paused}
	if nil != input.Paused {
		node.Paused = *input.Paused
	}
	if "" != input.Token {
		node.Token = input.Token
		node.token = input.Token
	}
	return node
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request, path string) {
	if "/api/settings/registries" == path {
		if "GET" == r.Method {
			c, _ := s.snapshot()
			jsonResponse(w, 200, publicRegistries(c.Registries))
			return
		}
		if "PUT" == r.Method {
			s.saveRegistries(w, r)
			return
		}
	}
	if "/api/settings/node-order" == path && "PUT" == r.Method {
		s.saveNodeOrder(w, r)
		return
	}
	if "/api/settings/nodes/pair" == path && "POST" == r.Method {
		s.pairNode(w, r)
		return
	}
	if "/api/settings/nodes/test" == path && "POST" == r.Method {
		s.testNode(w, r)
		return
	}
	if "/api/settings/account" == path && "POST" == r.Method {
		s.account(w, r)
		return
	}
	if "/api/settings" == path {
		if "GET" == r.Method {
			c, _ := s.snapshot()
			jsonResponse(w, 200, s.publicSettings(c))
			return
		}
	}
	if "/api/settings/nodes" == path && "POST" == r.Method {
		s.saveNode(w, r, "")
		return
	}
	if id, ok := strings.CutPrefix(path, "/api/settings/nodes/"); ok {
		if identity.MatchString(id) {
			if "PUT" == r.Method {
				s.saveNode(w, r, id)
				return
			}
			if "DELETE" == r.Method {
				s.deleteNode(w, r, id)
				return
			}
		}
	}
	apiError(w, 404, "Not found.")
}

// Caller holds configMu. Existing snapshots retain their clients until requests end.
func (s *Server) applyConfig(c Config) {
	nodesChanged := !maps.Equal(s.config.Nodes, c.Nodes)
	orderChanged := !slices.Equal(s.config.Global.NodeOrder, c.Global.NodeOrder)
	registrySync := registrySnapshot(c.Registries)
	registriesChanged := registrySnapshot(s.config.Registries).Revision != registrySync.Revision
	clients := s.nodes
	if nodesChanged {
		clients = make(map[string]*http.Client, len(c.Nodes))
		for id, node := range c.Nodes {
			if old, ok := s.config.Nodes[id]; ok && old == node {
				clients[id] = s.nodes[id]
			} else {
				clients[id] = nodeClient(node)
			}
		}
		for id, client := range s.nodes {
			if clients[id] != client {
				client.CloseIdleConnections()
			}
		}
	}
	s.config = c
	s.nodes = clients
	if nodesChanged || orderChanged || registriesChanged {
		s.hubMu.Lock()
		if registriesChanged {
			s.registrySync = registrySync
			s.resetImagesLocked()
			for _, channel := range s.channels {
				if nil != channel.conn {
					if err := s.syncRegistriesLocked(channel); nil != err {
						_ = channel.conn.CloseNow()
					}
				}
			}
		}
		if nodesChanged {
			s.reconcileChannelsLocked(c, clients)
		}
		if nodesChanged || orderChanged {
			s.broadcastLocked(runtimeFrame("nodes.changed", struct{}{}))
		}
		s.hubMu.Unlock()
	}
}

func (s *Server) saveNodeOrder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Nodes []string `json:"nodes"`
	}
	if !decodeBody(w, r, &input, 32<<10) {
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if nil == input.Nodes || len(input.Nodes) != len(s.config.Nodes) {
		apiError(w, 400, "Provide every current node exactly once.")
		return
	}
	seen := make(map[string]bool, len(input.Nodes))
	for _, id := range input.Nodes {
		if _, exists := s.config.Nodes[id]; !exists || seen[id] {
			apiError(w, 400, "Provide every current node exactly once.")
			return
		}
		seen[id] = true
	}
	if slices.Equal(s.config.Global.NodeOrder, input.Nodes) {
		jsonResponse(w, 200, map[string]string{"status": "ok"})
		return
	}
	c := cloneConfig(s.config)
	c.Global.NodeOrder = input.Nodes
	if err := saveConfig(c); nil != err {
		slog.Error(fmt.Sprintf("Could not save node order in server configuration %q: %v", c.path, err), "error", err)
		apiError(w, 500, "Could not save node order. Check the configuration directory's permissions.")
		return
	}
	s.applyConfig(c)
	jsonResponse(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) saveNode(w http.ResponseWriter, r *http.Request, id string) {
	var connection nodeConnectionRequest
	if !decodeBody(w, r, &connection, 32<<10) {
		return
	}
	input := nodeInput{ID: id, Name: connection.Name, URL: connection.URL, Token: connection.Token, Fingerprint: connection.Fingerprint, PublicHost: connection.PublicHost, Paused: connection.Paused}
	if "" == id {
		var identity [8]byte
		if _, err := rand.Read(identity[:]); nil != err {
			apiError(w, 500, "Could not add the node. Try again.")
			return
		}
		input.ID = hex.EncodeToString(identity[:])
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	existing, exists := s.config.Nodes[input.ID]
	if "" != id && !exists {
		apiError(w, 404, "Node not found. Choose another node.")
		return
	}
	s.writeNode(w, input, existing)
}

// Caller holds configMu.
func (s *Server) writeNode(w http.ResponseWriter, input nodeInput, existing NodeConfig) {
	if input.Token == existing.token {
		input.Token = ""
	}
	c := cloneConfig(s.config)
	node, err := validateNode(input.ID, input.node(existing), c.path)
	if nil != err {
		apiError(w, 400, err.Error())
		return
	}
	if _, exists := c.Nodes[input.ID]; !exists && 0 < len(c.Global.NodeOrder) {
		c.Global.NodeOrder = append(c.Global.NodeOrder, input.ID)
	}
	c.Nodes[input.ID] = node
	c, err = validateResolvedConfig(c)
	if nil != err {
		apiError(w, 400, err.Error())
		return
	}
	secretPath := ""
	if "" != input.Token {
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); nil != err {
			apiError(w, 500, "Could not save the node token. Check the configuration directory's permissions.")
			return
		}
		node.Token = "file:secrets/node-" + input.ID + "-" + hex.EncodeToString(suffix[:]) + ".token"
		secretPath = configutil.Path(c.path, strings.TrimPrefix(node.Token, "file:"))
		if err := configutil.Write(secretPath, []byte(input.Token)); nil != err {
			apiError(w, 500, "Could not save the node token. Check the configuration directory's permissions.")
			return
		}
		c.Nodes[input.ID] = node
	}
	if err := saveConfig(c); nil != err {
		if "" != secretPath {
			_ = os.Remove(secretPath)
		}
		apiError(w, 500, "Could not save the node. Check the configuration directory's permissions.")
		return
	}
	s.applyConfig(c)
	slog.Info(fmt.Sprintf("Saved configuration for %s.", nodeLogLabel(node)), "node", nodeLogLabel(node))
	jsonResponse(w, 200, publicNode(input.ID, node))
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request, id string) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	c := cloneConfig(s.config)
	node, ok := c.Nodes[id]
	if !ok {
		apiError(w, 404, "Node not found. Choose another node.")
		return
	}
	if node.Paused {
		apiError(w, http.StatusConflict, errNodePaused.Error())
		return
	}
	delete(c.Nodes, id)
	c.Global.NodeOrder = slices.DeleteFunc(c.Global.NodeOrder, func(nodeID string) bool { return id == nodeID })
	if err := saveConfig(c); nil != err {
		apiError(w, 500, "Could not delete the node. Check the configuration directory's permissions.")
		return
	}
	result := struct {
		Status  string `json:"status"`
		Warning string `json:"warning,omitempty"`
	}{Status: "ok"}
	// A failed local save must not reset a node that remains configured.
	if err := resetNodePairing(r.Context(), node, s.nodes[id]); nil != err {
		detail := strings.TrimSuffix(strings.TrimSpace(safeTransportError(err)), ".")
		result.Warning = "Node was removed, but pairing could not be reset."
		slog.Warn(fmt.Sprintf("Removed %s from server configuration, but could not reset its pairing: %s.", nodeLogLabel(node), detail), "node", nodeLogLabel(node), "error", detail)
	} else {
		slog.Info(fmt.Sprintf("Disconnected %s and reset its pairing.", nodeLogLabel(node)), "node", nodeLogLabel(node))
	}
	s.applyConfig(c)
	jsonResponse(w, 200, result)
}

func (s *Server) testNode(w http.ResponseWriter, r *http.Request) {
	var input nodeInput
	if !decodeBody(w, r, &input, 32<<10) {
		return
	}
	c, _ := s.snapshot()
	if c.Nodes[input.ID].Paused {
		apiError(w, http.StatusConflict, errNodePaused.Error())
		return
	}
	id := input.ID
	if "" == id {
		id = "test"
	}
	node, err := validateNode(id, input.node(c.Nodes[input.ID]), c.path)
	if nil != err {
		apiError(w, 400, err.Error())
		return
	}
	if node.Paused {
		apiError(w, http.StatusConflict, errNodePaused.Error())
		return
	}
	client := nodeClient(node)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	health, err := checkNode(ctx, node, client)
	if nil != err {
		detail := safeTransportError(err)
		slog.Error(fmt.Sprintf("Connection test for %s failed: %s", nodeLogLabel(node), detail), "node", nodeLogLabel(node), "error", detail)
		if incompatible, ok := errors.AsType[incompatibleAgent](err); ok {
			apiError(w, 426, incompatible.Error())
		} else {
			apiError(w, 502, "Cannot connect to the node. Check its token, address and public-key fingerprint.")
		}
		return
	}
	jsonResponse(w, 200, health)
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		CurrentPassword string `json:"current_password"`
	}
	if !decodeBody(w, r, &input, 16<<10) {
		return
	}
	if "" == strings.TrimSpace(input.Username) || 72 < len(input.Password) {
		apiError(w, 400, "Provide a username. Leave the password blank to keep it, or use at most 72 bytes.")
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if err := bcrypt.CompareHashAndPassword([]byte(s.config.Auth.passwordHash), []byte(input.CurrentPassword)); nil != err {
		apiError(w, 401, "Current password is incorrect.")
		return
	}
	c, err := changeCredentials(cloneConfig(s.config), input.Username, input.Password)
	if nil != err {
		apiError(w, 500, "Could not save account details. Check the configuration directory's permissions.")
		return
	}
	s.applyConfig(c)
	if s.createSession(w, r, c, c.Global.SecureCookie) {
		jsonResponse(w, 200, map[string]string{"status": "ok"})
	}
}
