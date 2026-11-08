package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/protocol"
)

type registryRelayRoundTripper func(*http.Request) (*http.Response, error)

func (transport registryRelayRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestRegistryRelay(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if method != r.Method || "Bearer registry-token" != r.Header.Get("Authorization") || "application/json" != r.Header.Get("Accept") {
					t.Errorf("registry request: method=%s headers=%v", r.Method, r.Header)
				}
				if http.MethodPost == method && "refresh_token=secret" != string(body) {
					t.Errorf("token request body: %q", body)
				}
				w.Header().Set("Retry-After", "120")
				w.Header().Add("WWW-Authenticate", `Bearer realm="https://auth.example/token"`)
				w.Header().Add("WWW-Authenticate", `Basic realm="registry"`)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, "registry limit")
			}))
			defer fixture.Close()
			input := protocol.RegistryRequest{Method: method, URL: fixture.URL + "/v2/repo/manifests/latest", Header: http.Header{"Authorization": {"Bearer registry-token"}, "Accept": {"application/json"}}}
			if http.MethodPost == method {
				input.Body = []byte("refresh_token=secret")
			}
			output, err := relayRegistry(context.Background(), input, fixture.Client().Transport)
			if nil != err || "" != output.Error || 429 != output.Status || "120" != output.Header.Get("Retry-After") || 2 != len(output.Header.Values("WWW-Authenticate")) {
				t.Fatalf("registry response: %+v error=%v", output, err)
			}
			if http.MethodHead == method {
				if 0 != len(output.Body) {
					t.Fatalf("HEAD returned a body: %q", output.Body)
				}
			} else if "registry limit" != string(output.Body) {
				t.Fatalf("registry body: %q", output.Body)
			}
		})
	}
}

func TestRegistryRelayRedirect(t *testing.T) {
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://must-not-be-contacted.invalid/token")
		w.WriteHeader(http.StatusFound)
	}))
	defer fixture.Close()
	output, err := relayRegistry(context.Background(), protocol.RegistryRequest{Method: http.MethodGet, URL: fixture.URL}, fixture.Client().Transport)
	if nil != err || 302 != output.Status || "https://must-not-be-contacted.invalid/token" != output.Header.Get("Location") {
		t.Fatalf("redirect was not preserved: %+v %v", output, err)
	}
	output, err = relayRegistry(context.Background(), protocol.RegistryRequest{Method: http.MethodGet, URL: fixture.URL}, http.DefaultTransport)
	if nil != err || "" == output.Error || 0 != output.Status {
		t.Fatalf("untrusted TLS accepted: %+v %v", output, err)
	}
}

func TestRegistryRelayValidation(t *testing.T) {
	transport := registryRelayRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid registry request reached the network")
		return nil, nil
	})
	for _, input := range []protocol.RegistryRequest{
		{Method: http.MethodDelete, URL: "https://registry.example"},
		{Method: http.MethodGet, URL: "http://registry.example"},
		{Method: http.MethodGet, URL: "https://user:password@registry.example"},
		{Method: http.MethodPost, URL: "https://registry.example", Body: make([]byte, registryRequestLimit+1)},
	} {
		if _, err := relayRegistry(context.Background(), input, transport); nil == err {
			t.Fatal("invalid request accepted")
		}
	}
}

func TestRegistryRelayFailure(t *testing.T) {
	input := protocol.RegistryRequest{Method: http.MethodPost, URL: "https://registry.example/token?account=query-secret", Header: http.Header{"Authorization": {"Bearer header-secret"}}, Body: []byte("refresh_token=body-secret")}
	transport := registryRelayRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: input.URL, Err: errors.New("connection refused: query-secret header-secret body-secret")}
	})
	output, err := relayRegistry(context.Background(), input, transport)
	if nil != err || 0 != output.Status || !strings.Contains(output.Error, "connection refused") {
		t.Fatalf("transport error: %+v %v", output, err)
	}
	for _, secret := range []string{"https://", "query-secret", "header-secret", "body-secret"} {
		if strings.Contains(output.Error, secret) {
			t.Fatalf("transport error leaked %q: %s", secret, output.Error)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport = registryRelayRoundTripper(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})
	output, err = relayRegistry(ctx, input, transport)
	if nil != err || context.Canceled.Error() != output.Error {
		t.Fatalf("cancellation: %+v %v", output, err)
	}
}

func TestRegistryRelayBounds(t *testing.T) {
	transport := registryRelayRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", registryResponseLimit+1)))}, nil
	})
	_, err := relayRegistry(context.Background(), protocol.RegistryRequest{Method: http.MethodGet, URL: "https://registry.example"}, transport)
	var failure *apiError
	if !errors.As(err, &failure) || http.StatusBadGateway != failure.code {
		t.Fatalf("oversized response: %v", err)
	}
}
