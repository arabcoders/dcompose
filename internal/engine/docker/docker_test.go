package docker

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

func testStack() engine.Stack {
	return engine.Stack{Name: "demo", Dir: "/stacks/a b", ComposeFile: "/stacks/a b/compose.yml", EnvFile: "/stacks/a b/.env"}
}

func TestEvents(t *testing.T) {
	d := New(nil)
	cmd := d.Events(time.Unix(12, 34))
	want := []string{"events", "--since", "12.000000034", "--format", "{{json .}}", "--filter", "type=container", "--filter", "label=com.docker.compose.project"}
	if !reflect.DeepEqual(want, cmd.Args) {
		t.Fatalf("events: %+v", cmd)
	}
	for _, test := range []struct{ data, stack string }{
		{`{"Type":"container","Action":"start","Actor":{"Attributes":{"com.docker.compose.project":"demo"}}}`, "demo"},
		{`{"Type":"container","Action":"exec_start","Actor":{"Attributes":{"com.docker.compose.project":"demo"}}}`, ""},
		{`{"Type":"image","Actor":{"Attributes":{"com.docker.compose.project":"demo"}}}`, ""},
		{"invalid", ""},
	} {
		if got := d.EventStack([]byte(test.data)); test.stack != got {
			t.Fatalf("event: %q", got)
		}
	}
}

func TestShell(t *testing.T) {
	id := strings.Repeat("a", 64)
	d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		if !reflect.DeepEqual([]string{"inspect", "--type", "container", "--", "demo-web"}, cmd.Args) {
			t.Fatalf("inspect: %+v", cmd)
		}
		return []byte(`[{"Id":"` + id + `","State":{"Running":true},"Config":{"Labels":{"com.docker.compose.project":"demo"}}}]`), nil, nil
	})
	container, err := d.ResolveContainer(context.Background(), testStack(), "demo-web")
	if nil != err || id != container {
		t.Fatalf("container: %s %v", container, err)
	}
	cmd := d.Shell(container, "bash", "session")
	if !reflect.DeepEqual([]string{"exec", "-it", "--detach-keys=", "--env", "DOCKER_NODE_SHELL_ID=session", "--env", "TERM=xterm-256color", id, "sh", "-c", shellBootstrap, "docker-node-shell", "bash"}, cmd.Args) {
		t.Fatalf("shell: %+v", cmd)
	}
}
