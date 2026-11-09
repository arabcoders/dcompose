package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func registryBridgeFixture(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if testHealth(w, r) {
			return
		}
		if http.MethodPost != r.Method || "/registry/request" != r.URL.Path || "Bearer agent-test-token" != r.Header.Get("Authorization") ||
			strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) {
			t.Errorf("agent transport request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(fixture.Close)
	return fixture
}

func TestRegistryTransport(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			fixture := registryBridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				var input protocol.RegistryRequest
				if err := json.NewDecoder(r.Body).Decode(&input); nil != err {
					t.Error(err)
					return
				}
				if method != input.Method || "https://private.invalid/token" != input.URL ||
					"Bearer registry-token" != input.Header.Get("Authorization") ||
					"refresh_token=secret" != string(input.Body) {
					t.Errorf("registry wire request: %+v", input)
				}
				_ = json.NewEncoder(w).Encode(protocol.RegistryResponse{
					Status: http.StatusTooManyRequests,
					Header: http.Header{
						"Retry-After":      {"60"},
						"Www-Authenticate": {`Bearer realm="https://auth.invalid"`, `Basic realm="private"`},
					},
					Body: []byte("registry limit"),
				})
			})
			s := testServer(t, fixture)
			request, _ := http.NewRequestWithContext(
				registryNodeContext(context.Background(), "home"),
				method,
				"https://private.invalid/token",
				strings.NewReader("refresh_token=secret"),
			)
			request.Header.Set("Authorization", "Bearer registry-token")
			response, err := (registryTransport{server: s}).RoundTrip(request)
			if nil != err {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			body, _ := io.ReadAll(response.Body)
			if 429 != response.StatusCode || "60" != response.Header.Get("Retry-After") ||
				2 != len(response.Header.Values("WWW-Authenticate")) ||
				request != response.Request {
				t.Fatalf("registry response: %+v", response)
			}
			if http.MethodHead == method {
				if 0 != len(body) {
					t.Fatalf("HEAD body: %q", body)
				}
			} else if "registry limit" != string(body) {
				t.Fatalf("registry body: %q", body)
			}
		})
	}
}

func TestRegistryTransportNode(t *testing.T) {
	home := registryBridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.RegistryResponse{Status: 200, Body: []byte("home")})
	})
	away := registryBridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(protocol.RegistryResponse{Status: 200, Body: []byte("away")})
	})
	s := testServer(t, home)
	node := NodeConfig{Name: "Away", URL: away.URL, token: "agent-test-token", Fingerprint: testFingerprint(away)}
	s.config.Nodes["away"] = node
	s.nodes["away"] = nodeClient(node)
	request, _ := http.NewRequestWithContext(
		registryNodeContext(context.Background(), "away"),
		http.MethodGet,
		"https://private.invalid/v2/",
		nil,
	)
	response, err := (registryTransport{server: s}).RoundTrip(request)
	if nil != err {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if "away" != string(body) {
		t.Fatalf("wrong execution node: %q", body)
	}
	node.Fingerprint = base64.StdEncoding.EncodeToString(make([]byte, 32))
	s.nodes["away"] = nodeClient(node)
	response, err = (registryTransport{server: s}).RoundTrip(request)
	if nil != response || !errors.Is(err, errFingerprint) {
		t.Fatalf("node pin bypassed: response=%v error=%v", response, err)
	}
}

func TestRegistryTransportFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		cause  string
	}{
		{"auth", 401, `{"error":"node token rejected"}`, "node token rejected"},
		{"node", 503, `{"error":"node shutting down"}`, "node shutting down"},
		{"network", 200, `{"error":"dial tcp: connection refused"}`, "connection refused"},
		{"invalid", 200, `{"status":0}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var direct atomic.Int32
			registry := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { direct.Add(1) }))
			defer registry.Close()
			fixture := registryBridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			})
			s := testServer(t, fixture)
			request, _ := http.NewRequestWithContext(registryNodeContext(context.Background(), "home"), http.MethodGet, registry.URL, nil)
			response, err := (registryTransport{server: s}).RoundTrip(request)
			if nil != response || nil == err || ("" != test.cause && !strings.Contains(err.Error(), test.cause)) || 0 != direct.Load() {
				t.Fatalf("agent failure: response=%v error=%v direct requests=%d", response, err, direct.Load())
			}
			request = request.WithContext(context.Background())
			_, err = (registryTransport{server: s}).RoundTrip(request)
			if nil == err || 0 != direct.Load() {
				t.Fatalf("missing node: %v direct requests=%d", err, direct.Load())
			}
		})
	}
}

func TestRegistryTransportCancel(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	fixture := registryBridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(canceled)
	})
	s := testServer(t, fixture)
	ctx, cancel := context.WithCancel(registryNodeContext(context.Background(), "home"))
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://private.invalid/v2/", nil)
	result := make(chan error, 1)
	go func() {
		_, err := (registryTransport{server: s}).RoundTrip(request)
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("agent request not received")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("registry request did not cancel")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("agent request did not cancel")
	}
}

func TestRegistryTransportBounds(t *testing.T) {
	var requests atomic.Int32
	fixture := registryBridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_ = json.NewEncoder(w).
			Encode(protocol.RegistryResponse{Status: http.StatusOK, Header: http.Header{"X-Padding": {strings.Repeat("x", (12<<20)+1)}}, Body: []byte("metadata")})
	})
	s := testServer(t, fixture)
	transport := registryTransport{server: s}
	request, _ := http.NewRequestWithContext(
		registryNodeContext(context.Background(), "home"),
		http.MethodGet,
		"https://private.invalid/v2/",
		nil,
	)
	if _, err := transport.RoundTrip(request); nil == err {
		t.Fatalf("response bound: %v", err)
	}
	request, _ = http.NewRequestWithContext(
		registryNodeContext(context.Background(), "home"),
		http.MethodPost,
		"https://private.invalid/token",
		strings.NewReader(strings.Repeat("x", (1<<20)+1)),
	)
	if _, err := transport.RoundTrip(request); nil == err || 1 != requests.Load() {
		t.Fatalf("request bound: %v", err)
	}
}
