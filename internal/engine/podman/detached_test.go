package podman

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

func detachedSelf() engine.SelfContainer {
	return engine.SelfContainer{
		ID: strings.Repeat("a", 64), Image: strings.Repeat("b", 64), Project: "node",
		Mounts: []engine.ContainerMount{
			{Type: "volume", Destination: "/cache", RW: true},
			{Type: "bind", Destination: "/stacks", RW: false},
		},
	}
}

func TestStartDetached(t *testing.T) {
	host := "unix:///mounted/podman.sock"
	t.Setenv("CONTAINER_HOST", host)
	t.Setenv("DCOMPOSE_TEST_SECRET", "not-in-arguments")
	self := detachedSelf()
	p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		prefix := []string{"--remote", "--url", host, "run", "--detach", "--name", "dagent-operation-test", "--label", "io.dcompose.operation=test", "--restart=no", "--pull", "never", "--volumes-from", self.ID, "--security-opt", "label=disable", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())}
		if "podman" != cmd.Program || len(prefix) >= len(cmd.Args) || !reflect.DeepEqual(prefix, cmd.Args[:len(prefix)]) {
			t.Fatalf("run: %+v", cmd)
		}
		args := cmd.Args[len(prefix):]
		groups, err := os.Getgroups()
		if nil != err {
			t.Fatal(err)
		}
		for _, group := range groups {
			if 2 > len(args) || "--group-add" != args[0] || strconv.Itoa(group) != args[1] {
				t.Fatalf("groups: %v", args)
			}
			args = args[2:]
		}
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if "CONTAINER_HOST" == key {
				continue
			}
			if 2 > len(args) || "--env" != args[0] || key != args[1] {
				t.Fatalf("environment key %q: %v", key, args)
			}
			args = args[2:]
		}
		want := []string{"--env", "CONTAINER_HOST", "--entrypoint", "dagent", self.Image, "--run-operation", "/cache/request.json"}
		if !reflect.DeepEqual(want, args) || !reflect.DeepEqual([]string{"CONTAINER_HOST=" + host}, cmd.Env) || "" != cmd.Dir {
			t.Fatalf("helper: %+v", cmd)
		}
		return []byte("helper-id"), nil, nil
	})
	// The endpoint captured by the driver must also reach the helper environment.
	t.Setenv("CONTAINER_HOST", "unix:///wrong/podman.sock")
	if err := p.StartDetached(context.Background(), self, "dagent-operation-test", "/cache/request.json", testStack()); nil != err {
		t.Fatal(err)
	}
}

func TestDetachedReject(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*engine.SelfContainer)
	}{
		{"mounts", func(self *engine.SelfContainer) { self.Mounts = nil }},
		{"readonly", func(self *engine.SelfContainer) { self.Mounts[0].RW = false }},
		{"tmpfs", func(self *engine.SelfContainer) {
			self.Mounts = append(self.Mounts, engine.ContainerMount{Type: "tmpfs", Destination: "/cache/operations", RW: true})
		}},
		{"image", func(self *engine.SelfContainer) { self.Image = "node:latest" }},
		{"id", func(self *engine.SelfContainer) { self.ID = "node" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			self := detachedSelf()
			test.change(&self)
			p := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
				t.Fatal("invalid helper reached runtime")
				return nil, nil, nil
			})
			err := p.StartDetached(context.Background(), self, "dagent-operation-test", "/cache/operations/request.json", testStack())
			var fault *engine.Error
			if !errors.As(err, &fault) || 409 != fault.Code || nil == fault.Cause {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestDetachedState(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "unix:///mounted/podman.sock")
	for _, state := range []engine.DetachedState{{Running: true}, {}, {ExitCode: 17}} {
		calls := 0
		p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
			calls++
			if "podman" != cmd.Program || !reflect.DeepEqual([]string{"--remote", "--url", "unix:///mounted/podman.sock"}, cmd.Args[:3]) {
				t.Fatalf("endpoint: %+v", cmd)
			}
			if 2 == calls {
				if !reflect.DeepEqual([]string{"logs", "--", "helper"}, cmd.Args[3:]) {
					t.Fatalf("logs: %+v", cmd)
				}
				return []byte("out\n"), []byte("err\n"), nil
			}
			if "inspect" != cmd.Args[3] || "helper" != cmd.Args[len(cmd.Args)-1] {
				t.Fatalf("inspect: %+v", cmd)
			}
			return []byte(`{"Running":` + strconv.FormatBool(state.Running) + `,"ExitCode":` + strconv.Itoa(state.ExitCode) + `}`), nil, nil
		})
		got, err := p.DetachedState(context.Background(), "helper")
		wantCalls := 1
		if !state.Running {
			state.Output = "out\nerr\n"
			wantCalls = 2
		}
		if nil != err || state != got || wantCalls != calls {
			t.Fatalf("state: %+v, error: %v, calls: %d", got, err, calls)
		}
	}
}

func TestRemoveDetached(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "unix:///mounted/podman.sock")
	p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		if "podman" != cmd.Program || !reflect.DeepEqual([]string{"--remote", "--url", "unix:///mounted/podman.sock", "rm", "--", "helper"}, cmd.Args) {
			t.Fatalf("remove: %+v", cmd)
		}
		return nil, nil, nil
	})
	if err := p.RemoveDetached(context.Background(), "helper"); nil != err {
		t.Fatal(err)
	}
}

func TestDetachedErrors(t *testing.T) {
	for _, action := range []string{"start", "state", "logs", "remove"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("CONTAINER_HOST", "unix:///mounted/podman.sock")
			cause := errors.New("runtime failure")
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if "logs" == action && "inspect" == cmd.Args[3] {
					return []byte(`{"Running":false,"ExitCode":17}`), nil, nil
				}
				return []byte("stdout-secret"), []byte("private diagnostic"), cause
			})
			var err error
			switch action {
			case "start":
				err = p.StartDetached(context.Background(), detachedSelf(), "helper", "/cache/request.json", testStack())
			case "state", "logs":
				var state engine.DetachedState
				state, err = p.DetachedState(context.Background(), "helper")
				if "logs" == action && (17 != state.ExitCode || !strings.Contains(state.Output, "stdout-secret")) {
					t.Fatalf("lost helper output: %+v", state)
				}
			case "remove":
				err = p.RemoveDetached(context.Background(), "helper")
			}
			var fault *engine.Error
			if !errors.As(err, &fault) || 502 != fault.Code || !errors.Is(err, cause) || !strings.Contains(fault.Cause.Error(), "private diagnostic") || strings.Contains(fault.Message, "private") || strings.Contains(err.Error(), "stdout-secret") {
				t.Fatalf("error boundary: %v", err)
			}
		})
	}
}
