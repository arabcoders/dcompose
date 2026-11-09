package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

type streamEvent struct {
	name string
	data []byte
}

type eventSubscriber struct {
	events chan streamEvent
	done   chan struct{}
}

func newSubscriber() *eventSubscriber {
	return &eventSubscriber{events: make(chan streamEvent, 64), done: make(chan struct{})}
}

// any (the interface{} alias) is only the JSON encoding boundary for the typed event payloads.
func event(name string, payload any) streamEvent {
	data, _ := json.Marshal(payload)
	return streamEvent{name, data}
}

// Callers serialize delivery with their state mutex. A slow reader must reconnect.
func (s *eventSubscriber) send(e streamEvent) {
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.events <- e:
	default:
		close(s.done)
	}
}

type stackRuntime struct {
	Name     string    `json:"name"`
	Busy     bool      `json:"busy"`
	Status   string    `json:"status"`
	Services []service `json:"services"`
	Error    string    `json:"error,omitempty"`
}

type inventoryRuntime struct {
	stackRuntime
	Loading bool `json:"loading"`
}

type nodeState struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

type runtimeEvents struct {
	mu          sync.Mutex
	started     bool
	subscribers map[*eventSubscriber]struct{}
	stacks      map[string]stackRuntime
	pending     map[string]bool
	inflight    map[string]bool
	state       nodeState
}

func newRuntimeEvents() *runtimeEvents {
	return &runtimeEvents{
		subscribers: make(map[*eventSubscriber]struct{}), stacks: make(map[string]stackRuntime),
		pending: make(map[string]bool), inflight: make(map[string]bool), state: nodeState{State: "unavailable"},
	}
}

func (h *runtimeEvents) publish(e streamEvent) {
	for s := range h.subscribers {
		s.send(e)
		select {
		case <-s.done:
			delete(h.subscribers, s)
		default:
		}
	}
}

func unknownRuntime(name string) stackRuntime {
	return stackRuntime{Name: name, Status: "unknown", Services: []service{}}
}

func (a *agent) publishRuntime(e streamEvent) {
	a.events.mu.Lock()
	a.events.publish(e)
	a.events.mu.Unlock()
}

func (a *agent) setStackBusy(name string, busy bool) {
	h := a.events
	h.mu.Lock()
	state, ok := h.stacks[name]
	if !ok {
		state = unknownRuntime(name)
	}
	state.Busy = busy
	h.stacks[name] = state
	h.publish(event("stack.runtime", state))
	h.mu.Unlock()
}

func (a *agent) inventory() ([]string, error) {
	entries, err := os.ReadDir(a.config.Global.StacksDir)
	if nil != err {
		return nil, err
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() || !stackName.MatchString(entry.Name()) {
			continue
		}
		dir, err := a.stackPath(entry.Name(), false)
		if nil != err {
			continue
		}
		if _, err := composeFile(dir); nil == err {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// The hub lock covers subscription and inventory reconciliation, so queued
// changes always follow this snapshot. Runtime commands never hold this lock.
func (a *agent) subscribeRuntime() (*eventSubscriber, []streamEvent, error) {
	h := a.events
	h.mu.Lock()
	names, err := a.inventory()
	if nil != err {
		h.mu.Unlock()
		return nil, nil, err
	}
	if nil != a.ctx.Err() {
		h.mu.Unlock()
		return nil, nil, fault(503, "node is shutting down")
	}
	s := newSubscriber()
	h.subscribers[s] = struct{}{}
	stacks := make([]inventoryRuntime, 0, len(names))
	present := make(map[string]bool, len(names))
	for _, name := range names {
		present[name] = true
		state, ok := h.stacks[name]
		if !ok {
			state = unknownRuntime(name)
			h.stacks[name] = state
		}
		stacks = append(stacks, inventoryRuntime{state, true})
		h.pending[name] = true
	}
	for name := range h.stacks {
		if !present[name] {
			delete(h.stacks, name)
			// A runtime query already in flight may still return the old stack.
			// Recheck after it completes so it cannot permanently resurrect it.
			h.pending[name] = true
			h.publish(event("stack.removed", struct {
				Name string `json:"name"`
			}{name}))
		}
	}
	initial := []streamEvent{event("node.snapshot", struct {
		Health nodeHealth         `json:"health"`
		Stacks []inventoryRuntime `json:"stacks"`
	}{a.health(), stacks}), event("node.state", h.state)}
	h.mu.Unlock()
	return s, initial, nil
}

func (a *agent) unsubscribeRuntime(s *eventSubscriber) {
	a.events.mu.Lock()
	delete(a.events.subscribers, s)
	a.events.mu.Unlock()
}

// Signals from saves and operations also discover additions/removals without
// requiring a runtime event (saving a draft creates no containers).
func (a *agent) signalRuntime(name string) {
	h := a.events
	h.mu.Lock()
	h.pending[name] = true
	h.mu.Unlock()
}

func (a *agent) runtimeStatus(ctx context.Context, name string) (stackRuntime, error) {
	s := unknownRuntime(name)
	dir, err := a.stackPath(name, false)
	if nil != err {
		return s, err
	}
	if _, err := composeFile(dir); nil != err {
		return s, err
	}
	services, err := a.containers(ctx, name, dir)
	a.events.mu.Lock()
	s.Busy = a.events.stacks[name].Busy
	a.events.mu.Unlock()
	if nil != err {
		// Never expose Compose stderr, which may contain interpolated secrets.
		s.Error = "container status is unavailable"
		return s, nil
	}
	s.Services, s.Status = services, stackStatus(services)
	return s, nil
}

type runtimeResult struct {
	name  string
	state stackRuntime
	err   error
}

// Runtime monitoring remains active even without an attached server.
func (a *agent) startBackground() {
	h := a.events
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.started && nil == a.ctx.Err() {
		h.started = true
		a.work.Add(2)
		go func() { defer a.work.Done(); a.refreshRuntimes() }()
		go func() { defer a.work.Done(); a.monitorRuntime() }()
	}
}

func (a *agent) refreshRuntimes() {
	jobs := make(chan string)
	results := make(chan runtimeResult, 4)
	var workers sync.WaitGroup
	for i := 0; 4 > i; i++ {
		workers.Go(func() {
			for name := range jobs {
				ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
				state, err := a.runtimeStatus(ctx, name)
				cancel()
				select {
				case results <- runtimeResult{name, state, err}:
				case <-a.ctx.Done():
					return
				}
			}
		})
	}
	defer func() { close(jobs); workers.Wait() }()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	h := a.events
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-tick.C:
			h.mu.Lock()
			for name := range h.pending {
				if h.inflight[name] {
					continue
				}
				select {
				case jobs <- name:
					h.inflight[name] = true
					delete(h.pending, name)
				default:
				}
			}
			h.mu.Unlock()
		case result := <-results:
			h.mu.Lock()
			delete(h.inflight, result.name)
			if nil != result.err {
				var apiErr *apiError
				if errors.As(result.err, &apiErr) && 404 == apiErr.code {
					delete(h.stacks, result.name)
					h.publish(event("stack.removed", struct {
						Name string `json:"name"`
					}{result.name}))
				} else {
					result.state.Error = "container status is unavailable"
				}
			}
			if nil == result.err || "" != result.state.Error {
				result.state.Busy = h.stacks[result.name].Busy
				h.stacks[result.name] = result.state
				h.publish(event("stack.runtime", result.state))
			}
			h.mu.Unlock()
		}
	}
}

func (a *agent) setNodeState(state nodeState) {
	h := a.events
	h.mu.Lock()
	if h.state != state {
		h.state = state
		h.publish(event("node.state", state))
	}
	h.mu.Unlock()
}

func (a *agent) runtimeEvent(line []byte) {
	name := a.engine.EventStack(line)
	dir, err := a.stackPath(name, false)
	if nil != err {
		return
	}
	if _, err := composeFile(dir); nil != err {
		return
	}
	a.signalRuntime(name)
}

func (a *agent) monitorRuntime() {
	delay := time.Second
	for nil == a.ctx.Err() {
		// One bounded daemon probe per connection attempt also establishes
		// connectivity for healthy nodes with no stacks or container events.
		ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
		err := a.engine.Probe(ctx)
		cancel()
		if nil == err {
			delay = time.Second
			a.listenRuntime(delay)
		} else if nil == a.ctx.Err() {
			slog.Warn(
				fmt.Sprintf("Could not probe %s for container events: %v. Retrying in %s.", a.config.Global.Engine, err, delay),
				"runtime",
				a.config.Global.Engine,
				"error",
				err,
				"retry_in",
				delay,
			)
		}
		if nil != a.ctx.Err() {
			return
		}
		a.setNodeState(nodeState{State: "unavailable", Message: "Runtime events unavailable. Reconnecting"})
		timer := time.NewTimer(delay)
		select {
		case <-a.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if nil != err {
			delay *= 2
			if 15*time.Second < delay {
				delay = 15 * time.Second
			}
		}
	}
}

func (a *agent) listenRuntime(retry time.Duration) {
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	reader, writer := io.Pipe()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			a.runtimeEvent(scanner.Bytes())
		}
		if err := scanner.Err(); nil != err {
			if context.Canceled == a.ctx.Err() {
				slog.Debug(
					fmt.Sprintf("Reading %s container events was canceled: %v", a.config.Global.Engine, err),
					"runtime",
					a.config.Global.Engine,
					"error",
					err,
				)
			} else {
				slog.Warn(
					fmt.Sprintf(
						"Could not read %s container events: %v. Restarting the subscription in %s.",
						a.config.Global.Engine,
						err,
						retry,
					),
					"runtime",
					a.config.Global.Engine,
					"error",
					err,
					"retry_in",
					retry,
				)
			}
			cancel()
		}
		_ = reader.Close()
	}()
	since := time.Now()
	cmd := command(ctx, a.engine.Events(since))
	var stderr cappedBuffer
	cmd.Stdout, cmd.Stderr = writer, &stderr
	if err := cmd.Start(); nil == err {
		a.setNodeState(nodeState{State: "connected"})
		// Refresh after reconnect to cover disconnect gaps. --since bridges
		// the CLI startup interval before its daemon subscription is ready.
		if names, err := a.inventory(); nil == err {
			for _, name := range names {
				a.signalRuntime(name)
			}
		}
		if err := waitCommand(ctx, cmd); nil == a.ctx.Err() {
			if nil != err {
				if detail := strings.TrimSpace(stderr.String()); "" != detail {
					slog.Warn(
						fmt.Sprintf(
							"The %s container event subscription stopped: %v: %s. Retrying in %s.",
							a.config.Global.Engine,
							err,
							detail,
							retry,
						),
						"runtime",
						a.config.Global.Engine,
						"error",
						err,
						"stderr",
						detail,
						"retry_in",
						retry,
					)
				} else {
					slog.Warn(
						fmt.Sprintf("The %s container event subscription stopped: %v. Retrying in %s.", a.config.Global.Engine, err, retry),
						"runtime",
						a.config.Global.Engine,
						"error",
						err,
						"retry_in",
						retry,
					)
				}
			} else {
				slog.Warn(
					fmt.Sprintf(
						"The %s container event subscription ended with exit code 0. Retrying in %s.",
						a.config.Global.Engine,
						retry,
					),
					"runtime",
					a.config.Global.Engine,
					"exit_code",
					0,
					"retry_in",
					retry,
				)
			}
		}
	} else {
		if context.Canceled == a.ctx.Err() {
			slog.Debug(
				fmt.Sprintf("Starting the %s container event subscription was canceled: %v", a.config.Global.Engine, err),
				"runtime",
				a.config.Global.Engine,
				"error",
				err,
			)
		} else {
			slog.Warn(
				fmt.Sprintf("Could not start the %s container event subscription: %v. Retrying in %s.", a.config.Global.Engine, err, retry),
				"runtime",
				a.config.Global.Engine,
				"error",
				err,
				"retry_in",
				retry,
			)
		}
	}
	_ = writer.Close()
	<-readDone
}
