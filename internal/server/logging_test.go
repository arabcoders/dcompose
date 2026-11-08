package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

type logTransport func(*http.Request) (*http.Response, error)

func (transport logTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func TestProxyLogs(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		body     string
		err      error
		detail   string
		method   string
		path     string
		request  string
		resource string
	}{
		{name: "transport", err: &url.Error{Op: "Get", URL: "https://user:secret@node.invalid?token=secret", Err: errors.New("connection refused")}, detail: "connection refused"},
		{name: "rejection", status: 403, body: `{"error":"IP 192.0.2.15 is not allowed. Allowed IPs: 192.0.2.20/32."}`, detail: "IP 192.0.2.15 is not allowed. Allowed IPs: 192.0.2.20/32."},
		{name: "health", status: 500, path: "/health"},
		{name: "stacks", status: 500, path: "/stacks"},
		{name: "stack-auth", status: 401, path: "/stacks/demo", resource: "demo"},
		{name: "stack-load", status: 404, path: "/stacks/demo", resource: "demo"},
		{name: "stack-save", status: 500, method: "PUT", path: "/stacks/demo", request: `{"compose":"services: {}"}`, resource: "demo"},
		{name: "stack-create", status: 500, method: "POST", path: "/stacks/demo", request: `{"compose":"services: {}","env":""}`, resource: "demo"},
		{name: "stack-create-conflict", status: 409, method: "POST", path: "/stacks/demo", request: `{"compose":"services: {}","env":""}`, resource: "demo"},
		{name: "compose-validation", status: 400, method: "PUT", path: "/stacks/demo", request: `{"compose":"services: {}"}`, resource: "demo"},
		{name: "stack-delete", status: 500, method: "DELETE", path: "/stacks/demo", resource: "demo"},
		{name: "stack-status", status: 403, path: "/stacks/demo/status", resource: "demo"},
		{name: "stack-action", status: 400, method: "POST", path: "/stacks/demo/actions", request: `{"action":"deploy"}`, resource: "demo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if "" == test.method {
				test.method = "GET"
			}
			if "" == test.path {
				test.path = "/info"
			}
			if "" == test.body && nil == test.err {
				test.detail = "private sentinel: /srv/config/.env SECRET=value"
				body, err := json.Marshal(map[string]string{"error": test.detail})
				if nil != err {
					t.Fatal(err)
				}
				test.body = string(body)
			}
			var output bytes.Buffer
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(original) })
			s := testServer(t, nil)
			s.config.Nodes["home"] = NodeConfig{Name: "Home", URL: "https://user:secret@node.invalid/private?token=secret", token: "secret"}
			assumeRegistriesApplied(s)
			s.nodes["home"] = &http.Client{Transport: logTransport(func(*http.Request) (*http.Response, error) {
				if nil != test.err {
					return nil, test.err
				}
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}
			w := perform(t, s, test.method, "/api/nodes/home"+test.path, test.request)
			code := test.status
			if nil != test.err {
				code = http.StatusBadGateway
			}
			if code != w.Code {
				t.Fatal("upstream status changed", w.Code)
			}
			var response struct {
				Error struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || w.Code != response.Error.Code || "" == response.Error.Message || strings.Contains(w.Body.String(), test.detail) {
				t.Fatal("unexpected public error", w.Body.String())
			}
			var record struct {
				Level     string `json:"level"`
				Message   string `json:"msg"`
				Node      string `json:"node"`
				Operation string `json:"operation"`
				Status    int    `json:"status"`
				Error     string `json:"error"`
			}
			if err := json.Unmarshal(output.Bytes(), &record); nil != err {
				t.Fatal(err, output.String())
			}
			if "ERROR" != record.Level || !strings.Contains(record.Node, "Home") || !strings.Contains(record.Node, "https://node.invalid") || "" == record.Operation || test.status != record.Status || test.detail != record.Error {
				t.Fatal("missing structured failure context", output.String())
			}
			for _, detail := range []string{"Home", "https://node.invalid", test.detail} {
				if !strings.Contains(record.Message, detail) {
					t.Fatal("failure message hides context", record.Message)
				}
			}
			if "" != test.resource && !strings.Contains(record.Message, test.resource) {
				t.Fatal("failure message hides resource", record.Message)
			}
			if nil == test.err && !strings.Contains(record.Message, strconv.Itoa(test.status)) {
				t.Fatal("failure message hides HTTP status", record.Message)
			}
			if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "/private") {
				t.Fatal("transport URL leaked", output.String())
			}
		})
	}
}

func TestRegistryActionLogs(t *testing.T) {
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	s := testServer(t, nil)
	s.config.Nodes["home"] = NodeConfig{Name: "Home", URL: "https://node.invalid"}
	w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/actions", `{"action":"update","service":"web"}`)
	if http.StatusServiceUnavailable != w.Code {
		t.Fatalf("registry rejection: %d %s", w.Code, w.Body.String())
	}
	var record struct {
		Level     string `json:"level"`
		Message   string `json:"msg"`
		Node      string `json:"node"`
		Operation string `json:"operation"`
		Status    int    `json:"status"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &record); nil != err {
		t.Fatal(err, output.String())
	}
	if "ERROR" != record.Level || http.StatusServiceUnavailable != record.Status || "" == record.Error {
		t.Fatalf("missing rejection log: %+v", record)
	}
	for _, detail := range []string{"Home", "demo", "web", "update", record.Error} {
		if !strings.Contains(record.Message, detail) {
			t.Fatalf("rejection message hides context: %s", record.Message)
		}
	}
}

func TestProxyResponseLogs(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"null", "null"},
		{"invalid", "!"},
		{"extra", "{}{}"},
		{"extra_invalid", "{}!"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(original) })
			s := testServer(t, nil)
			node := NodeConfig{Name: "Office", URL: "https://user:secret@node.invalid/private?token=secret"}
			s.config.Nodes["home"] = node
			s.nodes["home"] = &http.Client{Transport: logTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}
			if w := perform(t, s, "GET", "/api/nodes/home/info", ""); http.StatusBadGateway != w.Code {
				t.Fatal("invalid response succeeded", w.Code)
			}
			var record struct {
				Message string `json:"msg"`
				Status  int    `json:"status"`
				Error   string `json:"error"`
			}
			if err := json.Unmarshal(output.Bytes(), &record); nil != err || http.StatusOK != record.Status {
				t.Fatal("missing response diagnostics", output.String(), err)
			}
			for _, detail := range []string{node.Name, "https://node.invalid", strconv.Itoa(http.StatusOK)} {
				if !strings.Contains(record.Message, detail) {
					t.Fatal("response message hides context", record.Message)
				}
			}
			if "" != record.Error && !strings.Contains(record.Message, record.Error) {
				t.Fatal("response message hides failure cause", record.Message)
			}
			if strings.Contains(output.String(), "secret") || strings.Contains(output.String(), "/private") {
				t.Fatal("transport URL leaked", output.String())
			}
		})
	}
}

func TestNodeSettingsLogs(t *testing.T) {
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	const reason = "Connection source is not allowed by auth.allowed_ips."
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusForbidden, map[string]string{"error": reason})
	}))
	t.Cleanup(up.Close)
	s := persistedServer(t, up)
	body := fmt.Sprintf(`{"name":"Office","url":%q,"fingerprint":%q}`, up.URL, testFingerprint(up))
	for _, test := range []struct {
		method, path, body, level string
		status                    int
	}{
		{"PUT", "/api/settings/nodes/home", body, "INFO", 200},
		{"POST", "/api/settings/nodes/test", strings.Replace(body, `{"name":`, `{"id":"home","name":`, 1), "ERROR", 502},
		{"DELETE", "/api/settings/nodes/home", "", "WARN", 200},
	} {
		output.Reset()
		if w := perform(t, s, test.method, test.path, test.body); test.status != w.Code {
			t.Fatal(test.path, w.Code, w.Body.String())
		}
		var record struct {
			Message string `json:"msg"`
			Level   string `json:"level"`
		}
		if err := json.Unmarshal(output.Bytes(), &record); nil != err || test.level != record.Level {
			t.Fatal("missing settings diagnostics", output.String(), err)
		}
		for _, detail := range []string{"Office", up.URL} {
			if !strings.Contains(record.Message, detail) {
				t.Fatal("settings message hides node context", record.Message)
			}
		}
		if "INFO" != test.level && (!strings.Contains(record.Message, reason) || !strings.Contains(record.Message, "403")) {
			t.Fatal("connection test message hides failure", record.Message)
		}
		if strings.Contains(output.String(), "agent-test-token") {
			t.Fatal("settings log leaked token", output.String())
		}
	}
}
