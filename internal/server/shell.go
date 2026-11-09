package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func (s *Server) browserShellCommand(
	ctx context.Context,
	subscription *browserSubscription,
	message protocol.ChannelMessage,
) *protocol.ChannelMessage {
	if nil == subscription || "" == subscription.shell {
		return browserError(message.Stream, 404, "Shell is not open. Reopen the shell and try again.")
	}
	command := protocol.ChannelMessage{Type: message.Type, Shell: subscription.shell}
	switch message.Type {
	case "shell.input":
		if 65536 < len(message.Data) {
			return browserError(message.Stream, 400, "Shell input is too large. Send at most 64 KiB at a time.")
		}
		command.Data = message.Data
	case "shell.resize":
		if !validSize(message.Cols, message.Rows) {
			return browserError(message.Stream, 400, "Set the terminal size to 1-500 columns and rows.")
		}
		command.Cols, command.Rows = message.Cols, message.Rows
	}
	s.hubMu.Lock()
	if runtime := s.runtime[subscription.channel.id]; nil != runtime && `"paused"` == string(runtime.state["state"]) {
		s.hubMu.Unlock()
		return browserError(message.Stream, 409, errNodePaused.Error())
	}
	connected := subscription.channel.conn == subscription.conn
	s.hubMu.Unlock()
	if !connected {
		return browserError(message.Stream, 502, "Connection lost. Reopen the shell.")
	}
	if err := writeChannel(ctx, subscription.conn, command); nil != err {
		action := "send input to"
		if "shell.resize" == message.Type {
			action = "resize"
		}
		detail := safeTransportError(err)
		slog.Error(
			fmt.Sprintf("Could not %s shell %q on %s: %s", action, subscription.shell, nodeLogLabel(subscription.channel.node), detail),
			"node",
			nodeLogLabel(subscription.channel.node),
			"operation",
			action,
			"shell",
			subscription.shell,
			"error",
			detail,
		)
		return browserError(message.Stream, 502, "Connection lost. Reopen the shell.")
	}
	return nil
}

func websocketOrigin(r *http.Request, configured string) bool {
	origin := r.Header.Get("Origin")
	if "" == origin {
		return true
	}
	parsed, err := url.Parse(origin)
	if nil != err {
		return false
	}
	scheme := "http"
	if nil != r.TLS {
		scheme = "https"
	}
	if "" != configured {
		base, err := url.Parse(configured)
		if nil == err {
			scheme = base.Scheme
		}
	}
	return scheme == parsed.Scheme && strings.EqualFold(parsed.Host, r.Host)
}
