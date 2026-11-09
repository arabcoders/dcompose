package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type nodeChannel struct {
	id               string
	node             NodeConfig
	client           *http.Client
	ctx              context.Context
	cancel           context.CancelFunc
	conn             *websocket.Conn // guarded by hubMu, as are shells
	shells           map[string]map[chan protocol.ChannelMessage]struct{}
	streams          map[string]*browserSubscription
	registryRevision string
	registryApplied  string
	registryFailed   bool
	registryNotify   chan struct{}
	imageContext     context.Context
	imageCancel      context.CancelFunc
}

// Start connects configured agents independently of browser requests. New is inert.
func (s *Server) Start(ctx context.Context) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if nil != s.hubContext || s.hubClosed {
		return
	}
	s.hubContext, s.hubCancel = context.WithCancel(ctx)
	s.loadImageCooldowns()
	s.reconcileChannelsLocked(s.config, s.nodes)
	s.hubWorkers.Go(s.watchImages)
}

// Close cancels and joins the server-owned channels.
func (s *Server) Close() {
	s.hubMu.Lock()
	s.hubClosed = true
	if nil != s.hubCancel {
		s.hubCancel()
	}
	for _, channel := range s.channels {
		s.stopChannelLocked(channel, false)
	}
	for subscriber := range s.watchers {
		close(subscriber)
		delete(s.watchers, subscriber)
	}
	s.hubMu.Unlock()
	s.hubWorkers.Wait()
	s.closeLogHistory()
}

// Caller holds hubMu and, when configuration is being changed, configMu.
func (s *Server) reconcileChannelsLocked(config Config, clients map[string]*http.Client) {
	for id, channel := range s.channels {
		if node, ok := config.Nodes[id]; !ok || node.Paused || channel.node != node {
			s.stopChannelLocked(channel, node.Paused)
			delete(s.channels, id)
			delete(s.runtime, id)
			s.removeNodeImagesLocked(id)
		}
	}
	for id := range s.runtime {
		if node, ok := config.Nodes[id]; !ok || (!node.Paused && `"paused"` == string(s.runtime[id].state["state"])) {
			delete(s.runtime, id)
		}
	}
	for id, node := range config.Nodes {
		if node.Paused {
			if runtime := s.runtime[id]; nil == runtime || `"paused"` != string(runtime.state["state"]) {
				s.runtime[id] = &nodeRuntime{}
				s.stateLocked(id, "paused", errNodePaused.Error())
			}
		}
	}
	if nil == s.hubContext || s.hubClosed || nil != s.hubContext.Err() {
		return
	}
	for id, node := range config.Nodes {
		if node.Paused {
			continue
		}
		if _, ok := s.channels[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(s.hubContext)
		channel := &nodeChannel{
			id:      id,
			node:    node,
			client:  clients[id],
			ctx:     ctx,
			cancel:  cancel,
			shells:  make(map[string]map[chan protocol.ChannelMessage]struct{}),
			streams: make(map[string]*browserSubscription),
		}
		s.channels[id] = channel
		s.runtime[id] = &nodeRuntime{}
		s.stateLocked(id, "unavailable", "Connecting to node.")
		s.hubWorkers.Go(func() { s.runChannel(channel) })
	}
}

func (s *Server) stopChannelLocked(channel *nodeChannel, paused bool) {
	channel.cancel()
	if nil != channel.imageCancel {
		channel.imageCancel()
	}
	s.pauseNodeImagesLocked(channel.id)
	if nil != channel.conn {
		_ = channel.conn.CloseNow()
		channel.conn = nil
	}
	s.failShellsLocked(channel, paused)
	wakeRegistryLocked(channel)
}

func (s *Server) failShellsLocked(channel *nodeChannel, paused bool) {
	status, streamError, shellError := 502, "Connection lost. Try again.", "Node disconnected. Reopen the shell."
	if paused {
		status, streamError, shellError = 409, errNodePaused.Error(), errNodePaused.Error()
	}
	for id, subscription := range channel.streams {
		select {
		case subscription.subscriber <- protocol.ChannelMessage{Type: "error", Status: status, Error: streamError}:
		default:
		}
		close(subscription.subscriber)
		delete(channel.streams, id)
	}
	for shell, subscribers := range channel.shells {
		for subscriber := range subscribers {
			select {
			case subscriber <- protocol.ChannelMessage{Type: "shell.error", Shell: shell, Status: status, Error: shellError}:
			default:
			}
			close(subscriber)
		}
		delete(channel.shells, shell)
	}
}

func dialChannel(channel *nodeChannel) (*websocket.Conn, error) {
	if channel.node.Paused {
		return nil, errNodePaused
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+channel.node.token)
	headers.Set(protocol.Header, strconv.Itoa(protocol.AgentAPI))
	endpoint := "wss" + strings.TrimPrefix(strings.TrimRight(channel.node.URL, "/"), "https") + "/channel"
	ctx, cancel := context.WithTimeout(channel.ctx, 20*time.Second)
	defer cancel()
	conn, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: channel.client, HTTPHeader: headers})
	if nil != response && http.StatusUpgradeRequired == response.StatusCode {
		return nil, incompatibleAgent{}
	}
	if nil != err {
		if nil != response {
			return nil, readNodeHTTPError(response, "WebSocket handshake")
		}
		return nil, err
	}
	conn.SetReadLimit(8 << 20)
	return conn, nil
}

func (s *Server) runChannel(channel *nodeChannel) {
	delay := time.Second
	var warnings channelWarnings
	for nil == channel.ctx.Err() {
		conn, err := dialChannel(channel)
		if nil == err {
			s.hubMu.Lock()
			if nil != channel.ctx.Err() {
				s.hubMu.Unlock()
				_ = conn.CloseNow()
				return
			}
			channel.conn = conn
			channel.imageContext, channel.imageCancel = context.WithCancel(channel.ctx)
			s.hubWorkers.Go(func() {
				s.discoverRelays(channel.ctx, channel.id)
			})
			err = s.syncRegistriesLocked(channel)
			s.hubMu.Unlock()
			slog.Debug(
				fmt.Sprintf("Connected to %s. Authentication succeeded.", nodeLogLabel(channel.node)),
				"node",
				nodeLogLabel(channel.node),
			)
			stopPing := socketHeartbeat(channel.ctx, conn, nodeLogLabel(channel.node))
			for nil == err {
				var message protocol.ChannelMessage
				if err = wsjson.Read(channel.ctx, conn, &message); nil != err {
					break
				}
				switch message.Type {
				case "node.logs":
					err = s.nodeLogs(channel, message.Payload)
				case "registry.applied", "registry.error":
					err = s.registryReply(channel, message)
				case "event":
					err = s.runtimeEvent(channel, message.Event, message.Payload)
					if nil != err {
						err = fmt.Errorf("%w %q: %w", errRuntimeEvent, message.Event, err)
					}
					if nil == err && "node.snapshot" == message.Event {
						delay = time.Second
						warnings = channelWarnings{}
					}
				case "shell.event", "shell.error":
					s.shellEvent(channel, message)
				case "stream.event", "stream.error":
					s.streamEvent(channel, message)
				default:
					err = errChannelMessage
				}
				if nil != err {
					break
				}
			}
			stopPing()
			_ = conn.CloseNow()
		}
		s.hubMu.Lock()
		channel.conn = nil
		if nil != channel.imageCancel {
			channel.imageCancel()
		}
		channel.registryApplied = ""
		wakeRegistryLocked(channel)
		s.failShellsLocked(channel, false)
		if s.channels[channel.id] == channel && nil == channel.ctx.Err() {
			s.pauseNodeImagesLocked(channel.id)
			message := "Node is unavailable. Retrying."
			if incompatible, ok := errors.AsType[incompatibleAgent](err); ok {
				message = incompatible.Error()
			}
			s.stateLocked(channel.id, "unavailable", message)
		}
		s.hubMu.Unlock()
		if nil != channel.ctx.Err() {
			return
		}
		if warnings.allow(time.Now()) {
			node, detail := nodeLogLabel(channel.node), safeTransportError(err)
			slog.Warn(
				fmt.Sprintf(
					"Connection to %s failed: %s. Retrying in %s.",
					node,
					strings.TrimSuffix(strings.TrimSpace(detail), "."),
					delay.String(),
				),
				"node",
				node,
				"error",
				detail,
				"retry_in",
				delay,
			)
		}
		timer := time.NewTimer(delay)
		select {
		case <-channel.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}

type channelWarnings struct {
	next     time.Time
	interval time.Duration
}

func (w *channelWarnings) allow(now time.Time) bool {
	if now.Before(w.next) {
		return false
	}
	if 0 == w.interval {
		w.interval = time.Minute
	} else {
		w.interval = min(w.interval*2, time.Hour)
	}
	w.next = now.Add(w.interval)
	return true
}

var errChannelMessage = errors.New("node sent an unsupported WebSocket message type")
var errRuntimeEvent = errors.New("node sent an invalid or incomplete runtime event")

func nodeLogLabel(node NodeConfig) string {
	address, err := url.Parse(node.URL)
	if nil != err {
		return fmt.Sprintf("node %q at an invalid address", node.Name)
	}
	return fmt.Sprintf("node %q at %s://%s", node.Name, address.Scheme, address.Host)
}

// Transport errors may contain a URL including credentials. Never log that URL.
func safeTransportError(err error) string {
	if nil == err {
		return "connection closed"
	}
	if _, ok := errors.AsType[incompatibleAgent](err); ok {
		return fmt.Sprintf("node API version mismatch: server requires version %d", protocol.AgentAPI)
	}
	if address, ok := errors.AsType[*url.Error](err); ok {
		return safeTransportError(address.Err)
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "timeout"
	}
	if errors.Is(err, errFingerprint) {
		return errFingerprint.Error()
	}
	if errors.Is(err, errNodeHealth) {
		return errNodeHealth.Error()
	}
	if errors.Is(err, errChannelMessage) {
		return errChannelMessage.Error()
	}
	if errors.Is(err, errRuntimeEvent) {
		return err.Error()
	}
	if upstream, ok := errors.AsType[nodeHTTPError](err); ok {
		return upstream.Error()
	}
	if dns, ok := errors.AsType[*net.DNSError](err); ok {
		return fmt.Sprintf("DNS lookup for %q failed: %s", dns.Name, dns.Err)
	}
	if socket, ok := errors.AsType[*net.OpError](err); ok {
		return fmt.Sprintf("%s %s failed: %s", socket.Op, socket.Net, safeTransportError(socket.Err))
	}
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		return errno.Error()
	}
	if record, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return "TLS handshake failed: " + record.Msg
	}
	if alert, ok := errors.AsType[tls.AlertError](err); ok {
		return alert.Error()
	}
	if syntax, ok := errors.AsType[*json.SyntaxError](err); ok {
		return fmt.Sprintf("node sent invalid JSON at byte %d: %s", syntax.Offset, syntax.Error())
	}
	if field, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return fmt.Sprintf("node sent %s for JSON field %q, expected %s", field.Value, field.Field, field.Type)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "connection ended before a complete message was received"
	}
	// WebSocket errors may include untrusted peer close reasons or request URLs.
	if status := websocket.CloseStatus(err); -1 != status {
		return fmt.Sprintf("WebSocket closed with code %d (%s)", status, status.String())
	}
	return err.Error()
}

func writeChannel(ctx context.Context, conn *websocket.Conn, message protocol.ChannelMessage) error {
	data, err := json.Marshal(message)
	if nil != err {
		return err
	}
	return writeSocket(ctx, conn, data)
}

func writeSocket(ctx context.Context, conn *websocket.Conn, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, data)
}

func socketHeartbeat(ctx context.Context, conn *websocket.Conn, label string) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingContext, stop := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Ping(pingContext)
				stop()
				if nil != err {
					if nil == ctx.Err() {
						detail := safeTransportError(err)
						slog.Warn(fmt.Sprintf("Connection check for %s failed: %s", label, detail), "peer", label, "error", detail)
					}
					_ = conn.CloseNow()
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (s *Server) shellEvent(channel *nodeChannel, message protocol.ChannelMessage) {
	if "shell.error" == message.Type {
		slog.Error(
			fmt.Sprintf("Could not use shell %q on %s: %s", message.Shell, nodeLogLabel(channel.node), message.Error),
			"node",
			nodeLogLabel(channel.node),
			"shell",
			message.Shell,
			"error",
			message.Error,
		)
		message = protocol.ChannelMessage{
			Type:  "shell.error",
			Shell: message.Shell,
			Error: "Could not use the shell. Reopen it and try again.",
		}
	} else {
		switch message.Event {
		case "operation.snapshot", "operation.output", "operation.done":
		default:
			return
		}
		if !json.Valid(message.Payload) {
			return
		}
		message = protocol.ChannelMessage{Type: "shell.event", Shell: message.Shell, Event: message.Event, Payload: message.Payload}
	}
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	for subscriber := range channel.shells[message.Shell] {
		select {
		case subscriber <- message:
		default:
			close(subscriber)
			delete(channel.shells[message.Shell], subscriber)
		}
	}
}
