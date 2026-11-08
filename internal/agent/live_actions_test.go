package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func TestRuntimeBusy(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "slow", "compose.yaml")
	write(t, filepath.Join(fake, "block-slow"), "")
	states := make(chan stackRuntime, 1)
	go func() {
		state, _ := a.runtimeStatus(context.Background(), "slow")
		states <- state
	}()
	eventually(t, func() bool { _, err := os.Stat(filepath.Join(fake, "ps-slow")); return nil == err })
	a.setStackBusy("slow", true)
	if err := os.Remove(filepath.Join(fake, "block-slow")); nil != err {
		t.Fatal(err)
	}
	if state := <-states; !state.Busy {
		t.Fatal("a status query begun before the command erased its busy state")
	}
}

func TestActionOutputUTF8(t *testing.T) {
	a, _ := fixture(t)
	first, second := newSubscriber(), newSubscriber()
	a.events.subscribers[first], a.events.subscribers[second] = struct{}{}, struct{}{}
	output := &actionOutput{agent: a, name: "demo"}
	runeBytes := []byte("日")
	_, _ = output.Write(runeBytes[:1])
	if 0 != len(first.events) || 0 != len(second.events) {
		t.Fatal("partial rune was published")
	}
	_, _ = output.Write(runeBytes[1:])
	large := strings.Repeat("x", 2<<20)
	_, _ = output.Write([]byte(large))
	_, _ = output.Write([]byte{0xe2})
	output.flush()
	for _, subscriber := range []*eventSubscriber{first, second} {
		for _, expected := range []string{"日", large, "\uFFFD"} {
			e := <-subscriber.events
			var delta protocol.StackOutput
			if "stack.output" != e.name || nil != json.Unmarshal(e.data, &delta) || "demo" != delta.Name || expected != delta.Output || !utf8.ValidString(delta.Output) {
				t.Fatalf("invalid output chunk: %s (%d bytes)", e.name, len(e.data))
			}
		}
	}
	if 0 != len(a.ops) || 0 != len(a.order) || 4096 < len(output.diagnostic) {
		t.Fatal("live output allocated history or unbounded diagnostic state")
	}
}

func TestChannelRelay(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "self", "compose.yaml")
	a.operationsDir = filepath.Join(t.TempDir(), "dcompose", "operations")
	op := detachedOp(t, a, "0123456789abcdef")
	a.setStackBusy("self", true)
	server := channelServer(t, a)
	conn := openChannel(t, server)
	channelSend(t, conn, protocol.ChannelMessage{Type: "relay.attach", Stream: "relay", Resource: op.ID})
	snapshot := channelUntil(t, conn, "stream.event", "relay.snapshot")
	var initial operationSnapshot
	if nil != json.Unmarshal(snapshot.Payload, &initial) || "running" != initial.Status || op.ID != initial.ID {
		t.Fatalf("relay snapshot: %s", snapshot.Payload)
	}
	channelSend(t, conn, protocol.ChannelMessage{Type: "stream.detach", Stream: "relay"})
	eventually(t, func() bool {
		op.mu.Lock()
		defer op.mu.Unlock()
		return 0 == len(op.subscribers)
	})
	marker := filepath.Join(a.operationsDir, op.ID, "collected")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("running relay detach marked it consumed")
	}
	channelSend(t, conn, protocol.ChannelMessage{Type: "relay.attach", Stream: "relay", Resource: op.ID})
	channelUntil(t, conn, "stream.event", "relay.snapshot")
	_, _ = op.Write([]byte("helper output\n"))
	message := channelUntil(t, conn, "stream.event", "relay.output")
	if !strings.Contains(string(message.Payload), "helper output") {
		t.Fatalf("relay output: %s", message.Payload)
	}
	if err := writeDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op, 0); nil != err {
		t.Fatal(err)
	}
	a.finishOperation(op, 0)
	channelUntil(t, conn, "stream.event", "relay.done")
	_ = conn.CloseNow()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("disconnect consumed an unacknowledged relay")
	}
	var listed struct {
		Relays []struct {
			ID string `json:"id"`
		} `json:"relays"`
	}
	w := request(a, "GET", "/relays", "")
	if 200 != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &listed) || 1 != len(listed.Relays) || op.ID != listed.Relays[0].ID || strings.Contains(w.Body.String(), "output") {
		t.Fatalf("recoverable relays: %s", w.Body)
	}
	reconnected := openChannel(t, server)
	channelSend(t, reconnected, protocol.ChannelMessage{Type: "relay.attach", Stream: "recovered", Resource: op.ID})
	snapshot = channelUntil(t, reconnected, "stream.event", "relay.snapshot")
	if nil != json.Unmarshal(snapshot.Payload, &initial) || "succeeded" != initial.Status || "helper output\n" != initial.Output {
		t.Fatalf("terminal recovery snapshot: %s", snapshot.Payload)
	}
	// A terminal snapshot is itself the final result; no separate done frame is
	// required before the server acknowledges its consumption.
	channelSend(t, reconnected, protocol.ChannelMessage{Type: "stream.detach", Stream: "recovered"})
	eventually(t, func() bool { _, err := os.Stat(marker); return nil == err })
	eventually(t, func() bool {
		w := request(a, "GET", "/relays", "")
		return nil == json.Unmarshal(w.Body.Bytes(), &listed) && 0 == len(listed.Relays)
	})
	channelSend(t, reconnected, protocol.ChannelMessage{Type: "operation.attach", Stream: "obsolete", Resource: op.ID})
	if rejected := channelUntil(t, reconnected, "stream.error", ""); 404 != rejected.Status {
		t.Fatalf("ordinary operation attachment: %+v", rejected)
	}
}
