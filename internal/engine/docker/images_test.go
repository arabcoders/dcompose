package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

var testPlatform = engine.ImagePlatform{OS: "linux", Architecture: "amd64"}

func digest(letter string) string { return "sha256:" + strings.Repeat(letter, 64) }

func TestImageChanged(t *testing.T) {
	for _, test := range []struct {
		name, container, local, fail string
		changed, failed              bool
	}{
		{name: "current", container: `{"id":"sha256:old","ref":"nginx:latest"}`, local: "sha256:old\n"},
		{name: "changed", container: `{"id":"sha256:old","ref":"nginx:latest"}`, local: "sha256:new\n", changed: true},
		{name: "container_error", fail: "container", failed: true},
		{name: "image_error", container: `{"id":"sha256:old","ref":"nginx:latest"}`, fail: "image", failed: true},
		{name: "invalid", container: "invalid", failed: true},
		{name: "missing", container: `{}`, failed: true},
		{name: "empty", container: `{"id":"sha256:old","ref":"nginx:latest"}`, failed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if "docker" != cmd.Program {
					t.Fatalf("runtime: %+v", cmd)
				}
				if test.fail == cmd.Args[0] {
					return nil, []byte("inspect failed"), errors.New("inspect failed")
				}
				if "container" == cmd.Args[0] {
					if !reflect.DeepEqual(cmd.Args, []string{"container", "inspect", "--format", `{"id":{{json .Image}},"ref":{{json .Config.Image}}}`, "web"}) {
						t.Fatalf("container: %+v", cmd)
					}
					return []byte(test.container), nil, nil
				}
				if !reflect.DeepEqual(cmd.Args, []string{"image", "inspect", "--format", "{{.Id}}", "nginx:latest"}) {
					t.Fatalf("image: %+v", cmd)
				}
				return []byte(test.local), nil, nil
			})
			changed, err := d.ImageChanged(context.Background(), engine.Service{Name: "web", Image: "sha256:old"})
			if test.changed != changed || test.failed != (nil != err) {
				t.Fatalf("changed=%t, err=%v", changed, err)
			}
		})
	}
}

func TestImageUnsupported(t *testing.T) {
	container := readFixture(t, "container-image.json")
	for _, test := range []struct{ name, ref, metadata, reason string }{
		{"pinned", "nginx@" + digest("a"), "", "pinned_digest"},
		{"id", digest("a"), "", "local_image"},
		{"built", "local:latest", `{"os":"linux","architecture":"amd64","digests":[]}`, "local_image"},
		{"platform", "nginx", `{"digests":["nginx@digest"]}`, "platform_unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if "container" == cmd.Args[0] {
					return []byte(strings.Replace(container, `"nginx:latest"`, fmt.Sprintf("%q", test.ref), 1)), nil, nil
				}
				if "image" == cmd.Args[0] {
					if digest("a") != cmd.Args[len(cmd.Args)-1] {
						t.Fatal("inspected mutable tag, not container image ID")
					}
					return []byte(test.metadata), nil, nil
				}
				t.Error("unexpected registry command")
				return nil, nil, errors.New("unexpected registry")
			})
			result := d.InspectImage(context.Background(), engine.Service{Name: "web"})
			if "unsupported" != result.Status || test.reason != result.Reason {
				t.Fatalf("result: %+v", result)
			}
		})
	}
}

func TestImageInspection(t *testing.T) {
	container := readFixture(t, "container-image.json")
	image := readFixture(t, "image-inspect.json")
	d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		switch cmd.Args[0] {
		case "container":
			if "web" != cmd.Args[len(cmd.Args)-1] {
				t.Fatalf("container: %+v", cmd)
			}
			return []byte(container), nil, nil
		case "image":
			if digest("a") != cmd.Args[len(cmd.Args)-1] {
				t.Fatal("inspected mutable tag, not container image ID")
			}
			return []byte(image), nil, nil
		}
		t.Fatalf("unexpected command: %+v", cmd)
		return nil, nil, nil
	})
	result := d.InspectImage(context.Background(), engine.Service{Name: "web"})
	if digest("a") != result.LocalID || "docker.io/library/nginx:latest" != result.Key.Ref || testPlatform != result.Key.ImagePlatform || "" != result.Status {
		t.Fatalf("target: %+v", result)
	}
}

func TestImageRefs(t *testing.T) {
	canonicalImageRef := New(nil).ImageRef
	for _, ref := range []string{"nginx", "nginx:latest", "library/nginx", "docker.io/nginx", "docker.io/library/nginx", "index.docker.io/library/nginx:latest"} {
		if "docker.io/library/nginx:latest" != canonicalImageRef(ref) {
			t.Fatalf("not canonical: %s -> %s", ref, canonicalImageRef(ref))
		}
	}
	for _, ref := range []string{"private.example/team/app:v1", "localhost:5000/app:edge"} {
		if ref != canonicalImageRef(ref) {
			t.Fatalf("private ref changed: %s", ref)
		}
	}
}

func TestImageDiagnostics(t *testing.T) {
	container := readFixture(t, "container-image.json")
	image := readFixture(t, "image-inspect.json")
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
		{"image_json", "image", `{"Config":{"Env":["secret-inspect-stdout"]},`, false, false},
		{"missing_id", "container", `{"Config":{"Env":["secret-inspect-stdout"]}}`, false, false},
		{"canceled", "image", "secret-inspect-stdout", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
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
					return []byte(container), nil, nil
				}
				return []byte(image), nil, nil
			})
			got := d.InspectImage(ctx, engine.Service{Name: "web", Image: "nginx:latest"})
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
			if "WARN" != record.Level || "docker" != record.Runtime || "web" != record.Container || "nginx:latest" != record.Image || "" == record.Error {
				t.Fatalf("missing diagnostics: %s", &logs)
			}
			if test.commandError && ("sentinel command failure" != record.Error || "sentinel command stderr" != record.Stderr || "docker "+test.fail+" inspect" != record.Command) {
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
