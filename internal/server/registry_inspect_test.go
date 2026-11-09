package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/engine/registry"
	"github.com/arabcoders/dcompose/internal/protocol"
)

type registryTestTransport func(*http.Request) (*http.Response, error)

func (f registryTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func metadataResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func nativeMetadata(request *http.Request) *http.Response {
	amd := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:` + strings.Repeat(
		"a",
		64,
	) + `"},"layers":[]}`
	arm := strings.ReplaceAll(amd, strings.Repeat("a", 64), strings.Repeat("b", 64))
	amdDigest, armDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(amd))), fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(arm)))
	root := fmt.Sprintf(
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":%q,"platform":{"os":"linux","architecture":"amd64"}},{"digest":%q,"platform":{"os":"linux","architecture":"arm64"}}]}`,
		amdDigest,
		armDigest,
	)
	body := root
	if strings.HasSuffix(request.URL.Path, "/"+amdDigest) {
		body = amd
	} else if strings.HasSuffix(request.URL.Path, "/"+armDigest) {
		body = arm
	}
	response := metadataResponse(request, 200, body)
	response.Header.Set("Docker-Content-Digest", fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body))))
	return response
}

type imageNodeFixture struct {
	mu          sync.Mutex
	channel     *channelFixture
	services    map[string][]engine.Service
	local       map[string]string
	inspections int
	requests    []string
	actions     []string
	metadata    func(*http.Request) *http.Response
}

func imageService(name string) engine.Service {
	return engine.Service{ID: name + "-id", Name: name, Service: name, Image: "docker.io/library/nginx:latest", State: "running"}
}

func imageNode(t *testing.T, services ...engine.Service) *imageNodeFixture {
	t.Helper()
	f := &imageNodeFixture{
		channel:  fakeChannel(t),
		services: map[string][]engine.Service{"demo": services},
		local:    make(map[string]string),
		metadata: nativeMetadata,
	}
	stacks := []map[string]any{{"name": "demo", "status": "running", "loading": false, "services": services}}
	f.channel.snapshot, _ = json.Marshal(
		map[string]any{"health": map[string]string{"status": "ok", "image_check_interval": "6h"}, "stacks": stacks},
	)
	f.channel.http = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case "/images/inspect" == r.URL.Path:
			var input protocol.ImageInspectRequest
			_ = json.NewDecoder(r.Body).Decode(&input)
			f.mu.Lock()
			defer f.mu.Unlock()
			services, ok := f.services[input.Stack]
			if !ok {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"error":"stack not found"}`)
				return
			}
			f.inspections++
			images := []protocol.ImageInspection{}
			for _, service := range services {
				if "" != input.Container && input.Container != service.Name {
					continue
				}
				images = append(
					images,
					protocol.ImageInspection{
						ID:      service.ID,
						Service: service,
						Target: engine.ImageTarget{
							LocalID: f.local[service.Name],
							Key: engine.ImageKey{
								Ref:           service.Image,
								ImagePlatform: engine.ImagePlatform{OS: "linux", Architecture: "amd64"},
							},
						},
					},
				)
			}
			_ = json.NewEncoder(w).Encode(protocol.ImageInspectResponse{Images: images})
		case "/registry/request" == r.URL.Path:
			var input protocol.RegistryRequest
			_ = json.NewDecoder(r.Body).Decode(&input)
			request, err := http.NewRequestWithContext(r.Context(), input.Method, input.URL, strings.NewReader(string(input.Body)))
			if nil != err {
				t.Error(err)
				return
			}
			request.Header = input.Header
			f.mu.Lock()
			f.requests = append(f.requests, input.Method+" "+request.URL.Path)
			metadata := f.metadata
			f.mu.Unlock()
			response := metadata(request)
			defer func() { _ = response.Body.Close() }()
			body, _ := io.ReadAll(response.Body)
			_ = json.NewEncoder(w).Encode(protocol.RegistryResponse{Status: response.StatusCode, Header: response.Header, Body: body})
		case strings.HasSuffix(r.URL.Path, "/actions"):
			var input struct {
				Action    string `json:"action"`
				Service   string `json:"service"`
				Automatic bool   `json:"automatic"`
			}
			_ = json.NewDecoder(r.Body).Decode(&input)
			conn := f.channel.active.Load()
			if nil == conn {
				t.Error("automatic update has no live node channel")
				w.WriteHeader(503)
				return
			}
			sendRuntime(t, conn, "stack.action", `{"name":"demo","action":"update","phase":"start"}`)
			f.mu.Lock()
			if "update" != input.Action || !input.Automatic || "" == input.Service {
				t.Errorf("not a service-scoped automatic instruction: %+v", input)
			}
			f.actions = append(f.actions, input.Service)
			for i := range f.services["demo"] {
				service := &f.services["demo"][i]
				if service.Service == input.Service {
					f.local[service.Name] = "sha256:" + strings.Repeat("a", 64)
					service.ID += "-updated"
				}
			}
			runtime, _ := json.Marshal(map[string]any{"name": "demo", "status": "running", "busy": true, "services": f.services["demo"]})
			f.mu.Unlock()
			sendRuntime(t, conn, "stack.runtime", string(runtime))
			sendRuntime(t, conn, "stack.action", `{"name":"demo","action":"update","phase":"exit","exit_code":0}`)
			jsonResponse(w, http.StatusAccepted, map[string]string{"status": "accepted"})
		case "/stacks/demo/status" == r.URL.Path || "/stacks/demo" == r.URL.Path:
			f.mu.Lock()
			defer f.mu.Unlock()
			_ = json.NewEncoder(w).
				Encode(map[string]any{"name": "demo", "status": "running", "services": f.services["demo"], "compose": "services: {}", "env": ""})
		default:
			t.Error("unexpected image-node route", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	return f
}

func imageServer(t *testing.T, f *imageNodeFixture, background bool) *Server {
	t.Helper()
	s := testServer(t, f.channel.server)
	s.config.Registries = []RegistryConfig{
		{
			ID:               "login",
			Registry:         "docker.io",
			BackgroundChecks: background,
			auth:             map[string]string{"username": "test", "password": "test-secret"},
		},
	}
	s.registrySync = registrySnapshot(s.config.Registries)
	s.registryClient = registry.NewClient(&http.Client{Transport: registryTransport{server: s}})
	return s
}

func imageReady(t *testing.T, s *Server, node string) {
	t.Helper()
	awaitImage(t, func() bool {
		s.hubMu.Lock()
		defer s.hubMu.Unlock()
		return s.imageReadyLocked(node)
	})
}

func awaitImage(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !condition() {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("image state did not settle")
		}
	}
}

func imageStatus(s *Server, node, stack, container string) string {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if state := s.images.stacks[imageStackKey{node, stack}]; nil != state {
		return state.checked[container].update.Status
	}
	return ""
}

func TestRegistrySharedMetadata(t *testing.T) {
	one, two := imageNode(t), imageNode(t)
	s := imageServer(t, one, false)
	node := NodeConfig{
		Name:        "away",
		URL:         two.channel.server.URL,
		token:       "agent-test-token",
		Fingerprint: testFingerprint(two.channel.server),
	}
	s.config.Nodes["away"] = node
	s.nodes["away"] = nodeClient(node)
	now := time.Now().UTC().Truncate(time.Second)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	s.registryClient.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	config, _ := json.Marshal(registry.DockerConfig{Auths: map[string]map[string]string{}})
	key := imageKey{Ref: "docker.io/library/nginx:latest", OS: "linux", Architecture: "amd64", purpose: "explicit"}
	lookup := func(node string, age time.Duration) engine.RegistryImage {
		return s.registryClient.InspectConfig(registryNodeContext(context.Background(), node), key.ImageKey, config, age)
	}
	amd := lookup("home", time.Hour)
	if "sha256:"+strings.Repeat("a", 64) != amd.Digest || !now.Equal(amd.CheckedAt) || 3 != len(one.requests) {
		t.Fatalf("first lookup: %+v requests=%v", amd, one.requests)
	}
	if result := lookup("away", time.Hour); amd.Digest != result.Digest || 0 != len(two.requests) {
		t.Fatalf("same image refetched: %+v requests=%v", result, two.requests)
	}
	key.Architecture = "arm64"
	arm := lookup("away", time.Hour)
	if "sha256:"+strings.Repeat("b", 64) != arm.Digest || !amd.CheckedAt.Equal(arm.CheckedAt) || 1 != len(two.requests) {
		t.Fatalf("mixed platform root not shared: %+v requests=%v", arm, two.requests)
	}
	clock.Store(now.Add(30 * time.Minute).UnixNano())
	custom := lookup("away", 30*time.Minute)
	if !custom.CheckedAt.Equal(now.Add(30*time.Minute)) || 2 != len(two.requests) {
		t.Fatalf("custom interval did not refresh HEAD: %+v", custom)
	}
	clock.Store(now.Add(31 * time.Minute).UnixNano())
	manual := lookup("home", 0)
	if !manual.CheckedAt.Equal(now.Add(31*time.Minute)) || 4 != len(one.requests) {
		t.Fatalf("manual check did not refresh HEAD: %+v", manual)
	}
}
