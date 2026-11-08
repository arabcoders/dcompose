package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/engine/registry"
	"github.com/arabcoders/dcompose/internal/protocol"
)

const imageErrorRetry = 5 * time.Minute

type imageStackKey struct{ node, stack string }
type imageKey struct {
	engine.ImageKey
	purpose string
	node    string
}

type checkedImage struct {
	id, image, localID string
	key                imageKey
	update             engine.ImageUpdate
}

func (c checkedImage) available(service engine.Service) bool {
	return c.id == service.ID && c.image == service.Image && "available" == c.update.Status
}

type imageCacheEntry struct {
	done   chan struct{}
	result engine.RegistryImage
}

type imageStack struct {
	services    []engine.Service
	checked     map[string]checkedImage
	next        map[string]time.Time
	timerIDs    map[string]string
	custom      map[string]bool
	autoPending map[string]string
	autoNext    map[string]time.Time
	manual      bool
	selected    map[string]bool
	pending     bool
	inflight    bool
	revision    uint64
	busy        bool
	inspection  *protocol.ImageInspectResponse
}

type imageChecks struct {
	stacks       map[imageStackKey]*imageStack
	intervals    map[string]time.Duration
	cache        map[imageKey]*imageCacheEntry
	forceRefs    map[string]bool
	now          func() time.Time
	cooldownFile string
	persistMu    sync.Mutex
	persisted    []byte
}

func newImageChecks() *imageChecks {
	return &imageChecks{
		stacks: make(map[imageStackKey]*imageStack), intervals: make(map[string]time.Duration),
		cache: make(map[imageKey]*imageCacheEntry), forceRefs: make(map[string]bool), now: time.Now,
	}
}

func newImageStack() *imageStack {
	return &imageStack{
		checked: make(map[string]checkedImage), next: make(map[string]time.Time), timerIDs: make(map[string]string),
		custom: make(map[string]bool), autoPending: make(map[string]string), autoNext: make(map[string]time.Time),
	}
}

func (c *imageChecks) interval(node string) time.Duration {
	if interval := c.intervals[node]; 0 < interval {
		return interval
	}
	return 6 * time.Hour
}

func (s *Server) imageReadyLocked(node string) bool {
	channel := s.channels[node]
	if nil == channel || nil == channel.conn || nil != channel.ctx.Err() || channel.registryFailed || channel.registryApplied != s.registrySync.Revision {
		return false
	}
	runtime := s.runtime[node]
	return nil != runtime && `"connected"` == string(runtime.state["state"])
}

func (s *Server) backgroundImageLocked(state *imageStack, service engine.Service) bool {
	ref := state.checked[service.Name].key.Ref
	for _, credential := range s.registrySync.Registries {
		if credential.BackgroundChecks && ("" == ref || registry.RegistryHost(credential.Registry) == registry.RegistryHost(ref)) {
			return true
		}
	}
	return false
}

func (s *Server) imageRuntimeLocked(node string, payload map[string]json.RawMessage) {
	var name string
	var services []engine.Service
	if nil != json.Unmarshal(payload["name"], &name) || nil != json.Unmarshal(payload["services"], &services) {
		return
	}
	key := imageStackKey{node, name}
	state := s.images.stacks[key]
	if nil == state {
		state = newImageStack()
		s.images.stacks[key] = state
	}
	changed := false
	for _, service := range services {
		for _, previous := range state.services {
			if previous.Name == service.Name && previous.ID != service.ID {
				checked := state.checked[service.Name]
				if "" != checked.key.Ref {
					s.images.forceRefs[checked.key.Ref] = true
					for cached := range s.images.cache {
						if cached.Ref == checked.key.Ref {
							delete(s.images.cache, cached)
						}
					}
				}
				delete(state.checked, service.Name)
				changed = true
			}
		}
	}
	state.services = services
	_ = json.Unmarshal(payload["busy"], &state.busy)
	var failure string
	_ = json.Unmarshal(payload["error"], &failure)
	if "" != failure {
		state.manual = false
		state.selected = nil
		state.pending = false
		state.revision++
	} else {
		state.pending = true
		if changed {
			state.revision++
			state.inspection = nil
		}
	}
	s.decorateImagesLocked(key, payload)
}

func (s *Server) decorateImagesLocked(key imageStackKey, payload map[string]json.RawMessage) {
	var services []engine.Service
	if nil != json.Unmarshal(payload["services"], &services) {
		return
	}
	state := s.images.stacks[key]
	for i := range services {
		service := &services[i]
		service.ImageUpdate = nil
		if nil == state {
			continue
		}
		checked, exists := state.checked[service.Name]
		if exists && checked.id == service.ID && checked.image == service.Image && "" != checked.update.Status {
			update := checked.update
			service.ImageUpdate = &update
		} else if s.imageReadyLocked(key.node) && ((state.manual && (nil == state.selected || state.selected[service.Name])) || state.custom[service.Name] || ("" == service.CheckInterval && s.backgroundImageLocked(state, *service))) {
			service.ImageUpdate = &engine.ImageUpdate{Status: "checking"}
		}
	}
	payload["services"], _ = json.Marshal(services)
}

func (s *Server) publishImagesLocked(key imageStackKey) {
	runtime := s.runtime[key.node]
	if nil == runtime || nil == runtime.snapshot {
		return
	}
	var stacks []map[string]json.RawMessage
	_ = json.Unmarshal(runtime.snapshot["stacks"], &stacks)
	for _, stack := range stacks {
		var name string
		_ = json.Unmarshal(stack["name"], &name)
		if key.stack != name {
			continue
		}
		if state := s.images.stacks[key]; nil != state && nil != state.services {
			stack["services"], _ = json.Marshal(state.services)
		}
		s.decorateImagesLocked(key, stack)
		payload := make(map[string]json.RawMessage, len(stack)+1)
		for field, value := range stack {
			payload[field] = value
		}
		payload["node"], _ = json.Marshal(key.node)
		s.broadcastLocked(runtimeFrame("stack.runtime", payload))
		break
	}
	runtime.snapshot["stacks"], _ = json.Marshal(stacks)
}

func (s *Server) queueNodeImagesLocked(node string) {
	for key, state := range s.images.stacks {
		if node == key.node {
			state.pending = true
		}
	}
}

func (s *Server) pauseNodeImagesLocked(node string) {
	for key, state := range s.images.stacks {
		if node == key.node {
			state.manual = false
			state.selected = nil
			state.inspection = nil
			clear(state.custom)
			state.pending = false
			state.revision++
			s.publishImagesLocked(key)
		}
	}
}

func (s *Server) removeNodeImagesLocked(node string) {
	for key := range s.images.stacks {
		if node == key.node {
			delete(s.images.stacks, key)
		}
	}
	delete(s.images.intervals, node)
}

func (s *Server) resetImagesLocked() {
	clear(s.images.cache)
	for key, state := range s.images.stacks {
		clear(state.checked)
		clear(state.next)
		clear(state.timerIDs)
		clear(state.autoPending)
		state.inspection = nil
		state.revision++
		state.pending = true
		s.publishImagesLocked(key)
	}
}

func (s *Server) invalidateImagesLocked(node, stack string, refs []string) {
	selected := make(map[string]bool)
	for _, ref := range refs {
		selected[ref] = true
	}
	if state := s.images.stacks[imageStackKey{node, stack}]; nil != state {
		for _, checked := range state.checked {
			if "" != checked.key.Ref {
				selected[checked.key.Ref] = true
			}
		}
	}
	for ref := range selected {
		s.images.forceRefs[ref] = true
	}
	for key := range s.images.cache {
		if selected[key.Ref] {
			delete(s.images.cache, key)
		}
	}
	for key, state := range s.images.stacks {
		whole := node == key.node && stack == key.stack
		changed := whole
		for name, checked := range state.checked {
			if whole || selected[checked.key.Ref] {
				delete(state.checked, name)
				changed = true
			}
		}
		if changed {
			state.revision++
			state.inspection = nil
			state.pending = true
			s.publishImagesLocked(key)
		}
	}
}

func (s *Server) scheduleImagesLocked() {
	now := s.images.now()
	for key, state := range s.images.stacks {
		if !s.imageReadyLocked(key.node) {
			continue
		}
		for _, service := range state.services {
			checked := state.checked[service.Name]
			available := checked.available(service)
			automatic := available && service.AutoUpdate && "running" == service.State && "" != service.Service
			if "" != service.CheckInterval {
				interval, err := time.ParseDuration(service.CheckInterval)
				if nil == err && time.Minute <= interval && (state.timerIDs[service.Name] != service.ID || !now.Before(state.next[service.Name])) {
					state.timerIDs[service.Name] = service.ID
					state.next[service.Name] = now.Add(interval)
					if !available {
						state.custom[service.Name] = true
						state.pending = true
					} else if automatic {
						state.autoPending[service.Service] = service.ID
					}
				}
			} else if s.backgroundImageLocked(state, service) {
				if automatic {
					state.autoPending[service.Service] = service.ID
				} else if !available && ("" != checked.key.purpose || !s.imageFreshLocked(checked, service, s.images.interval(key.node))) {
					state.pending = true
				}
			}
			if "" != service.ID && state.autoPending[service.Service] == service.ID && !now.Before(state.autoNext[service.Service]) {
				state.pending = true
			}
		}
	}
}

func (s *Server) imageWorkLocked(key imageStackKey, state *imageStack) bool {
	if state.busy {
		return false
	}
	if state.manual || 0 < len(state.custom) {
		return true
	}
	for _, service := range state.services {
		checked := state.checked[service.Name]
		if !checked.available(service) && "" == service.CheckInterval && s.backgroundImageLocked(state, service) && ("" != checked.key.purpose || !s.imageFreshLocked(checked, service, s.images.interval(key.node))) {
			return true
		}
		if "" != service.ID && state.autoPending[service.Service] == service.ID && !s.images.now().Before(state.autoNext[service.Service]) {
			return true
		}
	}
	return false
}

func (s *Server) imageFreshLocked(checked checkedImage, service engine.Service, maxAge time.Duration) bool {
	if checked.id != service.ID || checked.image != service.Image || nil == checked.update.CheckedAt {
		return false
	}
	if entry := s.images.cache[checked.key]; "" != checked.key.Ref {
		return nil != entry && nil == entry.done && entry.result.CheckedAt.Equal(*checked.update.CheckedAt) && s.registryFreshLocked(entry.result, maxAge)
	}
	if "error" == checked.update.Status {
		maxAge = imageErrorRetry
	}
	return s.images.now().Before(checked.update.CheckedAt.Add(maxAge))
}

func (s *Server) registryFreshLocked(result engine.RegistryImage, maxAge time.Duration) bool {
	if nil != result.Cooldown {
		return s.images.now().Before(result.Cooldown.Until)
	}
	if "error" == result.Status {
		maxAge = imageErrorRetry
	}
	return s.images.now().Before(result.CheckedAt.Add(maxAge))
}

func (s *Server) watchImages() {
	jobs := make(chan imageStackKey)
	var workers sync.WaitGroup
	for i := 0; 4 > i; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for key := range jobs {
				s.refreshImages(key)
				s.hubMu.Lock()
				if state := s.images.stacks[key]; nil != state {
					state.inflight = false
				}
				s.hubMu.Unlock()
			}
		}()
	}
	defer func() { close(jobs); workers.Wait() }()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.hubContext.Done():
			return
		case <-tick.C:
			s.hubMu.Lock()
			s.scheduleImagesLocked()
			for key, state := range s.images.stacks {
				if state.inflight || state.busy || !s.imageReadyLocked(key.node) || !s.imageWorkLocked(key, state) || !state.pending {
					continue
				}
				select {
				case jobs <- key:
					state.pending = false
					state.inflight = true
				default:
				}
			}
			s.hubMu.Unlock()
		}
	}
}

func (s *Server) imageRequest(ctx context.Context, node, method, path string, input, output interface{}) error {
	var body io.Reader
	if nil != input {
		data, err := json.Marshal(input)
		if nil != err {
			return err
		}
		body = bytes.NewReader(data)
	}
	response, err := s.connect(ctx, node, method, path, body)
	if nil != err {
		return err
	}
	defer response.Body.Close()
	if 200 > response.StatusCode || 300 <= response.StatusCode {
		return readNodeHTTPError(response, "image update request")
	}
	return json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(output)
}

func (s *Server) refreshImages(key imageStackKey) {
	s.hubMu.Lock()
	state, channel := s.images.stacks[key], s.channels[key.node]
	if nil == state || state.busy || !s.imageReadyLocked(key.node) {
		s.hubMu.Unlock()
		return
	}
	revision := state.revision
	manual := state.manual
	selected := state.selected
	custom := make(map[string]bool, len(state.custom))
	for name := range state.custom {
		custom[name] = true
	}
	credentials := s.registrySync.Registries
	interval := s.images.interval(key.node)
	inspection := state.inspection
	state.inspection = nil
	autoPending := make(map[string]string, len(state.autoPending))
	for service, id := range state.autoPending {
		autoPending[service] = id
	}
	nodeContext := channel.imageContext
	s.hubMu.Unlock()
	ctx, cancel := context.WithTimeout(nodeContext, 45*time.Second)
	defer cancel()
	if err := s.awaitRegistries(ctx, key.node); nil != err {
		s.imageCheckFailed(key, revision, err)
		return
	}
	var inspected protocol.ImageInspectResponse
	if nil != inspection {
		inspected = *inspection
	} else {
		if err := s.imageRequest(ctx, key.node, "POST", "/images/inspect", protocol.ImageInspectRequest{Stack: key.stack}, &inspected); nil != err {
			s.imageCheckFailed(key, revision, err)
			return
		}
	}
	background, explicit, err := registry.BuildConfigs(credentials)
	if nil != err {
		s.imageCheckFailed(key, revision, err)
		return
	}
	services := make([]engine.Service, 0, len(inspected.Images))
	for _, image := range inspected.Images {
		service := image.Service
		service.ID = image.ID
		services = append(services, service)
		if manual && nil != selected && !selected[service.Name] {
			continue
		}
		purpose := ""
		maxAge := interval
		config := background
		if manual || custom[service.Name] || ("" != service.CheckInterval && "" != service.ID && autoPending[service.Service] == service.ID) {
			purpose = "explicit"
			config = explicit
			if manual {
				maxAge = 0
			} else {
				maxAge, _ = time.ParseDuration(service.CheckInterval)
			}
		} else if "" != service.CheckInterval {
			continue
		}
		allowed := "explicit" == purpose
		if "" != image.Target.Status && 0 < len(background.Auths) {
			allowed = true
		}
		for host := range background.Auths {
			if registry.RegistryHost(host) == registry.RegistryHost(image.Target.Key.Ref) {
				allowed = true
			}
		}
		s.hubMu.Lock()
		if s.images.stacks[key] != state || revision != state.revision || !s.imageReadyLocked(key.node) {
			s.hubMu.Unlock()
			return
		}
		previous := state.checked[service.Name]
		if previous.available(service) && previous.localID == image.Target.LocalID {
			s.hubMu.Unlock()
			continue
		}
		age := maxAge
		if 0 == age {
			age = interval
		}
		fresh := !custom[service.Name] && previous.key.purpose == purpose && s.imageFreshLocked(previous, service, age)
		if !allowed {
			at := s.images.now().UTC()
			state.checked[service.Name] = checkedImage{id: service.ID, image: service.Image, key: imageKey{ImageKey: image.Target.Key, node: key.node}, update: engine.ImageUpdate{CheckedAt: &at}}
			s.hubMu.Unlock()
			continue
		}
		if "" != previous.localID && previous.localID != image.Target.LocalID {
			s.images.forceRefs[image.Target.Key.Ref] = true
			for cached := range s.images.cache {
				if cached.Ref == image.Target.Key.Ref {
					delete(s.images.cache, cached)
				}
			}
			fresh = false
		}
		s.hubMu.Unlock()
		if fresh {
			continue
		}
		checked := checkedImage{id: service.ID, image: service.Image, localID: image.Target.LocalID, key: imageKey{ImageKey: image.Target.Key, purpose: purpose, node: key.node}}
		result := engine.RegistryImage{Status: image.Target.Status, Reason: image.Target.Reason, CheckedAt: s.images.now().UTC()}
		if "" == result.Status {
			data, _ := json.Marshal(config)
			result = s.registryImage(registryNodeContext(ctx, key.node), checked.key, data, maxAge, interval)
		}
		status := result.Status
		if "" == status {
			status = "available"
			if image.Target.LocalID == result.Digest {
				status = "current"
			}
		}
		checked.update = engine.ImageUpdate{Status: status, Reason: result.Reason, CheckedAt: &result.CheckedAt}
		s.hubMu.Lock()
		if s.images.stacks[key] != state || revision != state.revision || !s.imageReadyLocked(key.node) {
			s.hubMu.Unlock()
			return
		}
		state.checked[service.Name] = checked
		if "available" != status && state.autoPending[service.Service] == service.ID {
			delete(state.autoPending, service.Service)
		}
		if custom[service.Name] && "error" == status {
			state.next[service.Name] = s.images.now().Add(imageErrorRetry)
			if nil != result.Cooldown {
				state.next[service.Name] = result.Cooldown.Until
			}
		}
		s.publishImagesLocked(key)
		s.hubMu.Unlock()
	}
	s.hubMu.Lock()
	if s.images.stacks[key] != state || revision != state.revision || !s.imageReadyLocked(key.node) {
		s.hubMu.Unlock()
		return
	}
	state.services = services
	if !manual {
		s.rememberAutoUpdatesLocked(state, custom)
	}
	state.manual = false
	state.selected = nil
	clear(state.custom)
	s.publishImagesLocked(key)
	s.hubMu.Unlock()
	if !manual {
		s.startImageUpdate(ctx, key)
	}
}

func (s *Server) registryImage(ctx context.Context, key imageKey, config []byte, maxAge, interval time.Duration) engine.RegistryImage {
	for {
		s.hubMu.Lock()
		entry := s.images.cache[key]
		age := maxAge
		if 0 == age {
			age = interval
		}
		if nil != entry {
			if nil != entry.done {
				done := entry.done
				s.hubMu.Unlock()
				select {
				case <-done:
					continue
				case <-ctx.Done():
					return engine.RegistryImage{Status: "error", Reason: "registry_error", CheckedAt: s.images.now().UTC()}
				}
			}
			if s.registryFreshLocked(entry.result, age) {
				result := entry.result
				s.hubMu.Unlock()
				return result
			}
		}
		entry = &imageCacheEntry{done: make(chan struct{})}
		s.images.cache[key] = entry
		if s.images.forceRefs[key.Ref] {
			maxAge = 0
		}
		s.hubMu.Unlock()
		result := s.registryClient.InspectConfig(ctx, key.ImageKey, config, maxAge)
		s.saveImageCooldowns()
		s.hubMu.Lock()
		current := s.images.cache[key] == entry
		close(entry.done)
		entry.done = nil
		if current {
			if nil != ctx.Err() {
				delete(s.images.cache, key)
			} else {
				entry.result = result
				if "error" != result.Status {
					delete(s.images.forceRefs, key.Ref)
				}
			}
		}
		s.hubMu.Unlock()
		if current || nil != ctx.Err() {
			return result
		}
	}
}

func (s *Server) imageCheckFailed(key imageStackKey, revision uint64, err error) {
	if errors.Is(err, errNodePaused) {
		return
	}
	level := slog.LevelWarn
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		level = slog.LevelDebug
	}
	slog.Log(context.Background(), level, fmt.Sprintf("Could not check image updates for stack %q on node %q: %s", key.stack, key.node, safeTransportError(err)))
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	state := s.images.stacks[key]
	if nil == state || revision != state.revision {
		return
	}
	now := s.images.now().UTC()
	for _, service := range state.services {
		if state.manual && nil != state.selected && !state.selected[service.Name] {
			continue
		}
		if state.checked[service.Name].available(service) {
			continue
		}
		state.checked[service.Name] = checkedImage{id: service.ID, image: service.Image, update: engine.ImageUpdate{Status: "error", Reason: "inspect_error", CheckedAt: &now}}
		if state.custom[service.Name] {
			state.next[service.Name] = now.Add(imageErrorRetry)
		}
	}
	state.manual = false
	state.selected = nil
	clear(state.custom)
	s.publishImagesLocked(key)
}

func (s *Server) requestImageChecks(w http.ResponseWriter, r *http.Request, node, stack string, body []byte) {
	config, _ := s.snapshot()
	if config.Nodes[node].Paused {
		apiError(w, http.StatusConflict, errNodePaused.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.awaitRegistries(ctx, node); nil != err {
		if errors.Is(err, errNodePaused) {
			proxyError(w, err)
			return
		}
		apiError(w, 503, "Registry credentials are not synchronized with this node. Check registry settings or reconnect the node.")
		return
	}
	s.hubMu.Lock()
	channel := s.channels[node]
	ready := s.imageReadyLocked(node)
	var nodeContext context.Context
	if ready {
		nodeContext = channel.imageContext
	}
	s.hubMu.Unlock()
	if !ready {
		apiError(w, 503, "Node is unavailable. Try again when it reconnects.")
		return
	}
	stop := context.AfterFunc(nodeContext, cancel)
	defer stop()
	var input struct {
		Container string `json:"container"`
	}
	_ = json.Unmarshal(body, &input)
	var payload map[string]json.RawMessage
	var inspected protocol.ImageInspectResponse
	if "" != stack {
		if err := s.imageRequest(ctx, node, "POST", "/images/inspect", protocol.ImageInspectRequest{Stack: stack}, &inspected); nil != err {
			if errors.Is(err, errNodePaused) {
				proxyError(w, err)
				return
			}
			level := slog.LevelError
			if nil != ctx.Err() {
				level = slog.LevelDebug
			}
			slog.Log(ctx, level, fmt.Sprintf("Could not request image updates for stack %q on node %q: %s", stack, node, safeTransportError(err)))
			var upstream nodeHTTPError
			if errors.As(err, &upstream) && 404 == upstream.status {
				apiError(w, 404, "Stack not found.")
			} else {
				proxyError(w, err)
			}
			return
		}
		services := make([]engine.Service, 0, len(inspected.Images))
		for _, image := range inspected.Images {
			service := image.Service
			service.ID = image.ID
			services = append(services, service)
		}
		payload = make(map[string]json.RawMessage)
		payload["name"], _ = json.Marshal(stack)
		payload["services"], _ = json.Marshal(services)
		if "" != input.Container {
			found := false
			for _, service := range services {
				if input.Container == service.Name {
					found = true
				}
			}
			if !found {
				apiError(w, 404, "Container not found in stack.")
				return
			}
		}
	}
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if !s.imageReadyLocked(node) {
		apiError(w, 503, "Node is unavailable. Try again when it reconnects.")
		return
	}
	if nil != payload {
		s.imageRuntimeLocked(node, payload)
		s.images.stacks[imageStackKey{node, stack}].inspection = &inspected
	}
	refs := make(map[string]bool)
	images := make(map[string]bool)
	for _, image := range inspected.Images {
		if "" == input.Container || input.Container == image.Service.Name {
			service := image.Service
			service.ID = image.ID
			if s.images.stacks[imageStackKey{node, stack}].checked[service.Name].available(service) {
				continue
			}
			refs[image.Target.Key.Ref] = true
		}
	}
	for key, state := range s.images.stacks {
		if node != key.node || ("" != stack && stack != key.stack) {
			continue
		}
		for _, service := range state.services {
			if ("" != input.Container && input.Container != service.Name) || state.checked[service.Name].available(service) {
				continue
			}
			images[service.Image] = true
			refs[state.checked[service.Name].key.Ref] = true
			delete(state.checked, service.Name)
		}
		if "" == input.Container || (state.manual && nil == state.selected) {
			state.selected = nil
		} else {
			selected := make(map[string]bool)
			for name := range state.selected {
				selected[name] = true
			}
			selected[input.Container] = true
			state.selected = selected
		}
		state.manual = true
		state.pending = true
		state.revision++
		s.publishImagesLocked(key)
	}
	for _, state := range s.images.stacks {
		for _, checked := range state.checked {
			if images[checked.image] {
				refs[checked.key.Ref] = true
			}
		}
	}
	for key := range s.images.cache {
		if refs[key.Ref] || images[key.Ref] {
			delete(s.images.cache, key)
		}
	}
	jsonResponse(w, 202, map[string]string{"status": "checking"})
}

func (s *Server) rememberAutoUpdatesLocked(state *imageStack, custom map[string]bool) {
	for target, id := range state.autoPending {
		found := false
		for _, service := range state.services {
			if service.Service == target && service.ID == id && service.AutoUpdate && "running" == service.State {
				found = true
			}
		}
		if !found {
			delete(state.autoPending, target)
		}
	}
	for _, service := range state.services {
		checked := state.checked[service.Name]
		if service.AutoUpdate && "running" == service.State && "" != service.Service && (custom[service.Name] || ("" == service.CheckInterval && "" == checked.key.purpose)) && "available" == checked.update.Status && checked.id == service.ID {
			state.autoPending[service.Service] = service.ID
		}
	}
}

func (s *Server) startImageUpdate(ctx context.Context, key imageStackKey) {
	s.hubMu.Lock()
	state := s.images.stacks[key]
	if nil == state || state.busy || !s.imageReadyLocked(key.node) {
		s.hubMu.Unlock()
		return
	}
	service := ""
	for _, candidate := range state.services {
		if candidate.AutoUpdate && "running" == candidate.State && "" != candidate.Service && state.autoPending[candidate.Service] == candidate.ID && !s.images.now().Before(state.autoNext[candidate.Service]) {
			service = candidate.Service
			state.autoNext[service] = s.images.now().Add(imageErrorRetry)
			break
		}
	}
	if "" != service {
		state.busy = true
	}
	s.hubMu.Unlock()
	if "" == service {
		return
	}
	accepted := false
	defer func() {
		if !accepted {
			s.hubMu.Lock()
			if s.images.stacks[key] == state {
				state.busy = false
			}
			s.hubMu.Unlock()
		}
	}()
	if err := s.awaitRegistries(ctx, key.node); nil != err {
		return
	}
	var response actionResponse
	input := struct {
		Action    string `json:"action"`
		Service   string `json:"service"`
		Automatic bool   `json:"automatic"`
	}{"update", service, true}
	err := s.imageRequest(ctx, key.node, "POST", "/stacks/"+url.PathEscape(key.stack)+"/actions", input, &response)
	if nil != err {
		level := slog.LevelWarn
		var upstream nodeHTTPError
		if errors.As(err, &upstream) && 409 == upstream.status {
			level = slog.LevelDebug
		}
		slog.Log(ctx, level, fmt.Sprintf("Automatic update could not start for stack %q, service %q on node %q: %s", key.stack, service, key.node, safeTransportError(err)))
		return
	}
	if "accepted" != response.Status {
		slog.Error(fmt.Sprintf("Automatic update for stack %q, service %q on node %q was not accepted: invalid node response", key.stack, service, key.node), "node", key.node, "stack", key.stack, "service", service)
		return
	}
	accepted = true
	if "" != response.Relay {
		s.startRelay(key.node, response.Relay)
	}
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if s.images.stacks[key] == state {
		s.invalidateImagesLocked(key.node, key.stack, nil)
	}
}

func imageStackRoute(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if 2 <= len(parts) && "stacks" == parts[0] {
		return parts[1]
	}
	return ""
}
