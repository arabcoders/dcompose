package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func TestContainerLogs(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	write(t, filepath.Join(fake, "ps"),
		`[{"ID":"`+first+`","Name":"demo-web-1","Service":"web","State":"running"},`+
			`{"ID":"`+second+`","Name":"demo-web-2","Service":"web","State":"exited","ExitCode":0}]`)
	conn := openChannel(t, channelServer(t, a))
	// The exact replica name must select its own engine ID, and a stopped
	// replica resolves without a running-state requirement.
	channelSend(t, conn, protocol.ChannelMessage{Type: "logs.attach", Stream: "clogs", Resource: "demo", Container: "demo-web-2"})
	message := channelUntil(t, conn, "stream.event", "message")
	if "clogs" != message.Stream || "demo-web-2" != message.Container || `{"message":"container hello"}` != string(message.Payload) {
		t.Fatalf("filtered log frame: %+v", message)
	}
	done := channelUntil(t, conn, "stream.event", "done")
	if "clogs" != done.Stream || "demo-web-2" != done.Container || `{"exit_code":3}` != string(done.Payload) {
		t.Fatalf("terminal event: %+v", done)
	}
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	if !strings.Contains(string(argv), "<logs><--follow><--tail><100><"+second) || strings.Contains(string(argv), "--no-color") {
		t.Fatalf("native logs argv: %s", argv)
	}
}

func TestContainerLogsAbsent(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	write(t, filepath.Join(fake, "ps"), `[{"ID":"`+strings.Repeat("a", 64)+`","Name":"demo-web-1","State":"running"}]`)
	conn := openChannel(t, channelServer(t, a))
	// The name exists only in another stack: the project-scoped inventory of
	// this stack must not resolve it.
	channelSend(t, conn, protocol.ChannelMessage{Type: "logs.attach", Stream: "absent", Resource: "demo", Container: "other-web-1"})
	message := channelUntil(t, conn, "stream.error", "")
	if "absent" != message.Stream || 404 != message.Status || "" == message.Error || strings.Contains(message.Error, fake) {
		t.Fatalf("absent container: %+v", message)
	}
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	if strings.Contains(string(argv), "<logs>") {
		t.Fatalf("absent container started logs: %s", argv)
	}
}
