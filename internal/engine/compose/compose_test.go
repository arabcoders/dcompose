package compose

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

func testStack() engine.Stack {
	return engine.Stack{Name: "demo", Dir: "/stacks/a b", ComposeFile: "/stacks/a b/compose.yml", EnvFile: "/stacks/a b/.env"}
}

func TestCommands(t *testing.T) {
	c := New(nil, nil)
	s := testStack()
	base := []string{"compose", "--project-name", s.Name, "--project-directory", s.Dir, "--env-file", s.EnvFile, "--file", s.ComposeFile}
	for _, test := range []struct {
		action, service string
		args            []string
	}{
		{"deploy", "", []string{"up", "-d"}},
		{"start", "web", []string{"up", "-d", "--", "web"}},
		{"stop", "web", []string{"stop", "--", "web"}},
		{"restart", "web", []string{"restart", "--", "web"}},
		{"pull", "", []string{"pull"}},
		{"down", "", []string{"down"}},
	} {
		t.Run(test.action, func(t *testing.T) {
			cmd := c.Action(s, test.action, test.service)
			want := append(append([]string{}, base...), test.args...)
			if "docker" != cmd.Program || s.Dir != cmd.Dir || !reflect.DeepEqual(want, cmd.Args) ||
				!reflect.DeepEqual([]string{"COMPOSE_ANSI=always", "COMPOSE_PROGRESS=tty"}, cmd.Env) {
				t.Fatalf("command: %+v", cmd)
			}
		})
	}
	if cmd := c.Logs(s); !reflect.DeepEqual(append(base, "logs", "--follow", "--tail", "100", "--no-color"), cmd.Args) {
		t.Fatalf("logs: %+v", cmd)
	}
}

func TestValidation(t *testing.T) {
	s := testStack()
	env := []string{"DOCKER_HOST=unix:///run/user/1000/podman/podman.sock", "DOCKER_CONTEXT="}
	called := false
	cause := errors.New("exit 1")
	stdout := "rendered secret-token\n"
	stderr := "invalid compose /tmp/private/compose.yml"
	c := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		called = true
		want := New(nil, env).command(s, "config", "--quiet", "--no-env-resolution", "--services")
		if !reflect.DeepEqual(want, cmd) {
			t.Fatalf("validation lost project paths or runtime environment: %+v", cmd)
		}
		return []byte(stdout), []byte(stderr), cause
	}, env)
	err := c.Validate(context.Background(), s)
	var apiErr *engine.Error
	if !called || !errors.As(err, &apiErr) || 400 != apiErr.Code {
		t.Fatalf("validation error: %v", err)
	}
	if "" == apiErr.Message || !errors.Is(err, cause) {
		t.Fatalf("public validation error or cause: %+v", apiErr)
	}
	for _, diagnostic := range []string{"secret-token", "/tmp/private/compose.yml", stdout, stderr} {
		if strings.Contains(apiErr.Message, diagnostic) || !strings.Contains(err.Error(), diagnostic) {
			t.Fatalf("validation diagnostic separation: %v", err)
		}
	}
	if cmd := c.Action(
		s,
		"start",
		"",
	); !reflect.DeepEqual(
		append(append([]string{}, env...), "COMPOSE_ANSI=always", "COMPOSE_PROGRESS=tty"),
		cmd.Env,
	) {
		t.Fatalf("action lost runtime environment: %+v", cmd)
	}
	if cmd := c.Logs(s); !reflect.DeepEqual(env, cmd.Env) {
		t.Fatalf("logs lost runtime environment: %+v", cmd)
	}
}

func TestCommandErrors(t *testing.T) {
	cause := errors.New("exit 1: /tmp/private/docker")
	stderr := "secret-token from /tmp/private/compose.yml"
	c := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
		return nil, []byte(stderr), cause
	}, nil)
	for _, test := range []struct {
		name string
		call func() error
	}{
		{"services", func() error {
			_, err := c.Services(context.Background(), testStack())
			return err
		}},
		{"containers", func() error {
			_, err := c.Containers(context.Background(), testStack())
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			var apiErr *engine.Error
			if !errors.As(err, &apiErr) || 502 != apiErr.Code || "" == apiErr.Message || !errors.Is(err, cause) {
				t.Fatalf("command error: %v", err)
			}
			for _, diagnostic := range []string{"secret-token", "/tmp/private", cause.Error(), stderr} {
				if strings.Contains(apiErr.Message, diagnostic) || !strings.Contains(err.Error(), diagnostic) {
					t.Fatalf("command diagnostic separation: %v", err)
				}
			}
		})
	}
}

func TestContainerOutputErrors(t *testing.T) {
	for _, data := range []string{"[invalid secret-token /tmp/private]", "invalid secret-token /tmp/private"} {
		c := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
			return []byte(data), nil, nil
		}, nil)
		_, err := c.Containers(context.Background(), testStack())
		var apiErr *engine.Error
		var cause *json.SyntaxError
		if !errors.As(err, &apiErr) || 502 != apiErr.Code || "" == apiErr.Message || !errors.As(err, &cause) {
			t.Fatalf("parse error: %v", err)
		}
		if strings.Contains(apiErr.Message, "secret-token") || strings.Contains(apiErr.Message, "/tmp/private") ||
			!strings.Contains(err.Error(), cause.Error()) {
			t.Fatalf("parse diagnostic separation: %v", err)
		}
	}
}

func TestStaticError(t *testing.T) {
	err := &engine.Error{Code: 400, Message: "Unknown service."}
	if err.Message != err.Error() || nil != errors.Unwrap(err) {
		t.Fatalf("static error changed: %v", err)
	}
}

func TestRuntime(t *testing.T) {
	container := readFixture(t, "container.json")
	for _, data := range []string{
		"[" + container + "]",
		container,
	} {
		c := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
			if !reflect.DeepEqual(New(nil, nil).command(testStack(), "ps", "--all", "--format", "json").Args, cmd.Args) {
				t.Fatalf("ps: %+v", cmd)
			}
			return []byte(data), []byte("warning must stay out of JSON"), nil
		}, nil)
		services, err := c.Containers(context.Background(), testStack())
		if nil != err || 1 != len(services) || "one" != services[0].ID || ":::8080->80/tcp" != services[0].Ports ||
			8080 != services[0].PortBindings[0].HostPort {
			t.Fatalf("runtime: %+v %v", services, err)
		}
		if "Up 2 hours (healthy)" != services[0].Status || "2026-10-25 09:00:00 +0000 UTC" != services[0].CreatedAt ||
			"2 days ago" != services[0].RunningFor {
			t.Fatalf("timing: %+v", services[0])
		}
	}
}

func TestContainerTiming(t *testing.T) {
	for _, test := range []struct{ state, status string }{
		{"running", "Up 2 hours (healthy)"},
		{"exited", "Exited (0) 3 minutes ago"},
		{"restarting", "Restarting (1) 10 seconds ago"},
	} {
		t.Run(test.state, func(t *testing.T) {
			data, _ := json.Marshal(map[string]string{
				"State": test.state, "Status": test.status,
				"CreatedAt": "2026-10-25 09:00:00 +0000 UTC", "RunningFor": "2 days ago",
			})
			c := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
				return data, nil, nil
			}, nil)
			services, err := c.Containers(context.Background(), testStack())
			if nil != err || 1 != len(services) {
				t.Fatalf("runtime: %+v %v", services, err)
			}
			encoded, err := json.Marshal(services[0])
			if nil != err {
				t.Fatal(err)
			}
			var payload struct {
				Status     string `json:"status"`
				RunningFor string `json:"running_for"`
				CreatedAt  string `json:"created_at"`
			}
			if err := json.Unmarshal(encoded, &payload); nil != err {
				t.Fatal(err)
			}
			if test.status != payload.Status || "2 days ago" != payload.RunningFor || "2026-10-25 09:00:00 +0000 UTC" != payload.CreatedAt {
				t.Fatalf("API timing: %s", encoded)
			}
		})
	}
}

func TestContainerExitCode(t *testing.T) {
	for _, test := range []struct{ name, field, want string }{
		{"nonzero", `,"ExitCode":7`, "7"},
		{"zero", `,"ExitCode":0`, "0"},
		{"absent", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := `{"State":"exited","Health":"unhealthy","Status":"Exited (1)"` + test.field + `}`
			for _, data := range []string{item, "[" + item + "]"} {
				c := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
					return []byte(data), nil, nil
				}, nil)
				services, err := c.Containers(context.Background(), testStack())
				if nil != err || 1 != len(services) {
					t.Fatalf("containers: %+v %v", services, err)
				}
				if "exited" != services[0].State || "unhealthy" != services[0].Health || nil == services[0].PortBindings {
					t.Fatalf("runtime fields: %+v", services[0])
				}
				encoded, err := json.Marshal(services[0])
				if nil != err {
					t.Fatal(err)
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(encoded, &payload); nil != err {
					t.Fatal(err)
				}
				if test.want != string(payload["exit_code"]) {
					t.Fatalf("exit_code: %s", encoded)
				}
			}
		})
	}
}
