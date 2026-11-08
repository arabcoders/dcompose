package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func resetNodePairing(ctx context.Context, node NodeConfig, client *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := nodeRequest(ctx, node, client, http.MethodPost, "/unpair", nil)
	if nil != err {
		return err
	}
	defer response.Body.Close()
	if http.StatusOK != response.StatusCode {
		return readNodeHTTPError(response, "reset pairing")
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); nil != err {
		return fmt.Errorf("invalid pairing reset response: %w", err)
	}
	if "ok" != result.Status {
		return errors.New("node did not confirm the pairing reset")
	}
	return nil
}

func pairingURL(address string) (string, error) {
	address = strings.TrimSpace(address)
	if !strings.Contains(address, "://") {
		if _, port, err := net.SplitHostPort(address); nil != err || "" == port {
			return "", fmt.Errorf("node address must be host:port, IP:port, or an HTTPS URL")
		}
		address = "https://" + address
	}
	u, err := url.Parse(address)
	if nil != err || "https" != u.Scheme || "" == u.Hostname() || nil != u.User || "" != u.RawQuery || u.ForceQuery || "" != u.Fragment {
		return "", fmt.Errorf("node address must use HTTPS without credentials, query, or fragment")
	}
	if "" != u.Port() {
		port, err := strconv.Atoi(u.Port())
		if nil != err || 1 > port || 65535 < port {
			return "", fmt.Errorf("node port must be between 1 and 65535")
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (s *Server) pairNode(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name         string `json:"name"`
		URL          string `json:"url"`
		PairingToken string `json:"pairing_token"`
		Fingerprint  string `json:"fingerprint"`
	}
	if !decodeBody(w, r, &input, 32<<10) {
		return
	}
	address, err := pairingURL(input.URL)
	if nil != err {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	input.PairingToken = strings.TrimSpace(input.PairingToken)
	if "" == input.PairingToken {
		apiError(w, http.StatusBadRequest, "Copy the pairing token from the node startup logs.")
		return
	}
	input.Fingerprint = strings.TrimSpace(input.Fingerprint)
	if "" != input.Fingerprint {
		pin, err := base64.StdEncoding.DecodeString(input.Fingerprint)
		if nil != err || 32 != len(pin) {
			apiError(w, http.StatusBadRequest, "Enter a Base64-encoded SHA-256 public-key fingerprint.")
			return
		}
	}
	data, err := json.Marshal(struct {
		Token string `json:"token"`
	}{input.PairingToken})
	if nil != err {
		apiError(w, http.StatusInternalServerError, "Could not pair with the node. Try again.")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, address+"/pair", bytes.NewReader(data))
	if nil != err {
		apiError(w, http.StatusBadRequest, "Invalid node address.")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(protocol.Header, strconv.Itoa(protocol.AgentAPI))
	var client *http.Client
	if "" != input.Fingerprint {
		client = nodeClient(NodeConfig{Fingerprint: input.Fingerprint})
	} else {
		transport := &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// Without a supplied pin, pairing obtains it for subsequent connections.
			InsecureSkipVerify: true,
		}}
		client = &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	client.Timeout = 15 * time.Second
	defer client.CloseIdleConnections()
	label := nodeLogLabel(NodeConfig{Name: input.Name, URL: address})
	response, err := client.Do(request)
	if nil != err {
		detail := safeTransportError(err)
		slog.Error(fmt.Sprintf("Could not pair with %s: %s", label, detail), "node", label, "operation", "pair", "error", detail)
		if errors.Is(err, errFingerprint) {
			apiError(w, http.StatusBadGateway, "Node TLS fingerprint does not match. Copy the fingerprint from the node console and try again.")
			return
		}
		apiError(w, http.StatusBadGateway, "Could not pair with the node. Check its address and network connection.")
		return
	}
	defer response.Body.Close()
	if http.StatusOK != response.StatusCode {
		err := readNodeHTTPError(response, "pair")
		slog.Error(fmt.Sprintf("Could not pair with %s: %s", label, err.Error()), "node", label, "operation", "pair", "status", response.StatusCode, "error", err.Error())
		message := "Could not pair with the node. Try again."
		switch response.StatusCode {
		case http.StatusUnauthorized:
			message = "Invalid pairing token. Copy the current token from the node's startup logs."
		case http.StatusForbidden:
			message = "Node rejected pairing. Check the node or proxy access rules."
		case http.StatusConflict:
			message = "Node is already paired. Reset pairing on the node before trying again."
		case http.StatusUpgradeRequired:
			message = incompatibleAgent{}.Error()
		}
		apiError(w, http.StatusBadGateway, message)
		return
	}
	var paired protocol.PairingResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&paired); nil != err {
		slog.Error(fmt.Sprintf("Could not pair with %s: invalid JSON response (HTTP %d): %v", label, response.StatusCode, err), "node", label, "operation", "pair", "status", response.StatusCode, "error", err)
		apiError(w, http.StatusBadGateway, "Could not pair with the node. Try again.")
		return
	}
	if "" == paired.Token {
		apiError(w, http.StatusBadGateway, "Node returned an invalid pairing response.")
		return
	}
	if "" != input.Fingerprint {
		paired.Fingerprint = input.Fingerprint
	}
	name := strings.TrimSpace(input.Name)
	if "" == name {
		name = paired.Name
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if _, exists := s.config.Nodes[paired.ID]; exists {
		apiError(w, http.StatusConflict, "This node ID is already configured. Disconnect it before pairing again.")
		return
	}
	s.writeNode(w, nodeInput{ID: paired.ID, Name: name, URL: address, Token: paired.Token, Fingerprint: paired.Fingerprint}, NodeConfig{})
}
