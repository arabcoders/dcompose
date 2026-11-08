package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/engine/registry"
	"github.com/arabcoders/dcompose/internal/protocol"
)

func imageRequests(f *imageNodeFixture) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func imageActions(f *imageNodeFixture) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

func TestImageBackground(t *testing.T) {
	f := imageNode(t, imageService("web"))
	s := imageServer(t, f, true)
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	awaitImage(t, func() bool { return "available" == imageStatus(s, "home", "demo", "web") })
	if 3 != imageRequests(f) {
		t.Fatal("server did not perform one metadata lookup through the node", imageRequests(f))
	}
	s.hubMu.Lock()
	watchers := len(s.watchers)
	s.hubMu.Unlock()
	if 0 != watchers {
		t.Fatal("background checks required a browser")
	}
	for _, path := range []string{"/api/nodes/home/stacks/demo", "/api/nodes/home/stacks/demo/status"} {
		response := perform(t, s, "GET", path, "")
		if 200 != response.Code || !strings.Contains(response.Body.String(), `"image_update":{"status":"available"`) {
			t.Fatal("HTTP runtime did not use server comparison state", response.Code, response.Body.String())
		}
	}
	w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/images/check", "")
	if 202 != w.Code || `{"status":"checking"}` != strings.TrimSpace(w.Body.String()) {
		t.Fatal(w.Code, w.Body.String())
	}
	awaitImage(t, func() bool {
		s.hubMu.Lock()
		defer s.hubMu.Unlock()
		state := s.images.stacks[imageStackKey{"home", "demo"}]
		return !state.manual && !state.inflight
	})
	if 3 != imageRequests(f) || "available" != imageStatus(s, "home", "demo", "web") {
		t.Fatal("manual check repeated a known available lookup", imageRequests(f))
	}
}

func TestImageManualScope(t *testing.T) {
	f := imageNode(t, imageService("web"), imageService("worker"))
	s := imageServer(t, f, false)
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	imageReady(t, s, "home")
	w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/images/check", `{"container":"web"}`)
	if 202 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	awaitImage(t, func() bool { return "available" == imageStatus(s, "home", "demo", "web") })
	if "" != imageStatus(s, "home", "demo", "worker") || 3 != imageRequests(f) || 0 != len(imageActions(f)) {
		t.Fatal("manual selection checked another container or started an update")
	}
	if w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/images/check", `{"container":"outside"}`); 404 != w.Code {
		t.Fatal("container outside stack accepted", w.Code)
	}
	if w := perform(t, s, "POST", "/api/nodes/home/stacks/missing/images/check", ""); 404 != w.Code {
		t.Fatal("missing stack accepted", w.Code)
	}
}

func TestImageManualSharedRef(t *testing.T) {
	f := imageNode(t, imageService("web"), imageService("worker"))
	s := imageServer(t, f, false)
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	imageReady(t, s, "home")
	if w := perform(t, s, "POST", "/api/nodes/home/images/check", ""); 202 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	awaitImage(t, func() bool { return "available" == imageStatus(s, "home", "demo", "worker") })
	if 3 != imageRequests(f) {
		t.Fatal("replicas repeated a forced metadata lookup", imageRequests(f))
	}
	if w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/images/check", `{"container":"web"}`); 202 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	awaitImage(t, func() bool {
		s.hubMu.Lock()
		defer s.hubMu.Unlock()
		state := s.images.stacks[imageStackKey{"home", "demo"}]
		return !state.manual && !state.inflight
	})
	if 3 != imageRequests(f) {
		t.Fatal("manual check repeated a known shared lookup", imageRequests(f))
	}
	if "available" != imageStatus(s, "home", "demo", "worker") {
		t.Fatal("manual refresh discarded an unselected comparison")
	}
}

func TestImageAvailableExpiry(t *testing.T) {
	for _, interval := range []string{"", "30m"} {
		t.Run("interval="+interval, func(t *testing.T) {
			service := imageService("web")
			service.CheckInterval = interval
			f := imageNode(t, service)
			s := imageServer(t, f, "" == interval)
			now := time.Now().UTC()
			var clock atomic.Int64
			clock.Store(now.UnixNano())
			s.images.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			s.registryClient.Now = s.images.now
			startChannels(t, s)
			_ = nextConnection(t, f.channel)
			awaitImage(t, func() bool { return "available" == imageStatus(s, "home", "demo", "web") })
			awaitImage(t, func() bool {
				s.hubMu.Lock()
				defer s.hubMu.Unlock()
				return !s.images.stacks[imageStackKey{"home", "demo"}].inflight
			})
			s.hubMu.Lock()
			state := s.images.stacks[imageStackKey{"home", "demo"}]
			checked := state.checked["web"]
			// Registry cache expiry or eviction must not discard known availability.
			clear(s.images.cache)
			clock.Store(now.Add(7 * time.Hour).UnixNano())
			s.scheduleImagesLocked()
			work := s.imageWorkLocked(imageStackKey{"home", "demo"}, state)
			retained := state.checked["web"]
			s.hubMu.Unlock()
			if work || "available" != retained.update.Status || !checked.update.CheckedAt.Equal(*retained.update.CheckedAt) || 3 != imageRequests(f) {
				t.Fatal("known availability expired or scheduled another lookup", work, imageRequests(f))
			}
		})
	}
}

func TestImageManualMixed(t *testing.T) {
	for _, scope := range []string{"/api/nodes/home/images/check", "/api/nodes/home/stacks/demo/images/check"} {
		t.Run(scope, func(t *testing.T) {
			f := imageNode(t, imageService("web"), imageService("worker"))
			f.local["worker"] = "sha256:" + strings.Repeat("a", 64)
			s := imageServer(t, f, false)
			startChannels(t, s)
			_ = nextConnection(t, f.channel)
			imageReady(t, s, "home")
			if w := perform(t, s, "POST", scope, ""); 202 != w.Code {
				t.Fatal(w.Code, w.Body.String())
			}
			awaitImage(t, func() bool { return "current" == imageStatus(s, "home", "demo", "worker") })
			s.hubMu.Lock()
			checked := s.images.stacks[imageStackKey{"home", "demo"}].checked["web"]
			s.hubMu.Unlock()
			f.mu.Lock()
			f.metadata = func(r *http.Request) *http.Response { return metadataResponse(r, 503, "provider unavailable") }
			f.mu.Unlock()
			if w := perform(t, s, "POST", scope, ""); 202 != w.Code {
				t.Fatal(w.Code, w.Body.String())
			}
			awaitImage(t, func() bool { return "error" == imageStatus(s, "home", "demo", "worker") })
			s.hubMu.Lock()
			retained := s.images.stacks[imageStackKey{"home", "demo"}].checked["web"]
			s.hubMu.Unlock()
			if "available" != retained.update.Status || !checked.update.CheckedAt.Equal(*retained.update.CheckedAt) || 4 != imageRequests(f) {
				t.Fatal("mixed check replaced known availability or skipped the current image", retained.update, imageRequests(f))
			}
		})
	}
}

func TestImageLocalChange(t *testing.T) {
	f := imageNode(t, imageService("web"))
	f.local["web"] = "sha256:" + strings.Repeat("c", 64)
	s := imageServer(t, f, false)
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	imageReady(t, s, "home")
	if w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/images/check", ""); 202 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	awaitImage(t, func() bool { return "available" == imageStatus(s, "home", "demo", "web") })
	f.mu.Lock()
	f.local["web"] = "sha256:" + strings.Repeat("a", 64)
	f.mu.Unlock()
	if w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/images/check", ""); 202 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	awaitImage(t, func() bool { return "current" == imageStatus(s, "home", "demo", "web") })
	if 4 != imageRequests(f) {
		t.Fatal("changed local image reused known availability", imageRequests(f))
	}
}

func TestImageInspectFailure(t *testing.T) {
	s := imageServer(t, imageNode(t), false)
	key := imageStackKey{"home", "demo"}
	state := newImageStack()
	state.services = []engine.Service{imageService("web"), imageService("worker")}
	at := time.Now().UTC()
	state.checked["web"] = checkedImage{id: "web-id", image: state.services[0].Image, update: engine.ImageUpdate{Status: "available", CheckedAt: &at}}
	s.images.stacks[key] = state
	s.imageCheckFailed(key, state.revision, context.DeadlineExceeded)
	if "available" != state.checked["web"].update.Status || "error" != state.checked["worker"].update.Status {
		t.Fatal("stack inspection failure replaced known availability", state.checked)
	}
}

func TestImageAvailableAuto(t *testing.T) {
	for _, interval := range []string{"", "30m"} {
		t.Run("interval="+interval, func(t *testing.T) {
			service := imageService("web")
			service.CheckInterval = interval
			f := imageNode(t, service)
			handler := f.channel.http
			f.channel.http = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/actions") && 3 != imageRequests(f) {
					t.Error("automatic update repeated a known available lookup", imageRequests(f))
				}
				handler.ServeHTTP(w, r)
			})
			s := imageServer(t, f, "" == interval)
			now := time.Now().UTC()
			var clock atomic.Int64
			clock.Store(now.UnixNano())
			s.images.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
			s.registryClient.Now = s.images.now
			startChannels(t, s)
			conn := nextConnection(t, f.channel)
			awaitImage(t, func() bool { return "available" == imageStatus(s, "home", "demo", "web") })
			service.AutoUpdate = true
			f.mu.Lock()
			f.services["demo"][0] = service
			f.mu.Unlock()
			payload, _ := json.Marshal(map[string]interface{}{"name": "demo", "status": "running", "services": []engine.Service{service}})
			sendRuntime(t, conn, "stack.runtime", string(payload))
			clock.Store(now.Add(31 * time.Minute).UnixNano())
			awaitImage(t, func() bool { return 1 == len(imageActions(f)) })
			awaitImage(t, func() bool { return "current" == imageStatus(s, "home", "demo", "web") })
		})
	}
}

func TestImageSequentialUpdates(t *testing.T) {
	web, worker, stopped := imageService("web"), imageService("worker"), imageService("stopped")
	web.AutoUpdate, worker.AutoUpdate, stopped.AutoUpdate = true, true, true
	web.CheckInterval, worker.CheckInterval, stopped.CheckInterval = "30m", "30m", "30m"
	stopped.State = "exited"
	f := imageNode(t, web, worker, stopped)
	s := imageServer(t, f, false)
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	awaitImage(t, func() bool { return 2 == len(imageActions(f)) })
	actions := imageActions(f)
	if "web,worker" != strings.Join(actions, ",") {
		t.Fatal("automatic updates were not running-only and service sequential", actions)
	}
	awaitImage(t, func() bool { return "current" == imageStatus(s, "home", "demo", "worker") })
	s.hubMu.Lock()
	state := s.images.stacks[imageStackKey{"home", "demo"}]
	remaining := len(state.autoPending)
	s.hubMu.Unlock()
	if 0 != remaining {
		t.Fatal("updated services remained queued", remaining)
	}
}

func TestImageManualNoAutomatic(t *testing.T) {
	service := imageService("web")
	service.AutoUpdate = true
	f := imageNode(t, service)
	s := imageServer(t, f, false)
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	imageReady(t, s, "home")
	if w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/images/check", ""); 202 != w.Code {
		t.Fatal(w.Code)
	}
	awaitImage(t, func() bool { return "available" == imageStatus(s, "home", "demo", "web") })
	if 0 != len(imageActions(f)) {
		t.Fatal("manual-only lookup initiated an automatic update")
	}
}

func TestImageMetadataNonblocking(t *testing.T) {
	f := imageNode(t, imageService("web"))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.metadata = func(request *http.Request) *http.Response {
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-request.Context().Done():
		}
		return nativeMetadata(request)
	}
	s := imageServer(t, f, true)
	startChannels(t, s)
	conn := nextConnection(t, f.channel)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("metadata did not start")
	}
	sendRuntime(t, conn, "stack.runtime", `{"name":"other","status":"stopped","services":[]}`)
	awaitCache(t, s, `"name":"other"`, "")
	close(release)
	awaitImage(t, func() bool { return "available" == imageStatus(s, "home", "demo", "web") })
}

func TestImageInvalidation(t *testing.T) {
	f := imageNode(t, imageService("web"))
	s := imageServer(t, f, true)
	startChannels(t, s)
	conn := nextConnection(t, f.channel)
	awaitImage(t, func() bool { return "available" == imageStatus(s, "home", "demo", "web") })
	data, _ := json.Marshal(protocol.ImageInvalidated{Stack: "demo", Refs: []string{"docker.io/library/nginx:latest"}})
	sendRuntime(t, conn, "images.invalidated", string(data))
	awaitImage(t, func() bool { return 4 == imageRequests(f) && "available" == imageStatus(s, "home", "demo", "web") })
	// Same reference, new container identity must force a new central observation.
	f.mu.Lock()
	f.services["demo"][0].ID = "replacement"
	runtime, _ := json.Marshal(map[string]interface{}{"name": "demo", "status": "running", "services": f.services["demo"]})
	f.mu.Unlock()
	sendRuntime(t, conn, "stack.runtime", string(runtime))
	awaitImage(t, func() bool { return 5 == imageRequests(f) && "available" == imageStatus(s, "home", "demo", "web") })
	// A changed image reference must invalidate availability even with the same container ID.
	f.mu.Lock()
	f.services["demo"][0].Image = "docker.io/library/nginx:stable"
	runtime, _ = json.Marshal(map[string]interface{}{"name": "demo", "status": "running", "services": f.services["demo"]})
	f.mu.Unlock()
	sendRuntime(t, conn, "stack.runtime", string(runtime))
	awaitImage(t, func() bool { return 8 == imageRequests(f) && "available" == imageStatus(s, "home", "demo", "web") })
}

func TestImageCooldownRestart(t *testing.T) {
	f := imageNode(t, imageService("web"))
	f.metadata = func(r *http.Request) *http.Response {
		response := metadataResponse(r, 429, `{"errors":[{"message":"quota exhausted"}]}`)
		response.Header.Set("Retry-After", "3600")
		return response
	}
	s := imageServer(t, f, true)
	s.config.path = filepath.Join(t.TempDir(), "dcompose.toml")
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	awaitImage(t, func() bool { return "error" == imageStatus(s, "home", "demo", "web") })
	if 1 != imageRequests(f) {
		t.Fatal("registry limit retried", imageRequests(f))
	}
	if w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/images/check", ""); 202 != w.Code {
		t.Fatal(w.Code)
	}
	awaitImage(t, func() bool { return "error" == imageStatus(s, "home", "demo", "web") })
	if 1 != imageRequests(f) {
		t.Fatal("manual check bypassed provider cooldown")
	}
	s.Close()
	restarted := imageServer(t, f, true)
	restarted.config.path = s.config.path
	startChannels(t, restarted)
	_ = nextConnection(t, f.channel)
	awaitImage(t, func() bool { return "error" == imageStatus(restarted, "home", "demo", "web") })
	if 1 != imageRequests(f) || 1 != len(restarted.registryClient.Cooldowns()) {
		t.Fatal("server restart discarded account cooldown")
	}
}

func TestImageScheduleIntervals(t *testing.T) {
	f := imageNode(t, imageService("web"))
	f.local["web"] = "sha256:" + strings.Repeat("a", 64)
	s := imageServer(t, f, true)
	now := time.Now().UTC()
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	s.images.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	s.registryClient.Now = s.images.now
	startChannels(t, s)
	conn := nextConnection(t, f.channel)
	awaitImage(t, func() bool { return "current" == imageStatus(s, "home", "demo", "web") })
	service := imageService("web")
	service.CheckInterval = "30m"
	f.mu.Lock()
	f.services["demo"][0] = service
	f.mu.Unlock()
	payload, _ := json.Marshal(map[string]interface{}{"name": "demo", "status": "running", "services": []engine.Service{service}})
	sendRuntime(t, conn, "stack.runtime", string(payload))
	awaitImage(t, func() bool {
		s.hubMu.Lock()
		defer s.hubMu.Unlock()
		return "explicit" == s.images.stacks[imageStackKey{"home", "demo"}].checked["web"].key.purpose
	})
	clock.Store(now.Add(31 * time.Minute).UnixNano())
	awaitImage(t, func() bool { return 4 == imageRequests(f) })
	// Disconnected nodes retain completed cache entries but start no new work.
	_ = conn.CloseNow()
	awaitImage(t, func() bool {
		s.hubMu.Lock()
		defer s.hubMu.Unlock()
		return !s.imageReadyLocked("home")
	})
	s.hubMu.Lock()
	s.scheduleImagesLocked()
	state := s.images.stacks[imageStackKey{"home", "demo"}]
	manual := state.manual
	cache := len(s.images.cache)
	s.hubMu.Unlock()
	if manual || 0 == cache {
		t.Fatal("disconnect discarded completed metadata or retained manual state")
	}
}

func TestImageDisabledGlobal(t *testing.T) {
	f := imageNode(t, imageService("web"))
	s := imageServer(t, f, false)
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	imageReady(t, s, "home")
	s.hubMu.Lock()
	s.scheduleImagesLocked()
	work := s.imageWorkLocked(imageStackKey{"home", "demo"}, s.images.stacks[imageStackKey{"home", "demo"}])
	s.hubMu.Unlock()
	if work || 0 != imageRequests(f) {
		t.Fatal("pull-only credentials enabled global checks")
	}
}

func TestImageFailureRetry(t *testing.T) {
	service := imageService("web")
	service.CheckInterval = "30m"
	f := imageNode(t, service)
	f.metadata = func(r *http.Request) *http.Response { return metadataResponse(r, 503, "provider unavailable") }
	s := imageServer(t, f, false)
	var clock atomic.Int64
	now := time.Now().UTC()
	clock.Store(now.UnixNano())
	s.images.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	s.registryClient.Now = s.images.now
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	awaitImage(t, func() bool { return "error" == imageStatus(s, "home", "demo", "web") })
	s.hubMu.Lock()
	retry := s.images.stacks[imageStackKey{"home", "demo"}].next["web"]
	s.hubMu.Unlock()
	if !now.Add(imageErrorRetry).Equal(retry) || 1 != imageRequests(f) {
		t.Fatal("scheduled failure did not use five-minute retry", retry, imageRequests(f))
	}
	clock.Store(now.Add(imageErrorRetry).UnixNano())
	awaitImage(t, func() bool { return 2 == imageRequests(f) })
}

func TestImageCooldownAccount(t *testing.T) {
	f := imageNode(t)
	f.metadata = func(r *http.Request) *http.Response {
		response := metadataResponse(r, 429, "quota exhausted")
		response.Header.Set("Retry-After", "3600")
		return response
	}
	s := imageServer(t, f, false)
	lookup := func(username string) engine.RegistryImage {
		data, _ := json.Marshal(map[string]interface{}{"auths": map[string]interface{}{"docker.io": map[string]string{"username": username, "password": "secret"}}})
		return s.registryClient.InspectConfig(registryNodeContext(context.Background(), "home"), engine.ImageKey{Ref: "docker.io/library/nginx:latest", ImagePlatform: engine.ImagePlatform{OS: "linux", Architecture: "amd64"}}, data, 0)
	}
	first := lookup("one")
	second := lookup("one")
	if nil == first.Cooldown || nil == second.Cooldown || !first.Cooldown.Until.Equal(second.Cooldown.Until) || 1 != imageRequests(f) {
		t.Fatal("shared account bypassed provider deadline")
	}
	third := lookup("two")
	if nil == third.Cooldown || 2 != imageRequests(f) || first.Cooldown.Account == third.Cooldown.Account {
		t.Fatal("provider cooldown crossed account boundaries")
	}
}

func TestImagePurposes(t *testing.T) {
	f := imageNode(t, imageService("web"))
	f.local["web"] = "sha256:" + strings.Repeat("a", 64)
	var mu sync.Mutex
	logins := []string{}
	f.metadata = func(r *http.Request) *http.Response {
		username, _, ok := r.BasicAuth()
		if !ok {
			response := metadataResponse(r, 401, "")
			response.Header.Set("WWW-Authenticate", `Basic realm="registry"`)
			return response
		}
		mu.Lock()
		logins = append(logins, username)
		mu.Unlock()
		return nativeMetadata(r)
	}
	s := imageServer(t, f, true)
	s.config.Registries = []RegistryConfig{
		{ID: "background", Registry: "docker.io", BackgroundChecks: true, auth: map[string]string{"username": "background", "password": "background-secret"}},
		{ID: "pull", Registry: "docker.io", auth: map[string]string{"username": "pull", "password": "pull-secret"}},
	}
	s.registrySync = registrySnapshot(s.config.Registries)
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	awaitImage(t, func() bool { return "current" == imageStatus(s, "home", "demo", "web") })
	if w := perform(t, s, "POST", "/api/nodes/home/stacks/demo/images/check", ""); 202 != w.Code {
		t.Fatal(w.Code)
	}
	awaitImage(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(logins, ","), "pull")
	})
	mu.Lock()
	selected := strings.Join(logins, ",")
	mu.Unlock()
	if !strings.Contains(selected, "background") || !strings.Contains(selected, "pull") {
		t.Fatal("background and manual lookups shared credential permissions", selected)
	}
}

func TestImageNodeFailure(t *testing.T) {
	one, two := imageNode(t), imageNode(t)
	one.metadata = func(r *http.Request) *http.Response { return metadataResponse(r, 503, "node route unavailable") }
	s := imageServer(t, one, false)
	node := NodeConfig{Name: "away", URL: two.channel.server.URL, token: "agent-test-token", Fingerprint: testFingerprint(two.channel.server)}
	s.config.Nodes["away"] = node
	s.nodes["away"] = nodeClient(node)
	config, _ := json.Marshal(registry.DockerConfig{Auths: map[string]map[string]string{}})
	key := imageKey{ImageKey: engine.ImageKey{Ref: "docker.io/library/nginx:latest", ImagePlatform: engine.ImagePlatform{OS: "linux", Architecture: "amd64"}}, purpose: "explicit", node: "home"}
	first := s.registryImage(registryNodeContext(context.Background(), "home"), key, config, time.Hour, time.Hour)
	key.node = "away"
	second := s.registryImage(registryNodeContext(context.Background(), "away"), key, config, time.Hour, time.Hour)
	if "error" != first.Status || "" != second.Status || "" == second.Digest || 3 != imageRequests(two) {
		t.Fatal("one execution node's failure poisoned another node's check", first, second)
	}
}

func TestImageManualCacheScope(t *testing.T) {
	f := imageNode(t, imageService("web"))
	s := imageServer(t, f, false)
	startChannels(t, s)
	_ = nextConnection(t, f.channel)
	imageReady(t, s, "home")
	key := imageKey{ImageKey: engine.ImageKey{Ref: "private.example/other:latest"}, node: "away", purpose: "explicit"}
	entry := &imageCacheEntry{result: engine.RegistryImage{Digest: "retained", CheckedAt: time.Now()}}
	s.hubMu.Lock()
	s.images.cache[key] = entry
	s.hubMu.Unlock()
	if w := perform(t, s, "POST", "/api/nodes/home/images/check", ""); 202 != w.Code {
		t.Fatal(w.Code)
	}
	s.hubMu.Lock()
	retained := s.images.cache[key] == entry
	s.hubMu.Unlock()
	if !retained {
		t.Fatal("node-wide manual request discarded another node's unrelated metadata")
	}
}

func TestImageFleetCooldown(t *testing.T) {
	one, two := imageNode(t), imageNode(t)
	one.metadata = func(r *http.Request) *http.Response {
		response := metadataResponse(r, 429, "provider limit")
		response.Header.Set("Retry-After", "3600")
		return response
	}
	s := imageServer(t, one, false)
	node := NodeConfig{Name: "away", URL: two.channel.server.URL, token: "agent-test-token", Fingerprint: testFingerprint(two.channel.server)}
	s.config.Nodes["away"] = node
	s.nodes["away"] = nodeClient(node)
	config, _ := json.Marshal(registry.DockerConfig{Auths: map[string]map[string]string{}})
	key := imageKey{ImageKey: engine.ImageKey{Ref: "docker.io/library/nginx:latest", ImagePlatform: engine.ImagePlatform{OS: "linux", Architecture: "amd64"}}, purpose: "explicit", node: "home"}
	first := s.registryImage(registryNodeContext(context.Background(), "home"), key, config, 0, time.Hour)
	key.node = "away"
	second := s.registryImage(registryNodeContext(context.Background(), "away"), key, config, 0, time.Hour)
	if nil == first.Cooldown || nil == second.Cooldown || !first.Cooldown.Until.Equal(second.Cooldown.Until) || 0 != imageRequests(two) {
		t.Fatal("another execution node bypassed the central account cooldown", first, second)
	}
}
