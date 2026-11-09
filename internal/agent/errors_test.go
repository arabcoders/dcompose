package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

func TestErrorResponse(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code int
	}{
		{"filesystem", &os.PathError{Op: "open", Path: "/private/stacks/.env", Err: errors.New("private diagnostic")}, 500},
		{"wrapped", fmt.Errorf("private diagnostic: %w", fault(404, "stack not found")), 404},
		{"engine", &engine.Error{Code: 502, Message: "Could not read container information. Try again.", Cause: errors.New("private diagnostic")}, 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(original) })
			r := httptest.NewRequest("GET", "/stacks/demo?token=query-secret", nil)
			r.Header.Set("Authorization", "Bearer header-secret")
			r.RemoteAddr = "192.0.2.8:42"
			w := httptest.NewRecorder()
			respondError(w, r, test.err)
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(
				w.Body.Bytes(),
				&response,
			); nil != err || test.code != w.Code || "" == response.Error || strings.Contains(response.Error, "private diagnostic") ||
				strings.Contains(response.Error, "/private/stacks/.env") {
				t.Fatal(w.Code, w.Body.String(), err)
			}
			var apiErr *engine.Error
			if errors.As(test.err, &apiErr) && apiErr.Message != response.Error {
				t.Fatal("public fixture error was not passed through", response.Error)
			}
			var requestErr *apiError
			if errors.As(test.err, &requestErr) && requestErr.message != response.Error {
				t.Fatal("public fixture error was not passed through", response.Error)
			}
			var record struct {
				Message string `json:"msg"`
				Status  int    `json:"status"`
				Error   string `json:"error"`
			}
			if err := json.Unmarshal(logs.Bytes(), &record); nil != err || test.code != record.Status || test.err.Error() != record.Error {
				t.Fatal("missing request diagnostics", logs.String(), err)
			}
			for _, detail := range []string{"private diagnostic", r.RemoteAddr, r.Method, r.URL.Path, fmt.Sprint(test.code)} {
				if !strings.Contains(record.Message, detail) {
					t.Fatalf("message hides diagnostic %q: %s", detail, record.Message)
				}
			}
			if strings.Contains(logs.String(), "query-secret") || strings.Contains(logs.String(), "header-secret") {
				t.Fatal("request credentials leaked", logs.String())
			}
		})
	}
}

func TestInventoryFailure(t *testing.T) {
	a, _ := fixture(t)
	a.config.Global.StacksDir = filepath.Join(t.TempDir(), "private-stacks", "missing")
	w := request(a, "GET", "/stacks", "")
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(
		w.Body.Bytes(),
		&response,
	); nil != err || 500 != w.Code || "" == response.Error || strings.Contains(w.Body.String(), a.config.Global.StacksDir) ||
		strings.Contains(w.Body.String(), "private-stacks") {
		t.Fatal("inventory error exposed internals", w.Code, w.Body.String())
	}
}

func TestProxySourceError(t *testing.T) {
	c, err := loadConfig(configFixture(t, "", "allowed_ips = ['192.0.2.10']\ntrusted_proxies = ['198.51.100.1']"))
	if nil != err {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, peer, forwarded string
		code                  int
	}{
		{"proxy", "198.51.100.1:42", "malformed", http.StatusForbidden},
		{"denied", "203.0.113.1:42", "", http.StatusForbidden},
		{"token", "192.0.2.10:42", "", http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(original) })
			a := newAgent(t.Context(), c)
			w := sourceRequest(a, test.peer, "Bearer wrong-secret", test.forwarded)
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || test.code != w.Code || "" == response.Error {
				t.Fatal("missing public rejection error", w.Code, w.Body.String(), err)
			}
			if http.StatusUnauthorized == test.code && "Bearer" != w.Header().Get("WWW-Authenticate") {
				t.Fatal("missing authentication challenge", w.Header())
			}
			for _, private := range []string{"malformed", "198.51.100.1", "192.0.2.10", "203.0.113.1", "wrong-secret", "test-secret"} {
				if strings.Contains(w.Body.String(), private) {
					t.Fatal("rejection exposed internals", w.Body.String())
				}
			}
			var record struct {
				Message string `json:"msg"`
				Level   string `json:"level"`
				Peer    string `json:"peer"`
				Source  string `json:"source"`
				Error   string `json:"error"`
			}
			if err := json.Unmarshal(logs.Bytes(), &record); nil != err || "WARN" != record.Level || test.peer != record.Peer {
				t.Fatal("missing rejection diagnostics", logs.String(), err)
			}
			for _, detail := range []string{"GET", "/health", test.peer} {
				if !strings.Contains(record.Message, detail) {
					t.Fatal("rejection message hides request context", record.Message)
				}
			}
			if "proxy" == test.name {
				if "" == record.Error || !strings.Contains(record.Message, record.Error) {
					t.Fatal("missing forwarding failure cause", logs.String())
				}
			} else if strings.SplitN(test.peer, ":", 2)[0] != record.Source || !strings.Contains(record.Message, record.Source) {
				t.Fatal("missing rejected source", logs.String())
			}
			if strings.Contains(logs.String(), "wrong-secret") || strings.Contains(logs.String(), "test-secret") ||
				strings.Contains(logs.String(), "malformed") {
				t.Fatal("rejection logs leaked credentials or forwarding input", logs.String())
			}
		})
	}
}

func TestDecodeLogs(t *testing.T) {
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	r := httptest.NewRequest("POST", "/stacks/demo?token=query-secret", strings.NewReader("!"))
	r.Header.Set("Authorization", "Bearer header-secret")
	r.RemoteAddr = "192.0.2.8:42"
	var input struct {
		Action string `json:"action"`
	}
	var apiErr *apiError
	if err := decodeBody(r, &input); !errors.As(err, &apiErr) || http.StatusBadRequest != apiErr.code {
		t.Fatal("invalid JSON succeeded", err)
	}
	var record struct {
		Message string `json:"msg"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(logs.Bytes(), &record); nil != err || "" == record.Error {
		t.Fatal("missing decode diagnostics", logs.String(), err)
	}
	for _, detail := range []string{r.Method, r.URL.Path, r.RemoteAddr, record.Error} {
		if !strings.Contains(record.Message, detail) {
			t.Fatal("decode message hides failure context", record.Message)
		}
	}
	if strings.Contains(logs.String(), "query-secret") || strings.Contains(logs.String(), "header-secret") {
		t.Fatal("decode logs leaked credentials", logs.String())
	}
}
