package podman

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

func testStack() engine.Stack {
	return engine.Stack{Name: "demo", Dir: "/stacks/a b", ComposeFile: "/stacks/a b/compose.yml", EnvFile: "/stacks/a b/.env"}
}

func TestCheck(t *testing.T) {
	infoFixture := readFixture(t, "info.json")
	wantEnv := []string{"DOCKER_HOST=unix:///run/user/1000/podman/podman.sock", "DOCKER_CONTEXT=", "DOCKER_TLS_VERIFY=", "DOCKER_CERT_PATH="}
	t.Setenv("DOCKER_CONTEXT", "unrelated-docker")
	t.Setenv("DOCKER_HOST", "tcp://unrelated-docker:2376")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", "/docker-certs")
	calls := 0
	p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		calls++
		if 1 == calls {
			if "podman" != cmd.Program || !reflect.DeepEqual([]string{"info", "--format", "json"}, cmd.Args) {
				t.Fatalf("info: %+v", cmd)
			}
			return []byte(infoFixture), nil, nil
		}
		if "docker" != cmd.Program || !reflect.DeepEqual(wantEnv, cmd.Env) {
			t.Fatalf("compose target: %+v", cmd)
		}
		return []byte("Docker Compose version v5.6.1 --no-env-resolution"), nil, nil
	})
	if err := p.Check(context.Background()); nil != err || 3 != calls {
		t.Fatalf("check: %v, calls: %d", err, calls)
	}
	for _, cmd := range []engine.Command{p.Action(testStack(), "deploy", "web"), p.Logs(testStack())} {
		for _, env := range wantEnv {
			if !slices.Contains(cmd.Env, env) {
				t.Fatalf("lost socket environment: %+v", cmd)
			}
		}
	}
}

func TestCheckErrors(t *testing.T) {
	infoFixture := readFixture(t, "info.json")
	for _, test := range []struct{ name, old, replacement string }{
		{"remote", `"serviceIsRemote":false`, `"serviceIsRemote":true`},
		{"non-linux", `"os":"linux"`, `"os":"darwin"`},
		{"events", `"eventLogger":"journald"`, `"eventLogger":"none"`},
		{"missing events", `"eventLogger":"journald"`, `"eventLogger":""`},
		{"socket", `"path":"/run/user/1000/podman/podman.sock"`, `"path":"ssh://remote/run/podman.sock"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				calls++
				if "podman" != cmd.Program {
					t.Fatalf("unsupported mode reached Compose: %+v", cmd)
				}
				return []byte(strings.Replace(infoFixture, test.old, test.replacement, 1)), nil, nil
			})
			if err := p.Check(context.Background()); nil == err || 1 != calls {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestMountedSocket(t *testing.T) {
	host := "unix:///container/path/podman.sock"
	t.Setenv("CONTAINER_HOST", host)
	t.Setenv("DOCKER_HOST", "unix:///wrong/docker.sock")
	t.Setenv("DOCKER_CONTEXT", "unrelated-docker")
	fixture := readFixture(t, "info.json")
	for _, remote := range []bool{false, true} {
		t.Run(strconv.FormatBool(remote), func(t *testing.T) {
			calls := 0
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				calls++
				if "podman" == cmd.Program {
					want := []string{"--remote", "--url", host, "info", "--format", "json"}
					if !reflect.DeepEqual(want, cmd.Args) {
						t.Fatalf("info: %+v", cmd)
					}
					return []byte(strings.Replace(fixture, `"serviceIsRemote":false`, `"serviceIsRemote":`+strconv.FormatBool(remote), 1)), nil, nil
				}
				if "docker" != cmd.Program || !reflect.DeepEqual(composeEnv(host), cmd.Env) {
					t.Fatalf("compose: %+v", cmd)
				}
				return []byte("--no-env-resolution"), nil, nil
			})
			if err := p.Check(context.Background()); nil != err || 3 != calls {
				t.Fatalf("check: %v, calls: %d", err, calls)
			}
			if err := p.Probe(context.Background()); nil != err || 4 != calls {
				t.Fatalf("probe: %v, calls: %d", err, calls)
			}
			for _, cmd := range []engine.Command{p.Action(testStack(), "deploy", "web"), p.Logs(testStack())} {
				if !slices.Contains(cmd.Env, "DOCKER_HOST="+host) || !slices.Contains(cmd.Env, "DOCKER_CONTEXT=") {
					t.Fatalf("compose endpoint: %+v", cmd)
				}
			}
			for _, cmd := range []engine.Command{p.Events(time.Unix(12, 34)), p.Shell("container", "bash", "session")} {
				if "podman" != cmd.Program || !reflect.DeepEqual([]string{"--remote", "--url", host}, cmd.Args[:3]) {
					t.Fatalf("native endpoint: %+v", cmd)
				}
			}
		})
	}
}

func TestHostErrors(t *testing.T) {
	for _, host := range []string{"tcp://host:2375", "ssh://user@host/run/podman.sock", "/run/podman.sock", "unix://host/run/podman.sock", "unix://relative", "unix:///run/podman.sock?query=1", "unix:///run/podman.sock#fragment", "unix:///%"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("CONTAINER_HOST", host)
			p := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
				t.Fatal("unsupported endpoint reached engine")
				return nil, nil, nil
			})
			if err := p.Check(context.Background()); nil == err {
				t.Fatal("accepted unsupported endpoint")
			}
			if err := p.Probe(context.Background()); nil == err {
				t.Fatal("probed unsupported endpoint")
			}
		})
	}
}

func TestSocketLinux(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "unix:///container/path/podman.sock")
	fixture := strings.Replace(readFixture(t, "info.json"), `"os":"linux"`, `"os":"darwin"`, 1)
	calls := 0
	p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		calls++
		if "podman" != cmd.Program {
			t.Fatalf("non-Linux engine reached Compose: %+v", cmd)
		}
		return []byte(fixture), nil, nil
	})
	if err := p.Check(context.Background()); nil == err || 1 != calls {
		t.Fatalf("check: %v, calls: %d", err, calls)
	}
}

func TestSocketCommands(t *testing.T) {
	host := "unix:///container/path/podman.sock"
	t.Setenv("CONTAINER_HOST", host)
	id := strings.Repeat("a", 64)
	image := `{"id":"` + id + `","ref":"quay.io/team/app:edge"}`
	for _, test := range []struct {
		name string
		data []string
		run  func(*Podman) error
	}{
		{"stats", []string{readFixture(t, "info.json"), readFixture(t, "stats-underscored.json")}, func(p *Podman) error { _, err := p.NodeInfo(context.Background()); return err }},
		{"resolve", []string{readFixture(t, "container-inspect.json")}, func(p *Podman) error {
			_, err := p.ResolveContainer(context.Background(), testStack(), "demo-web")
			return err
		}},
		{"cleanup", []string{""}, func(p *Podman) error { return p.CleanupShell(context.Background(), id, "session") }},
		{"changed", []string{image, id}, func(p *Podman) error {
			_, err := p.ImageChanged(context.Background(), engine.Service{Name: "demo"})
			return err
		}},
		{"inspect", []string{image, readFixture(t, "image-inspect.json")}, func(p *Podman) error {
			if target := p.InspectImage(context.Background(), engine.Service{Name: "demo"}); "" != target.Status {
				return errors.New("image inspection failed")
			}
			return nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if "podman" != cmd.Program || 3 > len(cmd.Args) || !reflect.DeepEqual([]string{"--remote", "--url", host}, cmd.Args[:3]) {
					t.Fatalf("native endpoint: %+v", cmd)
				}
				output := test.data[calls]
				calls++
				return []byte(output), nil, nil
			})
			if err := test.run(p); nil != err || len(test.data) != calls {
				t.Fatalf("command: %v, calls: %d", err, calls)
			}
		})
	}
}

func TestSocketFallback(t *testing.T) {
	infoFixture := readFixture(t, "info.json")
	t.Setenv("XDG_RUNTIME_DIR", "/runtime/a b")
	if "/run/podman/podman.sock" != localSocket(false) || "/runtime/a b/podman/podman.sock" != localSocket(true) {
		t.Fatal("incorrect socket defaults")
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	want := filepath.Join("/run/user", strconv.Itoa(os.Geteuid()), "podman/podman.sock")
	if want != localSocket(true) {
		t.Fatalf("rootless fallback: %s", localSocket(true))
	}
	for _, rootless := range []bool{false, true} {
		socket := "/run/podman/podman.sock"
		if rootless {
			socket = want
		}
		wantEnv := []string{"DOCKER_HOST=unix://" + socket, "DOCKER_CONTEXT=", "DOCKER_TLS_VERIFY=", "DOCKER_CERT_PATH="}
		p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
			if "podman" == cmd.Program {
				fixture := strings.Replace(infoFixture, `"path":"/run/user/1000/podman/podman.sock"`, `"path":""`, 1)
				fixture = strings.Replace(fixture, `"rootless":true`, `"rootless":`+strconv.FormatBool(rootless), 1)
				return []byte(fixture), nil, nil
			}
			if !reflect.DeepEqual(wantEnv, cmd.Env) {
				t.Fatalf("fallback target: %+v", cmd)
			}
			return []byte("--no-env-resolution"), nil, nil
		})
		if err := p.Check(context.Background()); nil != err {
			t.Fatal(err)
		}
	}
}

func TestProbe(t *testing.T) {
	failure := errors.New("engine unavailable")
	p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		if "podman" != cmd.Program || !reflect.DeepEqual([]string{"info", "--format", "json"}, cmd.Args) {
			t.Fatalf("probe: %+v", cmd)
		}
		return nil, []byte("permission denied"), failure
	})
	if err := p.Probe(context.Background()); !errors.Is(err, failure) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("probe: %v", err)
	}
}

func TestEvents(t *testing.T) {
	fixture := readFixture(t, "event.json")
	imageEvent := strings.Replace(fixture, `"Type":"container"`, `"Type":"image"`, 1)
	p := New(nil)
	cmd := p.Events(time.Unix(12, 34))
	want := []string{"events", "--since", "1970-01-01T00:00:12.000000034Z", "--format", "json", "--filter", "type=container"}
	if "podman" != cmd.Program || !reflect.DeepEqual(want, cmd.Args) {
		t.Fatalf("events: %+v", cmd)
	}
	for _, test := range []struct{ data, stack string }{
		{fixture, "demo"},
		{strings.Replace(fixture, `"Status":"start"`, `"Status":"health_status"`, 1), "demo"},
		{strings.Replace(fixture, `"Status":"start"`, `"Status":"exec"`, 1), ""},
		{strings.Replace(fixture, `"Status":"start"`, `"Status":"exec_died"`, 1), ""},
		{strings.Replace(imageEvent, `"Status":"start"`, `"Status":"remove"`, 1), ""},
		{strings.Replace(fixture, "com.docker.compose.project", "io.podman.compose.project", 1), ""},
		{`{"Type":"container","Status":"start"}`, ""},
		{"invalid", ""},
	} {
		if got := p.EventStack([]byte(test.data)); test.stack != got {
			t.Fatalf("event: %q, want %q", got, test.stack)
		}
	}
}

func TestImageRefs(t *testing.T) {
	p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		if "docker" != cmd.Program || !slices.Contains(cmd.Args, "--images") || !slices.Contains(cmd.Args, "--no-env-resolution") || !slices.Contains(cmd.Env, "DOCKER_CONTEXT=") {
			t.Fatalf("images: %+v", cmd)
		}
		return []byte("nginx nginx:latest example.com/a:v1"), nil, nil
	})
	refs, err := p.ImageRefs(context.Background(), testStack())
	if nil != err || !reflect.DeepEqual(map[string]bool{"nginx:latest": true, "example.com/a:v1": true}, refs) {
		t.Fatalf("refs: %v, error: %v", refs, err)
	}
}
