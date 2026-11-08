package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
)

type browserSubscription struct {
	id         string
	stream     string
	shell      string
	kind       string
	resource   string
	channel    *nodeChannel
	conn       *websocket.Conn
	subscriber chan protocol.ChannelMessage
	ctx        context.Context
	cancel     context.CancelFunc
}

type browserDelivery struct {
	subscription *browserSubscription
	message      protocol.ChannelMessage
	done         bool
}

func (s *Server) attachBrowser(ctx context.Context, message protocol.ChannelMessage) (*browserSubscription, *protocol.ChannelMessage) {
	if "" == message.Stream {
		return nil, browserError("", 400, "Could not open the requested view. Reload the page and try again.")
	}
	valid := identity.MatchString(message.Node)
	switch message.Type {
	case "logs.attach":
		valid = valid && stackName.MatchString(message.Resource)
	case "shell.attach":
		valid = valid && identity.MatchString(message.Shell)
	default:
		valid = false
	}
	if !valid {
		return nil, browserError(message.Stream, 400, "Could not open the requested view. Reload the page and try again.")
	}
	config, _ := s.snapshot()
	if node, ok := config.Nodes[message.Node]; !ok {
		return nil, browserError(message.Stream, 404, "Node not found. Choose another node.")
	} else if node.Paused {
		return nil, browserError(message.Stream, 409, errNodePaused.Error())
	}
	return s.attachNode(ctx, message)
}

func (s *Server) attachNode(ctx context.Context, message protocol.ChannelMessage) (*browserSubscription, *protocol.ChannelMessage) {
	s.hubMu.Lock()
	if runtime := s.runtime[message.Node]; nil != runtime && `"paused"` == string(runtime.state["state"]) {
		s.hubMu.Unlock()
		return nil, browserError(message.Stream, 409, errNodePaused.Error())
	}
	channel := s.channels[message.Node]
	if nil == channel || nil == channel.conn {
		s.hubMu.Unlock()
		return nil, browserError(message.Stream, 502, "Node is unavailable. Try again when it reconnects.")
	}
	ctx, cancel := context.WithCancel(ctx)
	subscription := &browserSubscription{id: message.Stream, kind: message.Type, resource: message.Resource, channel: channel, conn: channel.conn, subscriber: make(chan protocol.ChannelMessage, 64), ctx: ctx, cancel: cancel}
	command := protocol.ChannelMessage{Type: message.Type, Resource: message.Resource}
	if "shell.attach" == message.Type {
		subscription.shell = message.Shell
		command.Shell = message.Shell
		if nil == channel.shells[message.Shell] {
			channel.shells[message.Shell] = make(map[chan protocol.ChannelMessage]struct{})
		}
		channel.shells[message.Shell][subscription.subscriber] = struct{}{}
	} else {
		subscription.stream = strconv.FormatUint(s.nextStream.Add(1), 16)
		command.Stream = subscription.stream
		channel.streams[subscription.stream] = subscription
	}
	s.hubMu.Unlock()
	if err := writeChannel(ctx, subscription.conn, command); nil != err {
		view := fmt.Sprintf("logs for stack %q", subscription.resource)
		switch subscription.kind {
		case "relay.attach":
			view = fmt.Sprintf("self-update relay %q", subscription.resource)
		case "shell.attach":
			view = fmt.Sprintf("shell %q", subscription.shell)
		}
		detail := safeTransportError(err)
		slog.Error(fmt.Sprintf("Could not open %s on %s: %s", view, nodeLogLabel(channel.node), detail), "node", nodeLogLabel(channel.node), "operation", subscription.kind, "resource", subscription.resource, "stream", subscription.stream, "shell", subscription.shell, "error", detail)
		s.detachBrowser(subscription)
		return nil, browserError(message.Stream, 502, "Could not open the requested view. Try again.")
	}
	return subscription, nil
}

func (s *Server) detachBrowser(subscription *browserSubscription) {
	subscription.cancel()
	channel := subscription.channel
	s.hubMu.Lock()
	connected := channel.conn == subscription.conn
	last := false
	if "" != subscription.shell {
		delete(channel.shells[subscription.shell], subscription.subscriber)
		last = 0 == len(channel.shells[subscription.shell])
		if last {
			delete(channel.shells, subscription.shell)
		}
	} else {
		delete(channel.streams, subscription.stream)
	}
	s.hubMu.Unlock()
	if !connected {
		return
	}
	if "" == subscription.shell {
		_ = writeChannel(channel.ctx, subscription.conn, protocol.ChannelMessage{Type: "stream.detach", Stream: subscription.stream})
	} else if last {
		_ = writeChannel(channel.ctx, subscription.conn, protocol.ChannelMessage{Type: "shell.close", Shell: subscription.shell})
		_ = writeChannel(channel.ctx, subscription.conn, protocol.ChannelMessage{Type: "shell.detach", Shell: subscription.shell})
	}
}

func (subscription *browserSubscription) forward(outgoing chan<- browserDelivery) {
	send := func(message protocol.ChannelMessage, done bool) bool {
		select {
		case outgoing <- browserDelivery{subscription: subscription, message: message, done: done}:
			return true
		case <-subscription.ctx.Done():
			return false
		}
	}
	snapshot := false
	for {
		select {
		case <-subscription.ctx.Done():
			return
		case message, ok := <-subscription.subscriber:
			if !ok {
				send(*browserError(subscription.id, 502, "Connection lost. Try again."), false)
				send(protocol.ChannelMessage{}, true)
				return
			}
			if "" != subscription.shell && "shell.error" != message.Type {
				if "operation.snapshot" == message.Event {
					snapshot = true
				} else if !snapshot {
					continue
				}
			}
			message.Stream = subscription.id
			if "error" == message.Type && 409 != message.Status && "Connection lost. Try again." != message.Error {
				message.Error = "Could not load logs. Try again."
			}
			if !send(message, false) {
				return
			}
			if "error" == message.Type || "shell.error" == message.Type || "operation.done" == message.Event || "done" == message.Event || "error" == message.Event {
				send(protocol.ChannelMessage{}, true)
				return
			}
		}
	}
}

func (s *Server) streamEvent(channel *nodeChannel, message protocol.ChannelMessage) {
	id := message.Stream
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	subscription := channel.streams[id]
	if nil == subscription {
		return
	}
	if "stream.error" == message.Type {
		status := message.Status
		if 400 > status || 599 < status {
			status = 502
		}
		view := fmt.Sprintf("logs for stack %q", subscription.resource)
		if "relay.attach" == subscription.kind {
			view = fmt.Sprintf("self-update relay %q", subscription.resource)
		}
		detail := fmt.Sprintf("Could not load %s on %s", view, nodeLogLabel(channel.node))
		if 400 <= message.Status && 599 >= message.Status {
			detail += fmt.Sprintf(" (HTTP %d)", message.Status)
		}
		if "" != message.Error {
			detail += ": " + message.Error
		} else {
			detail += "."
		}
		slog.Error(detail, "node", nodeLogLabel(channel.node), "operation", subscription.kind, "resource", subscription.resource, "stream", subscription.stream, "status", message.Status, "error", message.Error)
		message = protocol.ChannelMessage{Type: "error", Status: status, Error: "Could not load the requested view. Try again."}
	} else {
		switch message.Event {
		case "relay.snapshot", "relay.output", "relay.done", "message", "done", "error":
		default:
			return
		}
		if !json.Valid(message.Payload) {
			return
		}
		message = protocol.ChannelMessage{Type: "event", Event: message.Event, Payload: message.Payload}
	}
	select {
	case subscription.subscriber <- message:
	default:
		close(subscription.subscriber)
		delete(channel.streams, id)
	}
}
