package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/arabcoders/dcompose/internal/protocol"
)

type actionResponse struct {
	Status string `json:"status"`
	Relay  string `json:"relay,omitempty"`
}

type relayKey struct{ node, id string }

type relaySnapshot struct {
	ID       string `json:"id"`
	Stack    string `json:"stack"`
	Action   string `json:"action"`
	Status   string `json:"status"`
	Output   string `json:"output"`
	ExitCode *int   `json:"exit_code"`
}

// Only the node's replacement sidecar needs replay across a disconnected node.
// Normal command output never passes through this path.
func (s *Server) discoverRelays(ctx context.Context, node string) {
	var response struct {
		Relays []struct {
			ID string `json:"id"`
		} `json:"relays"`
	}
	if err := s.imageRequest(ctx, node, "GET", "/relays", nil, &response); nil != err {
		if nil == ctx.Err() && !errors.Is(err, errNodePaused) {
			slog.Warn(fmt.Sprintf("Could not discover self-update relays on node %q: %s", node, safeTransportError(err)), "node", node, "error", safeTransportError(err))
		}
		return
	}
	for _, relay := range response.Relays {
		s.startRelay(node, relay.ID)
	}
}

func (s *Server) startRelay(node, id string) {
	if !identity.MatchString(id) {
		return
	}
	key := relayKey{node, id}
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if nil == s.hubContext || s.hubClosed || nil != s.hubContext.Err() || s.relays[key] {
		return
	}
	if runtime := s.runtime[node]; nil != runtime && `"paused"` == string(runtime.state["state"]) {
		return
	}
	s.relays[key] = true
	s.hubWorkers.Go(func() {
		defer func() {
			s.hubMu.Lock()
			delete(s.relays, key)
			s.hubMu.Unlock()
		}()
		s.relaySelfUpdate(s.hubContext, key)
	})
}

func (s *Server) relaySelfUpdate(ctx context.Context, key relayKey) {
	offset := 0
	started := false
	stack, action := "", ""
	for nil == ctx.Err() {
		config, _ := s.snapshot()
		if node, ok := config.Nodes[key.node]; !ok || node.Paused {
			return
		}
		subscription, failure := s.attachNode(ctx, protocol.ChannelMessage{Type: "relay.attach", Node: key.node, Resource: key.id})
		if nil == failure {
			complete := func() bool {
				for {
					select {
					case <-ctx.Done():
						return false
					case message, open := <-subscription.subscriber:
						if !open {
							return false
						}
						if "error" == message.Type {
							return 404 == message.Status
						}
						switch message.Event {
						case "relay.snapshot", "relay.done":
							var snapshot relaySnapshot
							if nil != json.Unmarshal(message.Payload, &snapshot) || key.id != snapshot.ID || !stackName.MatchString(snapshot.Stack) || "" == snapshot.Action {
								return false
							}
							stack, action = snapshot.Stack, snapshot.Action
							if !started {
								s.emitRelayAction(key.node, stack, action, "start", nil, "")
								started = true
							}
							if offset < len(snapshot.Output) {
								s.emitRelayOutput(key.node, stack, snapshot.Output[offset:])
								offset = len(snapshot.Output)
							}
							if "running" != snapshot.Status && nil != snapshot.ExitCode {
								s.emitRelayAction(key.node, stack, action, "exit", snapshot.ExitCode, snapshot.Output)
								return true
							}
						case "relay.output":
							var chunk struct {
								ID     string `json:"id"`
								Output string `json:"output"`
							}
							if !started || nil != json.Unmarshal(message.Payload, &chunk) || key.id != chunk.ID {
								return false
							}
							s.emitRelayOutput(key.node, stack, chunk.Output)
							offset += len(chunk.Output)
						}
					}
				}
			}()
			s.detachBrowser(subscription)
			if complete {
				return
			}
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Server) emitRelayOutput(node, stack, output string) {
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if runtime := s.runtime[node]; nil != runtime && `"paused"` == string(runtime.state["state"]) {
		return
	}
	s.broadcastLocked(runtimeFrame("stack.output", map[string]string{"node": node, "name": stack, "output": output}))
}

var relayANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func (s *Server) emitRelayAction(node, stack, action, phase string, code *int, output string) {
	payload := map[string]json.RawMessage{}
	payload["node"], _ = json.Marshal(node)
	payload["name"], _ = json.Marshal(stack)
	payload["action"], _ = json.Marshal(action)
	payload["phase"], _ = json.Marshal(phase)
	message := fmt.Sprintf("Started %s for stack %q on node %q.", action, stack, node)
	if nil != code {
		payload["exit_code"], _ = json.Marshal(*code)
		message = fmt.Sprintf("Finished %s for stack %q on node %q (exit code %d).", action, stack, node, *code)
		if 0 != *code {
			message = fmt.Sprintf("%s failed for stack %q on node %q (exit code %d)", action, stack, node, *code)
			if detail := strings.Join(strings.Fields(relayANSI.ReplaceAllString(output, "")), " "); "" != detail {
				if 4096 < len(detail) {
					start := len(detail) - 4096
					for !utf8.RuneStart(detail[start]) {
						start++
					}
					detail = detail[start:]
				}
				message += ": " + detail
			}
		}
	}
	payload["message"], _ = json.Marshal(message)
	s.hubMu.Lock()
	defer s.hubMu.Unlock()
	if runtime := s.runtime[node]; nil != runtime && `"paused"` == string(runtime.state["state"]) {
		return
	}
	s.stackActionLocked(node, payload)
	s.broadcastLocked(runtimeFrame("stack.action", payload))
}
