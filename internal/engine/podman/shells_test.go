package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

func TestShellInspectionMessage(t *testing.T) {
	for _, stderr := range []string{"", "permission denied for Podman socket"} {
		t.Run(stderr, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			p := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
				return []byte("private-container-environment"), []byte(stderr), errors.New("exit status 1")
			})
			_, err := p.ResolveContainer(context.Background(), testStack(), "demo-web")
			var apiErr *engine.Error
			if !errors.As(err, &apiErr) || 404 != apiErr.Code || strings.Contains(apiErr.Message, "permission denied") {
				t.Fatalf("public error changed: %v", err)
			}
			var record struct {
				Level, Runtime, Stack, Container, Error, Stderr string
				Message                                         string `json:"msg"`
			}
			if err := json.Unmarshal(logs.Bytes(), &record); nil != err {
				t.Fatal(err)
			}
			if "ERROR" != record.Level || "podman" != record.Runtime || "demo" != record.Stack || "demo-web" != record.Container || "exit status 1" != record.Error || stderr != record.Stderr {
				t.Fatalf("inspection fields: %+v", record)
			}
			for _, detail := range []string{record.Stack, record.Container, record.Error, stderr} {
				if "" != detail && !strings.Contains(record.Message, detail) {
					t.Fatalf("inspection message hides %q: %s", detail, record.Message)
				}
			}
			if strings.Contains(logs.String(), "private-container-environment") {
				t.Fatal("container environment leaked")
			}
		})
	}
}

func TestShell(t *testing.T) {
	fixture := readFixture(t, "container-inspect.json")
	id := strings.Repeat("a", 64)
	p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		if "podman" != cmd.Program || testStack().Dir != cmd.Dir || !reflect.DeepEqual([]string{"inspect", "--type", "container", "--", "demo-web"}, cmd.Args) {
			t.Fatalf("inspect: %+v", cmd)
		}
		return []byte(fixture), nil, nil
	})
	container, err := p.ResolveContainer(context.Background(), testStack(), "demo-web")
	if nil != err || id != container {
		t.Fatalf("container: %s %v", container, err)
	}
	cmd := p.Shell(container, "bash", "session")
	want := []string{"exec", "-it", "--detach-keys=", "--env", "DCOMPOSE_SHELL_ID=session", "--env", "TERM=xterm-256color", id, "sh", "-c", shellBootstrap, "dcompose-shell", "bash"}
	if "podman" != cmd.Program || !reflect.DeepEqual(want, cmd.Args) || !strings.Contains(shellBootstrap, "$DCOMPOSE_SHELL_ID") {
		t.Fatalf("shell: %+v", cmd)
	}
}

func TestResolveErrors(t *testing.T) {
	fixture := readFixture(t, "container-inspect.json")
	for _, test := range []struct {
		name, reference, data string
		fail                  bool
		code                  int
	}{
		{"flag", "--all", "", false, 400},
		{"path", "../container", "", false, 400},
		{"inspect", "web", "secret-output", true, 404},
		{"invalid", "web", "secret-output", false, 502},
		{"empty", "web", "[]", false, 502},
		{"short ID", "web", strings.Replace(fixture, strings.Repeat("a", 64), "aaaa", 1), false, 502},
		{"foreign stack", "web", strings.Replace(fixture, `"com.docker.compose.project":"demo"`, `"com.docker.compose.project":"other"`, 1), false, 403},
		{"podman compose label", "web", strings.Replace(fixture, "com.docker.compose.project", "io.podman.compose.project", 1), false, 403},
		{"stopped", "web", strings.Replace(fixture, `"Running":true`, `"Running":false`, 1), false, 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := New(func(_ context.Context, _ engine.Command) ([]byte, []byte, error) {
				if 400 == test.code {
					t.Fatal("invalid reference reached inspect")
				}
				if test.fail {
					return []byte(test.data), nil, errors.New("not found")
				}
				return []byte(test.data), nil, nil
			})
			_, err := p.ResolveContainer(context.Background(), testStack(), test.reference)
			var apiErr *engine.Error
			if !errors.As(err, &apiErr) || test.code != apiErr.Code {
				t.Fatalf("error: %v, want %d", err, test.code)
			}
		})
	}
}

func TestCleanup(t *testing.T) {
	failure := errors.New("cleanup failed")
	for _, test := range []struct {
		name, running string
		cleanupError  bool
		probeError    bool
		wantError     bool
	}{
		{"cleaned", "", false, false, false},
		{"removed", "", true, false, false},
		{"running", "container", true, false, true},
		{"engine unavailable", "", true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				calls++
				if "podman" != cmd.Program {
					t.Fatalf("cleanup: %+v", cmd)
				}
				if 1 == calls {
					want := []string{"exec", "--user", "0", "container", "sh", "-c", shellCleanup, "dcompose-cleanup", "session"}
					if !reflect.DeepEqual(want, cmd.Args) || !strings.Contains(shellCleanup, "DCOMPOSE_SHELL_ID=$1") {
						t.Fatalf("cleanup: %+v", cmd)
					}
					if test.cleanupError {
						return []byte("secret-output"), []byte("secret-diagnostics"), failure
					}
					return nil, nil, nil
				}
				if !reflect.DeepEqual([]string{"ps", "--quiet", "--no-trunc", "--filter", "id=container"}, cmd.Args) {
					t.Fatalf("probe: %+v", cmd)
				}
				if test.probeError {
					return nil, nil, errors.New("unavailable")
				}
				return []byte(test.running), nil, nil
			})
			err := p.CleanupShell(context.Background(), "container", "session")
			if test.wantError != errors.Is(err, failure) || (!test.cleanupError && 1 != calls) || (test.cleanupError && 2 != calls) {
				t.Fatalf("cleanup: %v, calls: %d", err, calls)
			}
		})
	}
}
