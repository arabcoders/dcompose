package server

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
)

var errRegistrySync = errors.New("registry credentials have not been applied by the node")

// Caller holds hubMu. Wake existing waiters before replacing their notification.
func wakeRegistryLocked(channel *nodeChannel) {
	if nil != channel.registryNotify {
		close(channel.registryNotify)
	}
	channel.registryNotify = make(chan struct{})
}

// Caller holds hubMu. The reader remains free to receive the acknowledgement.
func (s *Server) syncRegistriesLocked(channel *nodeChannel) error {
	channel.registryRevision = s.registrySync.Revision
	channel.registryApplied = ""
	channel.registryFailed = false
	wakeRegistryLocked(channel)
	s.queueNodeImagesLocked(channel.id)
	s.stateLocked(channel.id, "unavailable", "Applying registry credentials.")
	payload, _ := json.Marshal(s.registrySync)
	err := writeChannel(channel.ctx, channel.conn, protocol.ChannelMessage{Type: "registry.sync", Payload: payload})
	if nil != err {
		channel.registryFailed = true
		wakeRegistryLocked(channel)
	}
	return err
}

func (s *Server) registryReply(channel *nodeChannel, message protocol.ChannelMessage) error {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if s.channels[channel.id] != channel {
		return nil
	}
	if "registry.error" == message.Type {
		// Never log or forward a peer's error text, which might contain credentials.
		channel.registryFailed = true
		channel.registryApplied = ""
		wakeRegistryLocked(channel)
		s.stateLocked(channel.id, "unavailable", "Could not apply registry credentials.")
		return nil
	}
	var applied protocol.RegistryApplied
	if nil != json.Unmarshal(message.Payload, &applied) || "" == applied.Revision {
		return errChannelMessage
	}
	if channel.registryRevision != applied.Revision {
		return nil
	}
	channel.registryApplied = applied.Revision
	channel.registryFailed = false
	wakeRegistryLocked(channel)
	s.queueNodeImagesLocked(channel.id)
	if runtime := s.runtime[channel.id]; nil != runtime && nil != runtime.snapshot {
		s.broadcastLocked(runtimeFrame("node.snapshot", runtime.snapshot))
		s.stateLocked(channel.id, "connected", "")
	}
	return nil
}

func (s *Server) awaitRegistries(ctx context.Context, nodeID string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		s.hubMu.Lock()
		if runtime := s.runtime[nodeID]; nil != runtime && `"paused"` == string(runtime.state["state"]) {
			s.hubMu.Unlock()
			return errNodePaused
		}
		channel := s.channels[nodeID]
		if nil == channel || nil == channel.conn || channel.registryFailed {
			s.hubMu.Unlock()
			return errRegistrySync
		}
		if s.registrySync.Revision == channel.registryApplied {
			s.hubMu.Unlock()
			return nil
		}
		notify := channel.registryNotify
		s.hubMu.Unlock()
		select {
		case <-notify:
		case <-ctx.Done():
			return errRegistrySync
		}
	}
}
