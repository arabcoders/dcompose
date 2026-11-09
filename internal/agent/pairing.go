package agent

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func (a *agent) unpair(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	a.mu.Lock()
	defer a.mu.Unlock()
	_, fingerprint, err := connectionInfo(a.config, a.config.path)
	if nil != err {
		return fmt.Errorf("read node connection information: %w", err)
	}
	if err := saveGlobalSetting(a.config.path, "paired", false); nil != err {
		return fmt.Errorf("reset global.paired: %w. Check that agent.toml is writable", err)
	}
	a.config.Global.Paired = false
	if "" == a.pairingToken {
		a.pairingToken = rand.Text()
	}
	slog.Info(
		fmt.Sprintf(
			"Reset pairing for node %q at the request of DCompose peer %s. The node is ready to pair again.",
			a.config.Global.NodeID,
			r.RemoteAddr,
		),
		"node",
		a.config.Global.NodeID,
		"peer",
		r.RemoteAddr,
	)
	// Keep the pairing token local, outside forwarded node logs.
	fmt.Fprintf(os.Stderr, "TLS fingerprint: %s\n", fingerprint)
	fmt.Fprintf(os.Stderr, "Use this single-use pairing token in Nodes > Add node: %s\n", a.pairingToken)
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
	return nil
}

func (a *agent) pair(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	if supplied := r.Header.Values(
		protocol.Header,
	); 0 != len(supplied) &&
		(1 != len(supplied) || strconv.Itoa(protocol.AgentAPI) != supplied[0]) {
		return fault(http.StatusUpgradeRequired, "Node version is incompatible. Update the node and server.")
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeBody(r, &input); nil != err {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if "" == a.pairingToken {
		return fault(http.StatusConflict, "Node is already paired. Reset pairing on the node before trying again.")
	}
	if 1 != subtle.ConstantTimeCompare([]byte(input.Token), []byte(a.pairingToken)) {
		return fault(http.StatusUnauthorized, "invalid pairing token. Copy the current token from the node's startup logs.")
	}
	token, fingerprint, err := connectionInfo(a.config, a.config.path)
	if nil != err {
		return fmt.Errorf("read node credentials: %w", err)
	}
	name, err := os.Hostname()
	if nil != err {
		return fmt.Errorf("read node hostname: %w", err)
	}
	if err := saveGlobalSetting(a.config.path, "paired", true); nil != err {
		return fmt.Errorf("save global.paired: %w. Check that agent.toml is writable", err)
	}
	a.config.Global.Paired = true
	a.pairingToken = ""
	slog.Info(
		fmt.Sprintf("Paired node %q (%s) with DCompose peer %s.", name, a.config.Global.NodeID, r.RemoteAddr),
		"name",
		name,
		"node",
		a.config.Global.NodeID,
		"peer",
		r.RemoteAddr,
	)
	respond(w, http.StatusOK, protocol.PairingResponse{ID: a.config.Global.NodeID, Name: name, Token: token, Fingerprint: fingerprint})
	return nil
}
