package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder/websocket"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if nil != err {
		t.Fatal(err)
	}
	return string(data)
}

type workspaceFixture struct {
	channel *channelFixture
	server  *Server
	node    *websocket.Conn
	app     *httptest.Server
}

func newWorkspace(t *testing.T) *workspaceFixture {
	t.Helper()
	channel := fakeChannel(t)
	server := testServer(t, channel.server)
	startChannels(t, server)
	node := nextConnection(t, channel)
	app := httptest.NewServer(server)
	t.Cleanup(app.Close)
	return &workspaceFixture{channel: channel, server: server, node: node, app: app}
}
