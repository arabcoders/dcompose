package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func (a *agent) channel(w http.ResponseWriter, r *http.Request) {
	subscriber, initial, err := a.subscribeRuntime()
	if nil != err {
		respondError(w, r, err)
		return
	}
	defer a.unsubscribeRuntime(subscriber)
	conn, err := websocket.Accept(w, r, nil)
	if nil != err {
		if context.Canceled == r.Context().Err() {
			slog.Debug(
				fmt.Sprintf("DCompose connection setup from %s was canceled: %v", r.RemoteAddr, err),
				"peer",
				r.RemoteAddr,
				"error",
				err,
			)
		} else {
			slog.Error(
				fmt.Sprintf("Could not establish an authenticated DCompose connection from %s: %v", r.RemoteAddr, err),
				"peer",
				r.RemoteAddr,
				"error",
				err,
			)
		}
		return
	}
	conn.SetReadLimit(8 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(a.ctx, cancel)
	defer stop()
	var workers sync.WaitGroup
	defer func() {
		cancel()
		_ = conn.CloseNow()
		workers.Wait()
	}()
	slog.Debug(
		fmt.Sprintf("DCompose peer %s connected and authenticated to node %q.", r.RemoteAddr, a.config.Global.NodeID),
		"peer",
		r.RemoteAddr,
		"node",
		a.config.Global.NodeID,
	)

	outgoing := make(chan protocol.ChannelMessage, 64)
	commands := make(chan protocol.ChannelMessage, 64)
	// PTY writes have a two-second deadline. Keep them off the socket reader
	// and writer so runtime traffic and pong processing remain independent.
	workers.Add(2)
	go func() {
		defer workers.Done()
		defer cancel()
		for {
			var message protocol.ChannelMessage
			if err := wsjson.Read(ctx, conn, &message); nil != err {
				a.channelError(ctx, r.RemoteAddr, "reading a request", err)
				return
			}
			select {
			case commands <- message:
			case <-ctx.Done():
				return
			default:
				slog.Warn(
					fmt.Sprintf("Closing DCompose connection from %s because its %d-command buffer is full.", r.RemoteAddr, cap(commands)),
					"peer",
					r.RemoteAddr,
					"capacity",
					cap(commands),
				)
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		a.channelCommands(ctx, r.RemoteAddr, commands, outgoing, cancel)
	}()
	write := func(message protocol.ChannelMessage) bool {
		writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if err := wsjson.Write(writeCtx, conn, message); nil != err {
			a.channelError(ctx, r.RemoteAddr, "sending an update", err)
			return false
		}
		return true
	}
	for _, e := range initial {
		if !write(protocol.ChannelMessage{Type: "event", Event: e.name, Payload: e.data}) {
			return
		}
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	// Logs use their own bounded queue and small batches so a noisy node cannot
	// fill the command/status queues. Disconnected records stay queued locally.
	logs := time.NewTicker(250 * time.Millisecond)
	defer logs.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-subscriber.done:
			slog.Warn(
				fmt.Sprintf(
					"Closing DCompose connection from %s because its %d-status-update buffer is full.",
					r.RemoteAddr,
					cap(subscriber.events),
				),
				"peer",
				r.RemoteAddr,
				"capacity",
				cap(subscriber.events),
			)
			return
		case e := <-subscriber.events:
			if !write(protocol.ChannelMessage{Type: "event", Event: e.name, Payload: e.data}) {
				return
			}
		case message := <-outgoing:
			if !write(message) {
				return
			}
		case <-logs.C:
			if entries := a.logs.take(); 0 != len(entries) {
				payload, _ := json.Marshal(protocol.NodeLogs{Entries: entries})
				if !write(protocol.ChannelMessage{Type: "node.logs", Payload: payload}) {
					return
				}
			}
		case <-heartbeat.C:
			pingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			stop()
			if nil != err {
				a.channelError(ctx, r.RemoteAddr, "checking the connection", err)
				return
			}
		}
	}
}

func (a *agent) channelError(ctx context.Context, peer, operation string, err error) {
	if nil != ctx.Err() {
		return
	}
	status := websocket.CloseStatus(err)
	if websocket.StatusNormalClosure == status || websocket.StatusGoingAway == status {
		slog.Debug(
			fmt.Sprintf(
				"DCompose peer %s disconnected from node %q: WebSocket closed with code %d (%s).",
				peer,
				a.config.Global.NodeID,
				status,
				status.String(),
			),
			"peer",
			peer,
			"node",
			a.config.Global.NodeID,
		)
		return
	}
	if errors.Is(err, io.EOF) {
		slog.Info(
			fmt.Sprintf("DCompose peer %s disconnected from node %q without a WebSocket close handshake.", peer, a.config.Global.NodeID),
			"peer",
			peer,
			"node",
			a.config.Global.NodeID,
		)
		return
	}
	detail := err.Error()
	if errors.Is(err, io.ErrUnexpectedEOF) {
		detail = "connection ended during an incomplete WebSocket message"
	}
	slog.Warn(
		fmt.Sprintf("DCompose connection from %s to node %q failed while %s: %s", peer, a.config.Global.NodeID, operation, detail),
		"peer",
		peer,
		"node",
		a.config.Global.NodeID,
		"error",
		err,
	)
}

func (a *agent) channelCommands(
	ctx context.Context,
	peer string,
	commands <-chan protocol.ChannelMessage,
	outgoing chan<- protocol.ChannelMessage,
	cancel context.CancelFunc,
) {
	attached := make(map[string]context.CancelFunc)
	type relayDelivery struct {
		op   *operation
		done atomic.Bool
	}
	relays := make(map[string]*relayDelivery)
	var streams sync.WaitGroup
	defer func() {
		for _, detach := range attached {
			detach()
		}
		streams.Wait()
	}()
	send := func(message protocol.ChannelMessage) bool {
		select {
		case outgoing <- message:
			return true
		case <-ctx.Done():
			return false
		}
	}
	attach := func(key string, op *operation, message protocol.ChannelMessage) {
		if detach, ok := attached[key]; ok {
			detach()
		}
		streamCtx, detach := context.WithCancel(ctx)
		attached[key] = detach
		subscriber, initial := op.subscribe()
		var relay *relayDelivery
		if "stream.event" == message.Type {
			relay = &relayDelivery{op: op}
			relays[key] = relay
		}
		streams.Go(func() {
			defer op.unsubscribe(subscriber)
			send := func(e streamEvent) bool {
				terminal := "operation.done" == e.name
				if nil != relay {
					finished := terminal
					if "operation.snapshot" == e.name {
						var snapshot operationSnapshot
						finished = nil == json.Unmarshal(e.data, &snapshot) && "running" != snapshot.Status
					}
					if finished {
						relay.done.Store(true)
					}
				}
				if nil != relay {
					e.name = "relay." + e.name[len("operation."):]
				}
				message.Event, message.Payload = e.name, e.data
				select {
				case outgoing <- message:
					return !terminal
				case <-streamCtx.Done():
					return false
				}
			}
			for _, e := range initial {
				if !send(e) {
					return
				}
			}
			for {
				select {
				case <-streamCtx.Done():
					return
				case <-subscriber.done:
					slog.Warn(
						fmt.Sprintf(
							"Closing DCompose connection from %s because the %d-update buffer for %s operation %s on stack %q is full.",
							peer,
							cap(subscriber.events),
							op.Action,
							op.ID,
							op.Stack,
						),
						"peer",
						peer,
						"operation",
						op.ID,
						"stack",
						op.Stack,
						"action",
						op.Action,
						"capacity",
						cap(subscriber.events),
					)
					cancel()
					return
				case e := <-subscriber.events:
					if !send(e) {
						return
					}
				}
			}
		})
	}
	for {
		select {
		case <-ctx.Done():
			return
		case message := <-commands:
			if nil != ctx.Err() {
				return
			}
			if "registry.sync" == message.Type {
				var snapshot protocol.RegistrySync
				if err := json.Unmarshal(message.Payload, &snapshot); nil != err {
					send(protocol.ChannelMessage{Type: "registry.error", Status: 400, Error: "Invalid registry snapshot."})
					continue
				}
				if err := a.applyRegistries(snapshot); nil != err {
					send(protocol.ChannelMessage{Type: "registry.error", Status: 500, Error: "Could not apply registry credentials."})
					continue
				}
				payload, _ := json.Marshal(protocol.RegistryApplied{Revision: snapshot.Revision})
				send(protocol.ChannelMessage{Type: "registry.applied", Payload: payload})
				continue
			}
			if "shell.detach" == message.Type {
				if detach, ok := attached["shell:"+message.Shell]; ok {
					detach()
					delete(attached, "shell:"+message.Shell)
				}
				continue
			}
			if "stream.detach" == message.Type {
				if relay := relays["stream:"+message.Stream]; nil != relay {
					if relay.done.Load() {
						a.collectRelay(relay.op)
					}
					delete(relays, "stream:"+message.Stream)
				}
				if detach, ok := attached["stream:"+message.Stream]; ok {
					detach()
					delete(attached, "stream:"+message.Stream)
				}
				continue
			}
			if "operation.attach" == message.Type {
				send(
					protocol.ChannelMessage{
						Type:   "stream.error",
						Stream: message.Stream,
						Error:  "operation streams are not available",
						Status: 404,
					},
				)
				continue
			}
			if "relay.attach" == message.Type || "logs.attach" == message.Type || "stats.attach" == message.Type {
				if "" == message.Stream {
					send(protocol.ChannelMessage{Type: "stream.error", Error: "stream ID is required", Status: 400})
					continue
				}
				if "" == message.Resource {
					send(protocol.ChannelMessage{Type: "stream.error", Stream: message.Stream, Error: "resource is required", Status: 400})
					continue
				}
				key := "stream:" + message.Stream
				if "relay.attach" == message.Type {
					a.mu.Lock()
					op := a.ops[message.Resource]
					a.mu.Unlock()
					if nil == op {
						send(protocol.ChannelMessage{Type: "stream.error", Stream: message.Stream, Error: "relay not found", Status: 404})
						continue
					}
					attach(key, op, protocol.ChannelMessage{Type: "stream.event", Stream: message.Stream})
				} else {
					if detach, ok := attached[key]; ok {
						detach()
					}
					streamCtx, detach := context.WithCancel(ctx)
					attached[key] = detach
					streams.Go(func() {
						stream := a.streamLogs
						if "stats.attach" == message.Type {
							stream = a.streamStats
						}
						err := stream(streamCtx, message.Resource, func(e streamEvent) bool {
							select {
							case outgoing <- protocol.ChannelMessage{Type: "stream.event", Stream: message.Stream, Event: e.name, Payload: e.data}:
								return true
							case <-streamCtx.Done():
								return false
							}
						})
						if nil != err && nil == streamCtx.Err() {
							code := 502
							if apiErr, ok := errors.AsType[*apiError](err); ok {
								code = apiErr.code
							}
							send(protocol.ChannelMessage{Type: "stream.error", Stream: message.Stream, Error: err.Error(), Status: code})
						}
					})
				}
				continue
			}
			a.mu.Lock()
			s := a.shells[message.Shell]
			a.mu.Unlock()
			if nil == s {
				send(protocol.ChannelMessage{Type: "shell.error", Shell: message.Shell, Error: "shell not found"})
				continue
			}
			var err error
			switch message.Type {
			case "shell.attach":
				s.touch()
				attach("shell:"+message.Shell, s.op, protocol.ChannelMessage{Type: "shell.event", Shell: message.Shell})
			case "shell.input":
				err = s.writeInput(message.Data)
			case "shell.resize":
				err = s.resize(message.Cols, message.Rows)
			case "shell.close":
				completed := "running" != s.op.snapshot().Status
				if _, ok := attached["shell:"+message.Shell]; !ok && !completed {
					attach("shell:"+message.Shell, s.op, protocol.ChannelMessage{Type: "shell.event", Shell: message.Shell})
				}
				s.requestClose()
				streams.Go(func() {
					select {
					case <-s.done:
					case <-ctx.Done():
						return
					}
					s.mu.Lock()
					cleanupErr := s.cleanupErr
					s.mu.Unlock()
					if completed && nil != cleanupErr && nil == ctx.Err() {
						cleanupErr = a.retryShellCleanup(s)
						if nil == cleanupErr {
							s.op.mu.Lock()
							code := 0
							// A successful explicit retry completes the requested close.
							s.op.Status, s.op.ExitCode = "succeeded", &code
							s.op.mu.Unlock()
						}
					}
					if nil != cleanupErr {
						slog.Error(
							fmt.Sprintf(
								"Could not close shell %s in container %q of stack %q for DCompose peer %s: %v",
								s.op.ID,
								s.container,
								s.op.Stack,
								peer,
								cleanupErr,
							),
							"shell",
							s.op.ID,
							"stack",
							s.op.Stack,
							"container",
							s.container,
							"peer",
							peer,
							"error",
							cleanupErr,
						)
						send(
							protocol.ChannelMessage{
								Type:  "shell.error",
								Shell: message.Shell,
								Error: "shell cleanup failed. Retry closing the shell when the container engine is available",
							},
						)
					}
					if completed {
						e := event("operation.done", s.op.snapshot())
						send(protocol.ChannelMessage{Type: "shell.event", Shell: message.Shell, Event: e.name, Payload: e.data})
					}
				})
			default:
				err = fault(400, "unsupported channel message")
			}
			if nil != err {
				// Terminal helpers return fixed messages, never runtime stderr.
				send(protocol.ChannelMessage{Type: "shell.error", Shell: message.Shell, Error: err.Error()})
			}
		}
	}
}
