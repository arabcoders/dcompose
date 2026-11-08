package podman

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

func TestSelfNative(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	p := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
		t.Fatal("native mode discovered a container")
		return nil, nil, nil
	})
	self, err := p.SelfContainer(context.Background())
	if nil != err || nil != self {
		t.Fatalf("native self: %+v %v", self, err)
	}
}

func TestSelfContainer(t *testing.T) {
	host := "unix:///container/path/podman.sock"
	t.Setenv("CONTAINER_HOST", host)
	id := strings.Repeat("a", 64)
	image := strings.Repeat("b", 64)
	for _, test := range []struct {
		name, ids, data string
		fail            string
		calls, code     int
		found           bool
	}{
		{name: "none", calls: 1},
		{name: "found", ids: id, data: `{"ID":"` + id + `","Image":"` + image + `","Project":"node","Mounts":[{"Type":"bind","Source":"/data/cache","Destination":"/cache","RW":true}],"Tmpfs":{"/cache/tmp":"rw"}}`, calls: 2, found: true},
		{name: "prefixed image", ids: id, data: `{"ID":"` + id + `","Image":"sha256:` + image + `","Project":"node"}`, calls: 2, found: true},
		{name: "tag", ids: id, data: `{"ID":"` + id + `","Image":"node:latest","Project":"node"}`, calls: 2, code: 502},
		{name: "ambiguous", ids: id + "\n" + strings.Repeat("b", 64), calls: 1, code: 502},
		{name: "ps error", fail: "ps", calls: 1, code: 502},
		{name: "inspect error", ids: id, fail: "inspect", calls: 2, code: 502},
		{name: "invalid JSON", ids: id, data: "invalid", calls: 2, code: 502},
		{name: "short ID", ids: id, data: `{"ID":"abc","Project":"node"}`, calls: 2, code: 502},
		{name: "different ID", ids: id, data: `{"ID":"` + strings.Repeat("b", 64) + `","Project":"node"}`, calls: 2, code: 502},
		{name: "no project", ids: id, data: `{"ID":"` + id + `"}`, calls: 2, code: 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				calls++
				if "podman" != cmd.Program || !reflect.DeepEqual([]string{"--remote", "--url", host}, cmd.Args[:3]) {
					t.Fatalf("endpoint: %+v", cmd)
				}
				args := cmd.Args[3:]
				if test.fail == args[0] {
					return nil, []byte("private diagnostic"), errors.New("engine failure")
				}
				if "ps" == args[0] {
					if !reflect.DeepEqual([]string{"ps", "--quiet", "--no-trunc", "--filter", "label=DCOMPOSE_AGENT=true"}, args) {
						t.Fatalf("discovery: %+v", cmd)
					}
					return []byte(test.ids), nil, nil
				}
				want := []string{"inspect", "--type", "container", "--format", selfContainerFormat, "--", id}
				if !reflect.DeepEqual(want, args) {
					t.Fatalf("inspect: %+v", cmd)
				}
				return []byte(test.data), nil, nil
			})
			self, err := p.SelfContainer(context.Background())
			var fault *engine.Error
			if (0 != test.code) != errors.As(err, &fault) || (nil != fault && test.code != fault.Code) || test.calls != calls {
				t.Fatalf("self error: %v, calls: %d", err, calls)
			}
			if test.found != (nil != self) || (nil != self && (id != self.ID || "node" != self.Project)) {
				t.Fatalf("self: %+v", self)
			}
			if "found" == test.name && (2 != len(self.Mounts) || "tmpfs" != self.Mounts[1].Type || "/cache/tmp" != self.Mounts[1].Destination || !self.Mounts[0].RW) {
				t.Fatalf("mounts: %+v", self.Mounts)
			}
		})
	}
	if strings.Contains(selfContainerFormat, ".Config.Env") || strings.Contains(selfContainerFormat, "{{json .}}") {
		t.Fatal("inspect selects container environment or full configuration")
	}
}
