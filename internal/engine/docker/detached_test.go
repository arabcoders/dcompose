package docker

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
		ID: strings.Repeat("a", 64), Image: "sha256:" + strings.Repeat("b", 64), Project: "node",
		Mounts: []engine.ContainerMount{
			{Type: "volume", Source: "/var/lib/docker/volumes/cache/_data", Destination: "/cache", RW: true},
			{Type: "bind", Source: "/srv/stacks", Destination: "/stacks", RW: false},
		},
	}
}

func TestSelfContainerUnset(t *testing.T) {
	d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		if !reflect.DeepEqual([]string{"ps", "--quiet", "--no-trunc", "--filter", "label=DCOMPOSE_AGENT=true"}, cmd.Args) {
			t.Fatalf("unexpected discovery: %+v", cmd)
		}
		return nil, nil, nil
	})
	self, err := d.SelfContainer(context.Background())
	if nil != err || nil != self {
		t.Fatalf("unconfigured self: %+v, %v", self, err)
	}
}

func TestSelfContainer(t *testing.T) {
	self := detachedSelf()
	calls := 0
	d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		calls++
		if 1 == calls {
			if !reflect.DeepEqual([]string{"ps", "--quiet", "--no-trunc", "--filter", "label=DCOMPOSE_AGENT=true"}, cmd.Args) {
				t.Fatalf("discovery: %+v", cmd)
			}
			return []byte(self.ID + "\n"), nil, nil
		}
		want := []string{"inspect", "--type", "container", "--format", selfContainerFormat, "--", self.ID}
		if "docker" != cmd.Program || !reflect.DeepEqual(want, cmd.Args) {
			t.Fatalf("inspect: %+v", cmd)
		}
		return []byte(`{"ID":"` + self.ID + `","Image":"` + self.Image + `","Project":"node","Mounts":[{"Type":"volume","Source":"/data/cache","Destination":"/cache","RW":true}]}`), nil, nil
	})
	got, err := d.SelfContainer(context.Background())
	if nil != err || nil == got || self.ID != got.ID || self.Image != got.Image || self.Project != got.Project || 1 != len(got.Mounts) || !got.Mounts[0].RW || 2 != calls {
		t.Fatalf("self: %+v, %v", got, err)
	}
	if strings.Contains(selfContainerFormat, "{{json .}}") || strings.Contains(selfContainerFormat, ".Config.Env") {
		t.Fatal("inspect must not select container configuration or environment")
	}
}

func TestSelfContainerAmbiguous(t *testing.T) {
	calls := 0
	d := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
		calls++
		return []byte(strings.Repeat("a", 64) + "\n" + strings.Repeat("b", 64) + "\n"), nil, nil
	})
	_, err := d.SelfContainer(context.Background())
	var fault *engine.Error
	if !errors.As(err, &fault) || 502 != fault.Code || nil == fault.Cause || 1 != calls {
		t.Fatalf("ambiguous marker: %v", err)
	}
}

func TestInspectSelfInvalid(t *testing.T) {
	self := detachedSelf()
	for _, test := range []struct{ name, data string }{
		{"short_id", `{"ID":"abc","Image":"` + self.Image + `"}`},
		{"different_id", `{"ID":"` + strings.Repeat("c", 64) + `","Image":"` + self.Image + `"}`},
		{"tag", `{"ID":"` + self.ID + `","Image":"node:latest"}`},
		{"missing", `{}`},
		{"invalid_json", `invalid`},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if "ps" == cmd.Args[0] {
					return []byte(self.ID), nil, nil
				}
				return []byte(test.data), nil, nil
			})
			_, err := d.SelfContainer(context.Background())
			var fault *engine.Error
			if !errors.As(err, &fault) || 502 != fault.Code || nil == fault.Cause || strings.Contains(fault.Message, test.data) {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestDetachedMounts(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*engine.SelfContainer, *engine.Stack, *string)
		ok     bool
	}{
		{"shared", nil, true},
		{"bind_cache", func(s *engine.SelfContainer, _ *engine.Stack, _ *string) { s.Mounts[0].Type = "bind" }, true},
		{"volume_stack", func(s *engine.SelfContainer, _ *engine.Stack, _ *string) { s.Mounts[1].Type = "volume" }, true},
		{"rootfs_cache", func(_ *engine.SelfContainer, _ *engine.Stack, p *string) { *p = "/private/request.json" }, false},
		{"readonly_cache", func(s *engine.SelfContainer, _ *engine.Stack, _ *string) { s.Mounts[0].RW = false }, false},
		{"tmpfs_cache", func(s *engine.SelfContainer, _ *engine.Stack, _ *string) { s.Mounts[0].Type = "tmpfs" }, false},
		{"stack_dir", func(_ *engine.SelfContainer, s *engine.Stack, _ *string) { s.Dir = "/private" }, false},
		{"compose_file", func(_ *engine.SelfContainer, s *engine.Stack, _ *string) { s.ComposeFile = "/private/compose.yml" }, false},
		{"env_file", func(_ *engine.SelfContainer, s *engine.Stack, _ *string) { s.EnvFile = "/private/.env" }, false},
		{"null_env", func(_ *engine.SelfContainer, s *engine.Stack, _ *string) { s.EnvFile = "/dev/null" }, true},
		{"empty_env", func(_ *engine.SelfContainer, s *engine.Stack, _ *string) { s.EnvFile = "" }, true},
		{"prefix", func(_ *engine.SelfContainer, _ *engine.Stack, p *string) { *p = "/cache-other/request.json" }, false},
		{"traversal", func(_ *engine.SelfContainer, _ *engine.Stack, p *string) { *p = "/cache/../private/request.json" }, false},
		{"nested_tmpfs", func(s *engine.SelfContainer, _ *engine.Stack, _ *string) {
			s.Mounts = append(s.Mounts, engine.ContainerMount{Type: "tmpfs", Destination: "/cache/operations", RW: true})
		}, false},
		{"nested_readonly", func(s *engine.SelfContainer, _ *engine.Stack, _ *string) {
			s.Mounts = append(s.Mounts, engine.ContainerMount{Type: "bind", Destination: "/cache/operations", RW: false})
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			self, stack, requestFile := detachedSelf(), testStack(), "/cache/operations/request.json"
			if nil != test.change {
				test.change(&self, &stack, &requestFile)
			}
			err := engine.ValidateDetachedMounts(self, requestFile, stack)
			if test.ok {
				if nil != err {
					t.Fatal(err)
				}
				return
			}
			var fault *engine.Error
			if !errors.As(err, &fault) || 409 != fault.Code || "" == fault.Message || nil == fault.Cause || strings.Contains(fault.Message, requestFile) || strings.Contains(fault.Message, "/private") {
				t.Fatalf("mount error: %v", err)
			}
		})
	}
}

func TestStartDetached(t *testing.T) {
	t.Setenv("DCOMPOSE_TEST_SECRET", "not-in-command-arguments")
	t.Setenv("DOCKER_CONFIG", "/cache/docker")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	self, name, requestFile := detachedSelf(), "dagent-operation-test", "/cache/operations/request.json"
	d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		prefix := []string{"run", "--detach", "--name", name, "--label", "io.dcompose.operation=test", "--restart=no", "--pull", "never", "--volumes-from", self.ID, "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())}
		if "docker" != cmd.Program || len(prefix) >= len(cmd.Args) || !reflect.DeepEqual(prefix, cmd.Args[:len(prefix)]) {
			t.Fatalf("run: %+v", cmd)
		}
		args := cmd.Args[len(prefix):]
		groups, err := os.Getgroups()
		if nil != err {
			t.Fatal(err)
		}
		for _, group := range groups {
			if 2 > len(args) || "--group-add" != args[0] || strconv.Itoa(group) != args[1] {
				t.Fatalf("supplementary groups: %v", args)
			}
			args = args[2:]
		}
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if 2 > len(args) || "--env" != args[0] || key != args[1] {
				t.Fatalf("environment name %q not forwarded", key)
			}
			args = args[2:]
		}
		want := []string{"--entrypoint", "dagent", self.Image, "--run-operation", requestFile}
		if !reflect.DeepEqual(want, args) || "" != cmd.Dir || 0 != len(cmd.Env) {
			t.Fatalf("helper: %+v", cmd)
		}
		return []byte("helper-id"), nil, nil
	})
	if err := d.StartDetached(context.Background(), self, name, requestFile, testStack()); nil != err {
		t.Fatal(err)
	}
}

func TestStartDetachedReject(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*engine.SelfContainer)
	}{
		{"mounts", func(s *engine.SelfContainer) { s.Mounts = nil }},
		{"image", func(s *engine.SelfContainer) { s.Image = "node:latest" }},
		{"id", func(s *engine.SelfContainer) { s.ID = "node" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			self := detachedSelf()
			test.change(&self)
			d := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
				t.Fatal("must reject before invoking docker")
				return nil, nil, nil
			})
			err := d.StartDetached(context.Background(), self, "dagent-operation-test", "/cache/operations/request.json", testStack())
			var fault *engine.Error
			if !errors.As(err, &fault) || 409 != fault.Code {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestDetachedState(t *testing.T) {
	for _, state := range []engine.DetachedState{{Running: true}, {ExitCode: 0}, {ExitCode: 17}} {
		calls := 0
		d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
			calls++
			if 2 == calls {
				if !reflect.DeepEqual([]string{"logs", "--", "dagent-operation-test"}, cmd.Args) {
					t.Fatalf("helper logs: %+v", cmd)
				}
				return []byte("stdout\n"), []byte("stderr\n"), nil
			}
			want := []string{"inspect", "--type", "container", "--format", `{"Running":{{json .State.Running}},"ExitCode":{{json .State.ExitCode}}}`, "--", "dagent-operation-test"}
			if !reflect.DeepEqual(want, cmd.Args) {
				t.Fatalf("state inspect: %+v", cmd)
			}
			return []byte(`{"Running":` + strconv.FormatBool(state.Running) + `,"ExitCode":` + strconv.Itoa(state.ExitCode) + `}`), nil, nil
		})
		got, err := d.DetachedState(context.Background(), "dagent-operation-test")
		wantCalls := 1
		if !state.Running {
			state.Output = "stdout\nstderr\n"
			wantCalls = 2
		}
		if nil != err || state != got || wantCalls != calls {
			t.Fatalf("state: %+v, %v", got, err)
		}
	}
}

func TestDetachedLogsError(t *testing.T) {
	calls := 0
	d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		calls++
		if 1 == calls {
			return []byte(`{"Running":false,"ExitCode":17}`), nil, nil
		}
		if !reflect.DeepEqual([]string{"logs", "--", "dagent-operation-test"}, cmd.Args) {
			t.Fatalf("logs: %+v", cmd)
		}
		return []byte("partial logs\n"), []byte("logs unavailable"), errors.New("exit status 1")
	})
	state, err := d.DetachedState(context.Background(), "dagent-operation-test")
	var fault *engine.Error
	if !errors.As(err, &fault) || 502 != fault.Code || state.Running || 17 != state.ExitCode || "partial logs\nlogs unavailable" != state.Output || 2 != calls {
		t.Fatalf("state: %+v, error: %v", state, err)
	}
}

func TestRemoveDetached(t *testing.T) {
	d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		if !reflect.DeepEqual([]string{"rm", "--", "dagent-operation-test"}, cmd.Args) {
			t.Fatalf("remove: %+v", cmd)
		}
		return nil, nil, nil
	})
	if err := d.RemoveDetached(context.Background(), "dagent-operation-test"); nil != err {
		t.Fatal(err)
	}
}

func TestDetachedCommandErrors(t *testing.T) {
	for _, operation := range []string{"inspect", "start", "state", "remove"} {
		t.Run(operation, func(t *testing.T) {
			calls := 0
			cause := errors.New("exit status 1")
			d := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
				calls++
				return []byte("inspect-stdout-secret"), []byte("daemon diagnostic"), cause
			})
			var err error
			switch operation {
			case "inspect":
				_, err = d.SelfContainer(context.Background())
			case "start":
				err = d.StartDetached(context.Background(), detachedSelf(), "dagent-operation-test", "/cache/operations/request.json", testStack())
			case "state":
				_, err = d.DetachedState(context.Background(), "dagent-operation-test")
			case "remove":
				err = d.RemoveDetached(context.Background(), "dagent-operation-test")
			}
			var fault *engine.Error
			if 1 != calls || !errors.As(err, &fault) || 502 != fault.Code || !errors.Is(err, cause) || !strings.Contains(fault.Cause.Error(), "daemon diagnostic") || strings.Contains(fault.Message, "diagnostic") || strings.Contains(err.Error(), "stdout-secret") {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
