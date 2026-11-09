package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/engine/compose"
	"github.com/arabcoders/dcompose/internal/engine/docker"
	"github.com/arabcoders/dcompose/internal/engine/podman"
)

// Unused methods panic via the embedded nil driver: the core must not reach
// around the engine to issue extra runtime probes for these operations.
type testEngine struct {
	engine.Driver
	t         *testing.T
	validated bool
}

func TestEngineChoice(t *testing.T) {
	for _, name := range []string{"docker", "podman"} {
		t.Run(name, func(t *testing.T) {
			c, err := loadConfig(configFixture(t, "engine = '"+name+"'", ""))
			if nil != err {
				t.Fatal(err)
			}
			a := newAgent(context.Background(), c)
			switch name {
			case "docker":
				if _, ok := a.engine.(*docker.Docker); !ok {
					t.Fatalf("driver: %T", a.engine)
				}
			case "podman":
				if _, ok := a.engine.(*podman.Podman); !ok {
					t.Fatalf("driver: %T", a.engine)
				}
			}
		})
	}
}

func (e *testEngine) Validate(_ context.Context, s engine.Stack) error {
	compose, err := os.ReadFile(s.ComposeFile)
	if nil != err || "engine-neutral document" != string(compose) || !strings.HasPrefix(filepath.Base(s.ComposeFile), ".dagent-compose-") {
		e.t.Fatalf("staged compose: %+v %q %v", s, compose, err)
	}
	env, err := os.ReadFile(s.EnvFile)
	if nil != err || "VALUE=one" != string(env) {
		e.t.Fatalf("staged env: %q %v", env, err)
	}
	e.validated = true
	return nil
}

func (e *testEngine) Containers(context.Context, engine.Stack) ([]engine.Service, error) {
	return []engine.Service{{Name: "web", State: "running", PortBindings: []engine.PortBinding{}}}, nil
}

func (e *testEngine) Action(s engine.Stack, action, service string) engine.Command {
	if "stop" != action || "" != service || "demo" != s.Name {
		e.t.Fatalf("action: %+v %q %q", s, action, service)
	}
	return engine.Command{Program: "sh", Args: []string{"-c", "printf 'runtime stopped\\n'"}, Dir: s.Dir}
}

func TestEngineBoundary(t *testing.T) {
	a, fake := fixture(t)
	driver := &testEngine{t: t}
	a.engine = driver
	w := request(a, "PUT", "/stacks/demo", `{"compose":"engine-neutral document","env":"VALUE=one"}`)
	if 200 != w.Code || !driver.validated {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	w = request(a, "GET", "/stacks/demo/status", "")
	if 200 != w.Code || !strings.Contains(w.Body.String(), `"status":"running"`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	op := completed(t, a, request(a, "POST", "/stacks/demo/actions", `{"action":"stop"}`))
	if "succeeded" != op.Status || !strings.Contains(op.Output, "runtime stopped") {
		t.Fatalf("action: %+v", op)
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatalf("core bypassed driver: %v", err)
	}
}

func TestRuntimeSchedulingLabels(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	for _, name := range []string{"docker", "podman"} {
		t.Run(name, func(t *testing.T) {
			inspected := false
			run := func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				args := strings.Join(cmd.Args, " ")
				if strings.Contains(args, "ps --all --format json") {
					return []byte(`[{"ID":"id","Name":"demo-web-1","Service":"web","Image":"nginx:latest","State":"running"}]`), nil, nil
				}
				if name != cmd.Program || !strings.Contains(args, "container inspect --format "+compose.ContainerInfoFormat+" demo-web-1") {
					t.Fatalf("label inspection: %+v", cmd)
				}
				inspected = true
				return []byte(
					`{"labels":{"io.dcompose.check-interval":"30m","io.dcompose.auto-update":"true","private.label":"private-label-value"}}`,
				), nil, nil
			}
			var driver engine.Driver = docker.New(run)
			if "podman" == name {
				driver = podman.New(run)
			}
			services, err := driver.Containers(
				context.Background(),
				engine.Stack{Name: "demo", Dir: "/stacks/demo", ComposeFile: "/stacks/demo/compose.yaml", EnvFile: "/dev/null"},
			)
			if nil != err || !inspected || 1 != len(services) || "30m" != services[0].CheckInterval || !services[0].AutoUpdate {
				t.Fatalf("runtime labels: %+v %v", services, err)
			}
			data, err := json.Marshal(services)
			if nil != err || strings.Contains(string(data), "private-label-value") {
				t.Fatal("arbitrary labels escaped into runtime metadata")
			}
		})
	}
}

func TestRuntimeExitCode(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	for _, name := range []string{"docker", "podman"} {
		t.Run(name, func(t *testing.T) {
			a, _ := fixture(t)
			seed(t, a, "demo", "compose.yaml")
			calls := 0
			run := func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				calls++
				if strings.Contains(strings.Join(cmd.Args, " "), "ps --all --format json") {
					return []byte(
						`[{"Name":"failed","State":"exited","ExitCode":7},{"Name":"success","State":"exited","ExitCode":0},{"Name":"unknown","State":"exited"}]`,
					), nil, nil
				}
				if name != cmd.Program ||
					!strings.Contains(strings.Join(cmd.Args, " "), "container inspect --format "+compose.ContainerInfoFormat) {
					t.Fatalf("unexpected runtime command: %+v", cmd)
				}
				return []byte(`{}`), nil, nil
			}
			a.engine = docker.New(run)
			if "podman" == name {
				a.engine = podman.New(run)
			}
			for _, path := range []string{"/stacks/demo", "/stacks/demo/status"} {
				w := request(a, "GET", path, "")
				var payload struct {
					Status   string                       `json:"status"`
					Services []map[string]json.RawMessage `json:"services"`
				}
				if 200 != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &payload) || 3 != len(payload.Services) ||
					"stopped" != payload.Status {
					t.Fatalf("runtime response: %d %s", w.Code, w.Body)
				}
				for i, want := range []string{"7", "0", ""} {
					if want != string(payload.Services[i]["exit_code"]) {
						t.Fatalf("container %d exit_code: %s", i, w.Body)
					}
				}
			}
			if 8 != calls {
				t.Fatalf("runtime calls: %d", calls)
			}
		})
	}
}

func TestRuntimeTiming(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	for _, name := range []string{"docker", "podman"} {
		t.Run(name, func(t *testing.T) {
			a, _ := fixture(t)
			seed(t, a, "demo", "compose.yaml")
			calls := 0
			run := func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				calls++
				if strings.Contains(strings.Join(cmd.Args, " "), "ps --all --format json") {
					return []byte(
						`[{"Name":"web","State":"running","CreatedAt":"stale Compose date","RunningFor":"38 seconds ago"}]`,
					), nil, nil
				}
				if name != cmd.Program || 5 != len(cmd.Args) || compose.ContainerInfoFormat != cmd.Args[3] || "web" != cmd.Args[4] {
					t.Fatalf("unexpected inspection: %+v", cmd)
				}
				return []byte(
					`{"labels":null,"created_at":"2026-11-03T02:39:58.237+02:00","started_at":"2026-11-03T05:45:00.123456789+02:00"}`,
				), nil, nil
			}
			a.engine = docker.New(run)
			if "podman" == name {
				a.engine = podman.New(run)
			}
			for _, path := range []string{"/stacks/demo", "/stacks/demo/status"} {
				w := request(a, "GET", path, "")
				var payload struct {
					Services []engine.Service `json:"services"`
				}
				if 200 != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &payload) || 1 != len(payload.Services) {
					t.Fatalf("runtime response: %d %s", w.Code, w.Body)
				}
				if "2026-11-03T00:39:58.237Z" != payload.Services[0].CreatedAt ||
					"2026-11-03T03:45:00.123456789Z" != payload.Services[0].StartedAt {
					t.Fatalf("runtime timing: %s", w.Body)
				}
			}
			if 4 != calls {
				t.Fatalf("runtime calls: %d", calls)
			}
		})
	}
}
