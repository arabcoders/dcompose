package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
)

var errFingerprint = errors.New("public-key fingerprint mismatch")
var errNodeHealth = errors.New("invalid node health response")
var errNodePaused = errors.New("Node is paused. Resume it in node settings before using it.")

type nodeHTTPError struct {
	operation string
	status    int
	message   string
}

func (err nodeHTTPError) Error() string {
	if "" != err.message {
		return fmt.Sprintf("%s HTTP status %d: %s", err.operation, err.status, err.message)
	}
	return fmt.Sprintf("%s HTTP status %d", err.operation, err.status)
}

func readNodeHTTPError(response *http.Response, operation string) nodeHTTPError {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body)
	return nodeHTTPError{operation: operation, status: response.StatusCode, message: body.Error}
}

func nodeClient(node NodeConfig) *http.Client {
	pin, _ := base64.StdEncoding.DecodeString(node.Fingerprint)
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConnsPerHost:   8,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// A pinned SPKI authenticates self-signed agents in place of CA/hostname validation.
			InsecureSkipVerify: true,
			VerifyConnection: func(state tls.ConnectionState) error {
				if 0 == len(state.PeerCertificates) {
					return errors.New("missing peer certificate")
				}
				digest := sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
				if 1 != subtle.ConstantTimeCompare(pin, digest[:]) {
					return errFingerprint
				}
				return nil
			},
		},
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s *Server) connect(ctx context.Context, nodeID, method, path string, body io.Reader) (*http.Response, error) {
	config, clients := s.snapshot()
	node, ok := config.Nodes[nodeID]
	if !ok {
		return nil, errors.New("node removed")
	}
	return nodeRequest(ctx, node, clients[nodeID], method, path, body)
}

func nodeRequest(ctx context.Context, node NodeConfig, client *http.Client, method, path string, body io.Reader) (*http.Response, error) {
	if node.Paused {
		return nil, errNodePaused
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(node.URL, "/")+path, body)
	if nil != err {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+node.token)
	r.Header.Set(protocol.Header, strconv.Itoa(protocol.AgentAPI))
	r.Header.Set("Accept", "application/json")
	if nil != body {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(r)
	if nil != err {
		return nil, err
	}
	if http.StatusUpgradeRequired == response.StatusCode {
		_ = response.Body.Close()
		return nil, incompatibleAgent{}
	}
	return response, nil
}

type nodeHealth struct {
	Version    string `json:"version"`
	APIVersion int    `json:"api_version"`
}

type incompatibleAgent struct{}

func (incompatibleAgent) Error() string {
	return "Node version is incompatible. Update the node and server."
}

func checkNode(ctx context.Context, node NodeConfig, client *http.Client) (nodeHealth, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var health nodeHealth
	response, err := nodeRequest(ctx, node, client, "GET", "/health", nil)
	if nil != err {
		return health, err
	}
	defer func() { _ = response.Body.Close() }()
	if 200 != response.StatusCode {
		return health, readNodeHTTPError(response, "node health check")
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&health); nil != err {
		return health, errNodeHealth
	}
	if err := decoder.Decode(new(any)); io.EOF != err {
		return health, errNodeHealth
	}
	if protocol.AgentAPI != health.APIVersion {
		return health, incompatibleAgent{}
	}
	return health, nil
}

func (s *Server) proxy(w http.ResponseWriter, r *http.Request, route string) {
	parts := strings.Split(route, "/")
	if 2 > len(parts) || !identity.MatchString(parts[0]) {
		apiError(w, 404, "Not found.")
		return
	}
	nodeID := parts[0]
	config, _ := s.snapshot()
	node, ok := config.Nodes[nodeID]
	if !ok {
		apiError(w, 404, "Node not found. Choose another node.")
		return
	}
	if node.Paused {
		apiError(w, http.StatusConflict, errNodePaused.Error())
		return
	}
	path := "/" + strings.Join(parts[1:], "/")
	statusOnly, safeMessage, valid := allowedRoute(r.Method, parts[1:])
	if !valid {
		apiError(w, 404, "Not found.")
		return
	}
	var body io.Reader
	var requestBody []byte
	if "POST" == r.Method || "PUT" == r.Method {
		payload, ok := proxyBody(w, r, parts[1:])
		if !ok {
			return
		}
		body = bytes.NewReader(payload)
		requestBody = payload
	}
	operation := proxyOperation(r.Method, parts[1:], requestBody)
	label := nodeLogLabel(node)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if "POST" == r.Method && ("/images/check" == path || strings.HasSuffix(path, "/images/check")) {
		s.requestImageChecks(w, r, nodeID, imageStackRoute(path), requestBody)
		return
	}
	if "POST" == r.Method && strings.HasSuffix(path, "/actions") {
		var input struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(requestBody, &input)
		if "deploy" == input.Action || "start" == input.Action || "pull" == input.Action || "update" == input.Action {
			if err := s.awaitRegistries(ctx, nodeID); nil != err {
				if errors.Is(err, errNodePaused) {
					proxyError(w, err)
					return
				}
				slog.Error(
					fmt.Sprintf("Could not %s on %s: %v", operation, label, err),
					"node",
					label,
					"operation",
					operation,
					"status",
					http.StatusServiceUnavailable,
					"error",
					err,
				)
				apiError(
					w,
					http.StatusServiceUnavailable,
					"Registry credentials are not synchronized with this node. Check registry settings or reconnect the node.",
				)
				return
			}
		}
	}
	response, err := s.connect(ctx, nodeID, r.Method, path, body)
	if nil != err {
		if errors.Is(err, errNodePaused) {
			proxyError(w, err)
			return
		}
		detail := safeTransportError(err)
		slog.Error(fmt.Sprintf("Could not %s on %s: %s", operation, label, detail), "node", label, "operation", operation, "error", detail)
		proxyError(w, err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	var payload map[string]json.RawMessage
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<20))
	if err := decoder.Decode(&payload); nil != err || nil == payload {
		detail := "expected a JSON object"
		if nil != err {
			detail = safeTransportError(err)
		}
		slog.Error(
			fmt.Sprintf("Could not %s on %s: invalid JSON response (HTTP %d): %s", operation, label, response.StatusCode, detail),
			"node",
			label,
			"operation",
			operation,
			"status",
			response.StatusCode,
			"error",
			detail,
		)
		apiError(w, 502, "Could not load data from this node. Try again.")
		return
	}
	if err := decoder.Decode(new(any)); io.EOF != err {
		detail := "expected only one JSON object"
		if nil != err {
			detail = safeTransportError(err)
		}
		slog.Error(
			fmt.Sprintf("Could not %s on %s: extra or invalid response data (HTTP %d): %s", operation, label, response.StatusCode, detail),
			"node",
			label,
			"operation",
			operation,
			"status",
			response.StatusCode,
		)
		apiError(w, 502, "Could not load data from this node. Try again.")
		return
	}
	if 200 > response.StatusCode || 300 <= response.StatusCode {
		var detail string
		_ = json.Unmarshal(payload["error"], &detail)
		if "" == detail {
			detail = http.StatusText(response.StatusCode)
		}
		slog.Error(
			fmt.Sprintf("Could not %s on %s (HTTP %d): %s", operation, label, response.StatusCode, detail),
			"node",
			label,
			"operation",
			operation,
			"status",
			response.StatusCode,
			"error",
			detail,
		)
	}
	if 200 > response.StatusCode || 300 <= response.StatusCode {
		message := safeMessage
		if "" == message {
			message = "Could not complete the task on this node. Try again."
		}
		switch response.StatusCode {
		case http.StatusUnauthorized:
			message = "Node authentication failed. Check its token in node settings."
		case http.StatusForbidden:
			message = "Node rejected this request. Check its allowed IP settings."
		case http.StatusBadRequest:
			if "PUT" == r.Method && 3 == len(parts) && "stacks" == parts[1] {
				message = "Compose validation failed. Check the Compose and .env files."
			}
		case http.StatusConflict:
			if "POST" == r.Method && 3 == len(parts) && "stacks" == parts[1] {
				message = "The destination stack already exists or is busy."
			}
		}
		status := response.StatusCode
		if 400 > status {
			status = 502
		}
		apiError(w, status, message)
		return
	}
	if statusOnly {
		payload = onlyFields(payload, "name", "status", "services", "busy")
	}
	if stack := imageStackRoute(path); "" != stack {
		if "GET" == r.Method && (path == "/stacks/"+stack || strings.HasSuffix(path, "/status")) {
			s.hubMu.Lock()
			s.imageRuntimeLocked(nodeID, payload)
			s.hubMu.Unlock()
		} else if http.StatusAccepted == response.StatusCode && ("POST" == r.Method || "DELETE" == r.Method) {
			var status, relay string
			_ = json.Unmarshal(payload["status"], &status)
			_ = json.Unmarshal(payload["relay"], &relay)
			if "accepted" != status || ("" != relay && !identity.MatchString(relay)) {
				slog.Error(
					fmt.Sprintf("Could not %s on %s: invalid action acceptance response", operation, label),
					"node",
					label,
					"operation",
					operation,
				)
				apiError(w, http.StatusBadGateway, "Could not run the stack action. Check the node logs.")
				return
			}
			if "" != relay {
				s.startRelay(nodeID, relay)
			}
			jsonResponse(w, http.StatusAccepted, map[string]string{"status": "accepted"})
			return
		}
	}
	jsonResponse(w, response.StatusCode, payload)
}

func proxyOperation(method string, parts []string, body []byte) string {
	switch parts[0] {
	case "health":
		return "check node health"
	case "info":
		return "load node information"
	case "shells":
		return fmt.Sprintf("close shell %q", parts[1])
	case "images":
		return "check image updates"
	case "stacks":
		if 1 == len(parts) {
			return "list stacks"
		}
		stack := fmt.Sprintf("stack %q", parts[1])
		if 2 == len(parts) {
			switch method {
			case "POST":
				return "create " + stack
			case "PUT":
				return "save " + stack
			case "DELETE":
				return "delete " + stack
			default:
				return "load " + stack
			}
		}
		var input struct {
			Action    string `json:"action"`
			Service   string `json:"service"`
			Container string `json:"container"`
			Shell     string `json:"shell"`
		}
		_ = json.Unmarshal(body, &input)
		switch parts[2] {
		case "actions":
			if "" != input.Service {
				return fmt.Sprintf("%s service %q in %s", input.Action, input.Service, stack)
			}
			return input.Action + " " + stack
		case "shells":
			return fmt.Sprintf("open a %s shell in container %q for %s", input.Shell, input.Container, stack)
		case "images":
			if "" != input.Container {
				return fmt.Sprintf("check image updates for container %q in %s", input.Container, stack)
			}
			return "check image updates for " + stack
		default:
			return "load status for " + stack
		}
	}
	return "complete the node request"
}

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func allowedRoute(method string, p []string) (statusOnly bool, safeMessage string, valid bool) {
	if 1 == len(p) {
		switch p[0] {
		case "health":
			valid = "GET" == method
			safeMessage = "Could not check node health. Try again."
		case "stacks":
			valid = "GET" == method
			safeMessage = "Could not load stacks. Try again."
		case "info":
			valid = "GET" == method
			safeMessage = "Could not load node information. Try again."
		}
		return
	}
	if 2 > len(p) {
		return
	}
	switch p[0] {
	case "images":
		valid = "POST" == method && 2 == len(p) && "check" == p[1]
		safeMessage = "Could not check updates. Try again."
	case "stacks":
		if !stackName.MatchString(p[1]) {
			return
		}
		if 2 == len(p) {
			valid = "GET" == method || "POST" == method || "PUT" == method || "DELETE" == method
			switch method {
			case "POST":
				safeMessage = "Could not create the stack. Try again."
			case "PUT":
				safeMessage = "Could not save the stack. Try again."
			case "DELETE":
				safeMessage = "Could not delete the stack. Try again."
			default:
				safeMessage = "Could not load the stack. Try again."
			}
			return
		}
		if 3 == len(p) {
			switch p[2] {
			case "status":
				valid = "GET" == method
				statusOnly = true
				safeMessage = "Could not load stack status. Try again."
			case "actions":
				valid = "POST" == method
				safeMessage = "Could not run the stack action. Try again."
			case "shells":
				valid = "POST" == method
				safeMessage = "Could not open the shell. Try again."
			}
		} else if 4 == len(p) && "images" == p[2] && "check" == p[3] {
			valid = "POST" == method
			safeMessage = "Could not check updates. Try again."
		}
	case "shells":
		if !identity.MatchString(p[1]) {
			return
		}
		if 2 == len(p) {
			valid = "DELETE" == method
			safeMessage = "Could not close the shell. Try again."
			return
		}
	}
	return
}

func proxyBody(w http.ResponseWriter, r *http.Request, p []string) ([]byte, bool) {
	var payload any
	switch {
	case 2 == len(p) && "images" == p[0] && "check" == p[1]:
		input := &struct{}{}
		if 0 != r.ContentLength && !decodeBody(w, r, &input, 16<<10) {
			return nil, false
		}
		if nil == input {
			apiError(w, 400, "Invalid JSON request.")
			return nil, false
		}
		payload = input
	case 4 == len(p) && "stacks" == p[0] && "images" == p[2] && "check" == p[3]:
		input := &struct {
			Container json.RawMessage `json:"container"`
		}{}
		if 0 != r.ContentLength && !decodeBody(w, r, &input, 16<<10) {
			return nil, false
		}
		if nil == input {
			apiError(w, 400, "Invalid JSON request.")
			return nil, false
		}
		request := struct {
			Container *string `json:"container,omitempty"`
		}{}
		if 0 != len(input.Container) {
			if err := json.Unmarshal(
				input.Container,
				&request.Container,
			); nil != err || nil == request.Container ||
				"" == strings.TrimSpace(*request.Container) {
				apiError(w, 400, "Specify a nonempty container name.")
				return nil, false
			}
		}
		payload = request
	case 2 == len(p) && "stacks" == p[0] && "POST" == r.Method:
		input := struct {
			Compose   string `json:"compose"`
			Env       string `json:"env"`
			Overwrite bool   `json:"overwrite"`
		}{}
		if !decodeBody(w, r, &input, 4<<20) {
			return nil, false
		}
		if "" == strings.TrimSpace(input.Compose) {
			apiError(w, 400, "Compose content is required.")
			return nil, false
		}
		payload = input
	case 2 == len(p) && "stacks" == p[0]:
		input := struct {
			Compose string `json:"compose"`
			Env     string `json:"env"`
		}{}
		if !decodeBody(w, r, &input, 4<<20) {
			return nil, false
		}
		if "" == strings.TrimSpace(input.Compose) {
			apiError(w, 400, "Compose content is required.")
			return nil, false
		}
		payload = input
	case 3 == len(p) && "actions" == p[2]:
		input := struct {
			Action  string `json:"action"`
			Service string `json:"service,omitempty"`
		}{}
		if !decodeBody(w, r, &input, 16<<10) {
			return nil, false
		}
		switch input.Action {
		case "deploy", "start", "stop", "restart", "pull", "update", "down":
		default:
			apiError(w, 400, "Invalid stack action.")
			return nil, false
		}
		if "" != input.Service &&
			(!serviceName.MatchString(input.Service) || ("start" != input.Action && "stop" != input.Action && "restart" != input.Action && "update" != input.Action)) {
			apiError(w, 400, "Use a valid service name with start, stop, restart or update only.")
			return nil, false
		}
		payload = input
	case 3 == len(p) && "stacks" == p[0] && "shells" == p[2]:
		input := struct {
			Container string `json:"container"`
			Shell     string `json:"shell"`
			Cols      int    `json:"cols"`
			Rows      int    `json:"rows"`
		}{Shell: "sh", Cols: 120, Rows: 30}
		if !decodeBody(w, r, &input, 16<<10) {
			return nil, false
		}
		if "" == strings.TrimSpace(input.Container) ||
			("sh" != input.Shell && "bash" != input.Shell && "ash" != input.Shell && "dash" != input.Shell) ||
			!validSize(input.Cols, input.Rows) {
			apiError(w, 400, "Specify a container, an allowed shell (sh, bash, ash or dash), and 1-500 columns and rows.")
			return nil, false
		}
		payload = input
	default:
		return nil, true
	}
	body, err := json.Marshal(payload)
	if nil != err {
		apiError(w, 400, "Invalid request.")
		return nil, false
	}
	return body, true
}

func validSize(cols, rows int) bool { return 1 <= cols && 500 >= cols && 1 <= rows && 500 >= rows }

func onlyFields(payload map[string]json.RawMessage, fields ...string) map[string]json.RawMessage {
	filtered := make(map[string]json.RawMessage)
	for _, field := range fields {
		if value, ok := payload[field]; ok {
			filtered[field] = value
		}
	}
	return filtered
}

func proxyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNodePaused) {
		apiError(w, http.StatusConflict, errNodePaused.Error())
		return
	}
	if incompatible, ok := errors.AsType[incompatibleAgent](err); ok {
		apiError(w, http.StatusUpgradeRequired, incompatible.Error())
		return
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		apiError(w, 504, "The node took too long to respond. Try again.")
		return
	}
	apiError(w, 502, "Cannot connect to the node. Check its address, network connection and public-key fingerprint.")
}
