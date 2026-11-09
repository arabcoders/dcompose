package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

func TestOperationTTY(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			a, fake := fixture(t)
			dir := seed(t, a, "demo", "compose.yaml")
			write(t, filepath.Join(fake, "docker"), `#!/bin/sh
[ -t 1 ] && [ -t 2 ] && [ ! -t 0 ] || exit 90
[ "$TERM" = xterm-256color ] && [ "$COMPOSE_ANSI" = always ] && [ "$COMPOSE_PROGRESS" = tty ] || exit 91
printf '\033[2K\rpulling...\033[1A\033[2K\rpulled\n'
printf 'stderr\n' >&2
exit `+strconv.Itoa(code)+"\n")
			base, err := a.engineStack("demo", dir)
			if nil != err {
				t.Fatal(err)
			}
			op := &operation{Stack: "demo", Action: "pull"}
			err = a.runOperation(op, dir, base, "")
			if code != exitCode(err) {
				t.Fatalf("exit: %v", err)
			}
			if "\x1b[2K\rpulling...\x1b[1A\x1b[2K\rpulled\r\nstderr\r\n" != op.snapshot().Output {
				t.Fatalf("terminal output: %q", op.snapshot().Output)
			}
		})
	}
}

func TestPortBindings(t *testing.T) {
	for _, test := range []struct {
		name, output, ports string
		bindings            []engine.PortBinding
	}{
		{
			"publishers",
			`[{"Ports":"original display text","Publishers":[{"URL":"0.0.0.0","TargetPort":80,"PublishedPort":8080,"Protocol":"tcp"},{"URL":"::","TargetPort":80,"PublishedPort":8080,"Protocol":"tcp"},{"URL":"192.0.2.1","TargetPort":53,"PublishedPort":5353,"Protocol":"udp"},{"URL":"","TargetPort":443,"PublishedPort":0,"Protocol":"tcp"}]}]`,
			"original display text",
			[]engine.PortBinding{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, {HostIP: "::", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, {HostIP: "192.0.2.1", HostPort: 5353, ContainerPort: 53, Protocol: "udp"}, {ContainerPort: 443, Protocol: "tcp"}},
		},
		{
			"fallback",
			`{"Publishers":[{"URL":"127.0.0.1","TargetPort":80,"PublishedPort":8080,"Protocol":"tcp"},{"TargetPort":443,"Protocol":"tcp"}]}`,
			"127.0.0.1:8080->80/tcp, 443/tcp",
			[]engine.PortBinding{{HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, {ContainerPort: 443, Protocol: "tcp"}},
		},
		{"missing", `[{}]`, "", []engine.PortBinding{}},
		{"null", `[{"Publishers":null}]`, "", []engine.PortBinding{}},
		{"display-only", `[{"Ports":"0.0.0.0:8080->80/tcp","Publishers":[]}]`, "0.0.0.0:8080->80/tcp", []engine.PortBinding{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, fake := fixture(t)
			seed(t, a, "demo", "compose.yaml")
			write(t, filepath.Join(fake, "ps"), test.output)
			w := request(a, "GET", "/stacks/demo", "")
			if 200 != w.Code {
				t.Fatalf("detail: %d %s", w.Code, w.Body)
			}
			var result stack
			if err := json.Unmarshal(w.Body.Bytes(), &result); nil != err {
				t.Fatal(err)
			}
			if 1 != len(result.Services) || test.ports != result.Services[0].Ports ||
				!reflect.DeepEqual(test.bindings, result.Services[0].PortBindings) {
				t.Fatalf("unexpected services: %+v", result.Services)
			}
		})
	}
}

// Config-only integration: no images, containers, or Docker daemon are needed.
func TestComposeEnv(t *testing.T) {
	if _, err := exec.LookPath("docker"); nil != err {
		t.Skip("Docker CLI unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a := newAgent(ctx, config{Global: globalConfig{StacksDir: t.TempDir()}})
	help, err := exec.CommandContext(ctx, "docker", "compose", "config", "--help").CombinedOutput()
	if nil != err || !strings.Contains(string(help), "--no-env-resolution") {
		t.Skip("Docker Compose 2.35+ unavailable")
	}
	compose := "# original comment\nservices:\n  web:\n    image: ${DOCKER_NODE_TEST_IMAGE:?image required}\n    env_file: .env\n    volumes:\n      - ./data:/data\n"
	for _, image := range []string{"busybox:latest", "busybox:stable"} {
		env := "DOCKER_NODE_TEST_IMAGE=" + image + "\nDOCKER_NODE_TEST_VALUE=updated\n"
		if err := a.save(ctx, "envtest", compose, env); nil != err {
			t.Fatal(err)
		}
		dir := filepath.Join(a.config.Global.StacksDir, "envtest")
		s, err := a.engineStack("envtest", dir)
		if nil != err {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(
			ctx,
			"docker",
			"compose",
			"--project-name",
			s.Name,
			"--project-directory",
			s.Dir,
			"--env-file",
			s.EnvFile,
			"--file",
			s.ComposeFile,
			"config",
			"--format",
			"json",
		)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), a.engine.Action(s, "update", "").Env...)
		output, err := cmd.CombinedOutput()
		if nil != err {
			t.Fatalf("compose config: %v: %s", err, output)
		}
		var parsed struct {
			Services map[string]struct {
				Image       string            `json:"image"`
				Environment map[string]string `json:"environment"`
				Volumes     []struct {
					Source string `json:"source"`
				} `json:"volumes"`
			} `json:"services"`
		}
		if err := json.Unmarshal([]byte(output), &parsed); nil != err {
			t.Fatal(err)
		}
		web := parsed.Services["web"]
		if image != web.Image || "updated" != web.Environment["DOCKER_NODE_TEST_VALUE"] || 1 != len(web.Volumes) ||
			filepath.Join(dir, "data") != web.Volumes[0].Source {
			t.Fatalf("wrong env/context: %+v", web)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "compose.yaml"))
		if compose != string(data) {
			t.Fatal("comments changed")
		}
	}
	if err := a.save(ctx, "envtest", compose, ""); nil == err {
		t.Fatal("missing interpolation variable accepted")
	}
	data, _ := os.ReadFile(filepath.Join(a.config.Global.StacksDir, "envtest", ".env"))
	if !strings.Contains(string(data), "busybox:stable") {
		t.Fatal("invalid env overwrote working file")
	}
}
