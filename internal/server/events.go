package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func (s *Server) workspaceEvents(w http.ResponseWriter, r *http.Request) {
	config, _ := s.snapshot()
	nativeOrigin := sameOrigin(r) && websocketOrigin(r, config.Global.Origin)
	allowTicket := ticketOrigin(r, config.Global.WebsocketOrigins)
	if !nativeOrigin && !allowTicket {
		apiError(w, http.StatusForbidden, "Cross-origin requests are not allowed.")
		return
	}
	options := &websocket.AcceptOptions{}
	if allowTicket {
		// OriginPatterns uses globs; keep IPv6 brackets literal.
		origin := strings.NewReplacer("[", "\\[", "]", "\\]").Replace(r.Header.Get("Origin"))
		options.OriginPatterns = []string{origin}
	}
	conn, err := websocket.Accept(w, r, options)
	if nil != err {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	authRequest := r
	if !nativeOrigin || !s.authenticated(r) {
		if !allowTicket {
			_ = writeChannel(r.Context(), conn, signInMessage())
			return
		}
		authRequest = s.authenticateWorkspaceTicket(r.Context(), conn)
		if nil == authRequest {
			return
		}
	}
	// A 64 KiB terminal input can expand sixfold when encoded as JSON.
	conn.SetReadLimit(6*(64<<10) + 1024)
	ctx, cancel := context.WithCancel(r.Context())
	var workers sync.WaitGroup
	subscriptions := make(map[string]*browserSubscription)
	defer func() {
		cancel()
		_ = conn.CloseNow()
		for _, subscription := range subscriptions {
			s.detachBrowser(subscription)
		}
		workers.Wait()
	}()
	frames, initial := s.subscribeWorkspace()
	defer func() {
		s.hubMu.Lock()
		delete(s.watchers, frames)
		s.hubMu.Unlock()
	}()
	commands := make(chan protocol.ChannelMessage, 64)
	outgoing := make(chan browserDelivery, 64)
	workers.Go(func() {
		defer cancel()
		for {
			var message protocol.ChannelMessage
			if nil != wsjson.Read(ctx, conn, &message) {
				return
			}
			select {
			case commands <- message:
			case <-ctx.Done():
				return
			}
		}
	})
	stopHeartbeat := socketHeartbeat(ctx, conn, "workspace browser at "+r.RemoteAddr)
	defer stopHeartbeat()
	authCheck := time.NewTicker(15 * time.Second)
	defer authCheck.Stop()
	for _, frame := range initial {
		if err := writeSocket(ctx, conn, frame); nil != err {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-authCheck.C:
			if !s.authenticated(authRequest) {
				_ = writeChannel(ctx, conn, signInMessage())
				return
			}
		case message := <-commands:
			if !s.authenticated(authRequest) {
				_ = writeChannel(ctx, conn, signInMessage())
				return
			}
			var response *protocol.ChannelMessage
			subscription := subscriptions[message.Stream]
			switch message.Type {
			case "workspace.sync":
				s.syncWorkspace(frames)
			case "stream.detach":
				if nil != subscription {
					s.detachBrowser(subscription)
					delete(subscriptions, message.Stream)
				}
			case "logs.attach", "stats.attach", "shell.attach":
				if nil != subscription {
					s.detachBrowser(subscription)
					delete(subscriptions, message.Stream)
				}
				subscription, response = s.attachBrowser(ctx, message)
				if nil != subscription {
					subscriptions[message.Stream] = subscription
					workers.Go(func() {
						subscription.forward(outgoing)
					})
				}
			case "shell.input", "shell.resize", "shell.close":
				response = s.browserShellCommand(ctx, subscription, message)
			default:
				response = browserError(message.Stream, 400, "Could not complete the request. Reload the page and try again.")
			}
			if nil != response && nil != writeChannel(ctx, conn, *response) {
				return
			}
		case delivery := <-outgoing:
			subscription := delivery.subscription
			if subscriptions[subscription.id] != subscription {
				continue
			}
			if delivery.done {
				s.detachBrowser(subscription)
				delete(subscriptions, subscription.id)
				continue
			}
			if nil != writeChannel(ctx, conn, delivery.message) {
				return
			}
		case frame, ok := <-frames:
			if !ok {
				return
			}
			if err := writeSocket(ctx, conn, frame); nil != err {
				return
			}
		}
	}
}

func (s *Server) authenticateWorkspaceTicket(ctx context.Context, conn *websocket.Conn) *http.Request {
	conn.SetReadLimit(1024)
	authContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	kind, data, err := conn.Read(authContext)
	cancel()
	if nil != err {
		return nil
	}
	var input struct {
		Type   string `json:"type"`
		Ticket string `json:"ticket"`
	}
	if websocket.MessageText == kind && nil == json.Unmarshal(data, &input) && "auth" == input.Type {
		if ticket, ok := s.consumeWebsocketTicket(input.Ticket); ok {
			authRequest := &http.Request{Header: ticket.auth}
			if s.authenticated(authRequest) {
				if nil == writeChannel(ctx, conn, protocol.ChannelMessage{Type: "auth.ok"}) {
					return authRequest
				}
				return nil
			}
		}
	}
	_ = writeChannel(ctx, conn, protocol.ChannelMessage{Type: "error", Status: 401, Error: "Invalid or expired connection ticket."})
	return nil
}

func signInMessage() protocol.ChannelMessage {
	return protocol.ChannelMessage{Type: "error", Status: 401, Error: "Sign in to continue."}
}

func browserError(id string, status int, message string) *protocol.ChannelMessage {
	return &protocol.ChannelMessage{Type: "error", Stream: id, Status: status, Error: message}
}

type nodeRuntime struct {
	snapshot map[string]json.RawMessage
	state    map[string]json.RawMessage
}

func (s *Server) broadcastLocked(frame []byte) {
	for subscriber := range s.watchers {
		select {
		case subscriber <- frame:
		default:
			delete(s.watchers, subscriber)
			close(subscriber)
		}
	}
}

func (s *Server) subscribeWorkspace() (chan []byte, [][]byte) {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	subscriber := make(chan []byte, 64)
	s.watchers[subscriber] = struct{}{}
	return subscriber, s.workspaceFramesLocked()
}

func (s *Server) syncWorkspace(subscriber chan []byte) {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if _, ok := s.watchers[subscriber]; !ok {
		return
	}
	for _, frame := range s.workspaceFramesLocked() {
		select {
		case subscriber <- frame:
		default:
			delete(s.watchers, subscriber)
			close(subscriber)
			return
		}
	}
}

func (s *Server) workspaceFramesLocked() [][]byte {
	var initial [][]byte
	ids := make([]string, 0, len(s.runtime))
	for id := range s.runtime {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		runtime := s.runtime[id]
		channel := s.channels[id]
		ready := nil == channel || (!channel.registryFailed && channel.registryRevision == channel.registryApplied)
		if nil != runtime.snapshot && ready {
			initial = append(initial, runtimeFrame("node.snapshot", runtime.snapshot))
		}
		if nil != runtime.state {
			initial = append(initial, runtimeFrame("node.state", runtime.state))
		}
	}
	return initial
}

func (s *Server) runtimeEvent(channel *nodeChannel, event string, data json.RawMessage) error {
	payload, err := runtimePayload(event, data)
	if nil != err {
		return err
	}
	if nil == payload {
		return io.ErrUnexpectedEOF
	}
	payload["node"], _ = json.Marshal(channel.id)
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if s.channels[channel.id] != channel {
		return nil
	}
	runtime := s.runtime[channel.id]
	ready := !channel.registryFailed && channel.registryRevision == channel.registryApplied
	switch event {
	case "stack.output":
		// Command output is live-only. Never add it to the runtime snapshot.
		s.broadcastLocked(runtimeFrame(event, payload))
		return nil
	case "stack.action":
		s.stackActionLocked(channel.id, payload)
		s.broadcastLocked(runtimeFrame(event, payload))
		return nil
	case "images.invalidated":
		var invalidated protocol.ImageInvalidated
		if nil != json.Unmarshal(data, &invalidated) || !stackName.MatchString(invalidated.Stack) {
			return io.ErrUnexpectedEOF
		}
		s.invalidateImagesLocked(channel.id, invalidated.Stack, invalidated.Refs)
		return nil
	case "node.snapshot":
		runtime.snapshot = payload
		if ready {
			s.stateLocked(channel.id, "connected", "")
		}
		var health struct {
			Interval string `json:"image_check_interval"`
		}
		_ = json.Unmarshal(payload["health"], &health)
		if interval, err := time.ParseDuration(health.Interval); nil == err && time.Minute <= interval {
			s.images.intervals[channel.id] = interval
		}
		var stacks []map[string]json.RawMessage
		_ = json.Unmarshal(payload["stacks"], &stacks)
		present := make(map[string]bool, len(stacks))
		for _, stack := range stacks {
			var name string
			_ = json.Unmarshal(stack["name"], &name)
			present[name] = true
			s.imageRuntimeLocked(channel.id, stack)
		}
		for key := range s.images.stacks {
			if channel.id == key.node && !present[key.stack] {
				delete(s.images.stacks, key)
			}
		}
		payload["stacks"], _ = json.Marshal(stacks)
	case "node.state":
		if !ready {
			return nil
		}
		runtime.state = payload
		if `"connected"` == string(payload["state"]) {
			s.queueNodeImagesLocked(channel.id)
		} else {
			s.pauseNodeImagesLocked(channel.id)
		}
	case "stack.runtime", "stack.removed":
		if "stack.runtime" == event {
			s.imageRuntimeLocked(channel.id, payload)
		} else {
			var name string
			_ = json.Unmarshal(payload["name"], &name)
			delete(s.images.stacks, imageStackKey{channel.id, name})
		}
		if nil != runtime.snapshot {
			var stacks []map[string]json.RawMessage
			_ = json.Unmarshal(runtime.snapshot["stacks"], &stacks)
			updated := make([]map[string]json.RawMessage, 0, len(stacks)+1)
			for _, stack := range stacks {
				if string(stack["name"]) != string(payload["name"]) {
					updated = append(updated, stack)
				}
			}
			if "stack.runtime" == event {
				stack := make(map[string]json.RawMessage, len(payload))
				for key, value := range payload {
					if "node" != key {
						stack[key] = value
					}
				}
				stack["loading"] = json.RawMessage(`false`)
				updated = append(updated, stack)
			}
			runtime.snapshot["stacks"], _ = json.Marshal(updated)
		}
	}
	if ready {
		s.broadcastLocked(runtimeFrame(event, payload))
	}
	return nil
}

func (s *Server) stateLocked(id, state, message string) {
	payload := map[string]json.RawMessage{}
	payload["node"], _ = json.Marshal(id)
	payload["state"], _ = json.Marshal(state)
	if "" != message {
		payload["message"], _ = json.Marshal(message)
	}
	s.runtime[id].state = payload
	s.broadcastLocked(runtimeFrame("node.state", payload))
}

func runtimeFrame(event string, payload any) []byte {
	data, _ := json.Marshal(payload)
	frame, _ := json.Marshal(protocol.ChannelMessage{Type: "event", Event: event, Payload: data})
	return frame
}

func runtimePayload(event string, data []byte) (map[string]json.RawMessage, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); nil != err {
		return nil, err
	}
	valid := false
	switch event {
	case "stack.output":
		var name string
		valid = nil == json.Unmarshal(payload["name"], &name) && stackName.MatchString(name) && isString(payload["output"])
		payload = onlyFields(payload, "name", "output")
	case "stack.action":
		var name, action, phase string
		valid = nil == json.Unmarshal(payload["name"], &name) && stackName.MatchString(name) &&
			nil == json.Unmarshal(payload["action"], &action) && "" != action &&
			nil == json.Unmarshal(payload["phase"], &phase) && ("start" == phase || "exit" == phase)
		if "exit" == phase {
			var code int
			valid = valid && 0 < len(payload["exit_code"]) && "null" != string(payload["exit_code"]) &&
				nil == json.Unmarshal(payload["exit_code"], &code)
		}
		if message, ok := payload["message"]; ok {
			valid = valid && isString(message)
		}
		payload = onlyFields(payload, "name", "action", "phase", "exit_code", "message")
	case "node.snapshot":
		var health map[string]json.RawMessage
		valid = nil == json.Unmarshal(payload["health"], &health) && isString(health["status"]) && isArray(payload["stacks"])
	case "stack.runtime":
		valid = isString(payload["name"]) && isString(payload["status"]) && isArray(payload["services"])
		if _, ok := payload["error"]; ok {
			payload["error"] = json.RawMessage(`"Stack status is unavailable."`)
		}
	case "stack.removed":
		valid = isString(payload["name"])
	case "images.invalidated":
		valid = isString(payload["stack"]) && isArray(payload["refs"])
	case "node.state":
		var state string
		valid = nil == json.Unmarshal(payload["state"], &state) && ("connected" == state || "unavailable" == state)
		if "unavailable" == state {
			payload["message"] = json.RawMessage(`"Node is unavailable."`)
		}
	}
	if !valid {
		return nil, io.ErrUnexpectedEOF
	}
	return payload, nil
}

func (s *Server) stackActionLocked(node string, payload map[string]json.RawMessage) {
	var name, phase string
	_ = json.Unmarshal(payload["name"], &name)
	_ = json.Unmarshal(payload["phase"], &phase)
	busy := "start" == phase
	key := imageStackKey{node, name}
	state := s.images.stacks[key]
	if nil == state {
		state = newImageStack()
		s.images.stacks[key] = state
	}
	state.busy = busy
	if !busy {
		s.invalidateImagesLocked(node, name, nil)
		state.pending = true
	}
	runtime := s.runtime[node]
	if nil == runtime || nil == runtime.snapshot {
		return
	}
	var stacks []map[string]json.RawMessage
	_ = json.Unmarshal(runtime.snapshot["stacks"], &stacks)
	for _, stack := range stacks {
		if string(stack["name"]) == string(payload["name"]) {
			stack["busy"], _ = json.Marshal(busy)
		}
	}
	runtime.snapshot["stacks"], _ = json.Marshal(stacks)
}

func isString(value json.RawMessage) bool {
	var result string
	return 0 < len(value) && '"' == value[0] && nil == json.Unmarshal(value, &result)
}

func isArray(value json.RawMessage) bool {
	var result []json.RawMessage
	return 0 < len(value) && '[' == value[0] && nil == json.Unmarshal(value, &result)
}
