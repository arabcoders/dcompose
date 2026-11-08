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

func TestImageChanged(t *testing.T) {
	for _, test := range []struct {
		name, container, local, fail string
		changed, failed              bool
	}{
		{name: "current", container: `{"id":"old","ref":"quay.io/app:latest"}`, local: "old\n"},
		{name: "prefix", container: `{"id":"old","ref":"quay.io/app:latest"}`, local: "sha256:old\n"},
		{name: "changed", container: `{"id":"old","ref":"quay.io/app:latest"}`, local: "new\n", changed: true},
		{name: "container_error", fail: "container", failed: true},
		{name: "image_error", container: `{"id":"old","ref":"quay.io/app:latest"}`, fail: "image", failed: true},
		{name: "invalid", container: "invalid", failed: true},
		{name: "missing", container: `{}`, failed: true},
		{name: "empty", container: `{"id":"old","ref":"quay.io/app:latest"}`, failed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if "podman" != cmd.Program || 0 != len(cmd.Env) {
					t.Fatalf("runtime: %+v", cmd)
				}
				if test.fail == cmd.Args[0] {
					return nil, []byte("inspect failed"), errors.New("inspect failed")
				}
				if "container" == cmd.Args[0] {
					if !reflect.DeepEqual(cmd.Args, []string{"container", "inspect", "--format", `{"id":{{json .Image}},"ref":{{json .ImageName}}}`, "web"}) {
						t.Fatalf("container: %+v", cmd)
					}
					return []byte(test.container), nil, nil
				}
				if !reflect.DeepEqual(cmd.Args, []string{"image", "inspect", "--format", "{{.Id}}", "quay.io/app:latest"}) {
					t.Fatalf("image: %+v", cmd)
				}
				return []byte(test.local), nil, nil
			})
			changed, err := p.ImageChanged(context.Background(), engine.Service{Name: "web", Image: "sha256:old"})
			if test.changed != changed || test.failed != (nil != err) {
				t.Fatalf("changed=%t, err=%v", changed, err)
			}
		})
	}
}

func TestInspectImage(t *testing.T) {
	fixture := readFixture(t, "image-inspect.json")
	ambiguous := strings.Replace(fixture, `"RepoDigests":`, `"RepoTags":["quay.io/team/app:latest","example.com/app:latest"],"RepoDigests":`, 1)
	ambiguous = strings.Replace(ambiguous, "quay.io/team/app@sha256:123", "example.com/app@sha256:123", 1)
	id := strings.Repeat("a", 64)
	for _, test := range []struct {
		name, ref, image, status, reason, resolved string
	}{
		{"resolved", "quay.io/team/app:edge", fixture, "", "", "quay.io/team/app:edge"},
		{"short", "app", strings.Replace(fixture, `"RepoDigests":`, `"RepoTags":["quay.io/team/app:latest"],"RepoDigests":`, 1), "", "", "quay.io/team/app:latest"},
		{"ambiguous name", "app", ambiguous, "unsupported", "local_image", ""},
		{"pinned", "quay.io/team/app@sha256:" + id, "", "unsupported", "pinned_digest", ""},
		{"id", id, "", "unsupported", "local_image", ""},
		{"local", "localhost/built:latest", strings.Replace(fixture, `"quay.io/team/app@sha256:123"`, "", 1), "unsupported", "local_image", ""},
		{"unknown", "quay.io/team/app:edge", strings.Replace(fixture, `"Os":"linux","Architecture":"amd64",`, "", 1), "unsupported", "platform_unknown", ""},
		{"arm unknown", "quay.io/team/app:edge", strings.Replace(fixture, `"Architecture":"amd64"`, `"Architecture":"arm"`, 1), "unsupported", "platform_unknown", ""},
		{"arm variant", "quay.io/team/app:edge", strings.Replace(fixture, `"Architecture":"amd64"`, `"Architecture":"arm","Variant":"v7"`, 1), "", "", "quay.io/team/app:edge"},
		{"arm64 baseline", "quay.io/team/app:edge", strings.Replace(fixture, `"Architecture":"amd64"`, `"Architecture":"arm64"`, 1), "", "", "quay.io/team/app:edge"},
		{"windows unknown", "quay.io/team/app:edge", strings.Replace(fixture, `"Os":"linux"`, `"Os":"windows"`, 1), "unsupported", "platform_unknown", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				calls++
				if "podman" != cmd.Program || "" != cmd.Dir || 0 != len(cmd.Env) {
					t.Fatalf("runtime credentials changed: %+v", cmd)
				}
				if 1 == calls {
					if !reflect.DeepEqual(cmd.Args, []string{"container", "inspect", "--format", `{"id":{{json .Image}},"ref":{{json .ImageName}}}`, "demo"}) {
						t.Fatalf("container command: %+v", cmd)
					}
					data, err := json.Marshal(map[string]string{"id": id, "ref": test.ref})
					return data, nil, err
				}
				if !reflect.DeepEqual(cmd.Args, []string{"image", "inspect", id}) {
					t.Fatalf("image command: %+v", cmd)
				}
				return []byte(test.image), nil, nil
			})
			got := p.InspectImage(context.Background(), engine.Service{Name: "demo"})
			if got.LocalID != "sha256:"+id || got.Status != test.status || got.Reason != test.reason || got.Key.Ref != test.resolved {
				t.Fatalf("target: %+v", got)
			}
		})
	}
}

func TestInspectImageError(t *testing.T) {
	for _, data := range []string{"bad", `{}`, `{"id":"not-a-digest","ref":"quay.io/app"}`} {
		p := New(func(context.Context, engine.Command) ([]byte, []byte, error) { return []byte(data), nil, nil })
		if got := p.InspectImage(context.Background(), engine.Service{Name: "demo"}); "inspect_error" != got.Reason {
			t.Fatalf("target: %+v", got)
		}
	}
	p := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
		return nil, nil, errors.New("inspect failed")
	})
	if got := p.InspectImage(context.Background(), engine.Service{Name: "demo"}); "inspect_error" != got.Reason {
		t.Fatalf("target: %+v", got)
	}
}

func TestImageRef(t *testing.T) {
	p := New(nil)
	for input, want := range map[string]string{
		"": "", "nginx": "nginx:latest", "team/app": "team/app:latest",
		"quay.io/team/app": "quay.io/team/app:latest", "localhost:5000/app:edge": "localhost:5000/app:edge",
		"index.docker.io/nginx": "docker.io/library/nginx:latest", "docker.io/nginx": "docker.io/library/nginx:latest",
		"docker.io/library/nginx": "docker.io/library/nginx:latest", "quay.io/app@sha256:abcd": "quay.io/app@sha256:abcd",
	} {
		if got := p.ImageRef(input); want != got {
			t.Errorf("%q: got %q, want %q", input, got, want)
		}
	}
}

func TestImageDiagnostics(t *testing.T) {
	image := readFixture(t, "image-inspect.json")
	id := strings.Repeat("a", 64)
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	for _, test := range []struct {
		name, fail, output     string
		commandError, canceled bool
	}{
		{"container_command", "container", "secret-inspect-stdout", true, false},
		{"image_command", "image", "secret-inspect-stdout", true, false},
		{"container_json", "container", `{"id":"secret-inspect-stdout",`, false, false},
		{"image_json", "image", `[{"Config":{"Env":["secret-inspect-stdout"]},`, false, false},
		{"missing_id", "container", `{"Config":{"Env":["secret-inspect-stdout"]}}`, false, false},
		{"image_count", "image", `[]`, false, false},
		{"canceled", "image", "secret-inspect-stdout", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if test.fail == cmd.Args[0] {
					if test.commandError {
						if test.canceled {
							cancel()
						}
						return []byte(test.output), []byte("sentinel command stderr"), errors.New("sentinel command failure")
					}
					return []byte(test.output), nil, nil
				}
				if "container" == cmd.Args[0] {
					return []byte(`{"id":"` + id + `","ref":"quay.io/team/app:edge"}`), nil, nil
				}
				return []byte(image), nil, nil
			})
			got := p.InspectImage(ctx, engine.Service{Name: "demo", Image: "quay.io/team/app:edge"})
			if "error" != got.Status || "inspect_error" != got.Reason {
				t.Fatalf("target: %+v", got)
			}
			if test.canceled {
				if 0 != logs.Len() {
					t.Fatalf("shutdown logged a failure: %s", &logs)
				}
				return
			}
			var record struct {
				Level, Runtime, Container, Image, Command, Error, Stderr string
				Message                                                  string `json:"msg"`
			}
			if err := json.Unmarshal(logs.Bytes(), &record); nil != err {
				t.Fatalf("decode logs: %v", err)
			}
			if "WARN" != record.Level || "podman" != record.Runtime || "demo" != record.Container || "quay.io/team/app:edge" != record.Image || "" == record.Error {
				t.Fatalf("missing diagnostics: %s", &logs)
			}
			if test.commandError && ("sentinel command failure" != record.Error || "sentinel command stderr" != record.Stderr || "podman "+test.fail+" inspect" != record.Command) {
				t.Fatalf("missing command details: %s", &logs)
			}
			details := []string{record.Container, record.Image, record.Stderr}
			if test.commandError {
				details = append(details, "sentinel command failure")
			}
			for _, detail := range details {
				if "" != detail && !strings.Contains(record.Message, detail) {
					t.Fatalf("message hides diagnostic %q: %s", detail, record.Message)
				}
			}
			encoded, err := json.Marshal(got)
			if nil != err || strings.Contains(string(encoded), "sentinel") || strings.Contains(logs.String(), "secret-inspect-stdout") {
				t.Fatalf("private details leaked: target=%s logs=%s error=%v", encoded, &logs, err)
			}
		})
	}
}
