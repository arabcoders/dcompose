package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/engine/docker"
	"github.com/arabcoders/dcompose/internal/engine/podman"
	"github.com/arabcoders/dcompose/internal/protocol"
)

type inspectionDriver struct {
	engine.Driver
	containers func(context.Context, engine.Stack) ([]engine.Service, error)
	inspect    func(context.Context, engine.Service) engine.ImageTarget
}

func (d *inspectionDriver) Containers(ctx context.Context, stack engine.Stack) ([]engine.Service, error) {
	return d.containers(ctx, stack)
}

func (d *inspectionDriver) InspectImage(ctx context.Context, service engine.Service) engine.ImageTarget {
	return d.inspect(ctx, service)
}

func TestImageInspect(t *testing.T) {
	for _, container := range []string{"", "worker-1"} {
		t.Run(container, func(t *testing.T) {
			a, _ := fixture(t)
			dir := seed(t, a, "demo", "compose.yaml")
			services := []service{
				{ID: "web-id", Name: "web-1", Service: "web", Image: "nginx:latest", State: "running", CheckInterval: "30m", AutoUpdate: true},
				{ID: "worker-id", Name: "worker-1", Service: "worker", Image: "redis:latest", State: "exited"},
			}
			inspected := []string{}
			a.engine = &inspectionDriver{
				Driver: a.engine,
				containers: func(ctx context.Context, stack engine.Stack) ([]engine.Service, error) {
					if "demo" != stack.Name || dir != stack.Dir || filepath.Join(dir, "compose.yaml") != stack.ComposeFile || nil != ctx.Err() {
						t.Fatalf("stack inspection: %+v %v", stack, ctx.Err())
					}
					return services, nil
				},
				inspect: func(_ context.Context, service engine.Service) engine.ImageTarget {
					inspected = append(inspected, service.Name)
					return engine.ImageTarget{LocalID: "local-" + service.ID, Key: engine.ImageKey{Ref: service.Image, ImagePlatform: engine.ImagePlatform{OS: "linux", Architecture: "amd64"}}}
				},
			}
			w := request(a, "POST", "/images/inspect", fmt.Sprintf(`{"stack":"demo","container":%q}`, container))
			var response protocol.ImageInspectResponse
			if http.StatusOK != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &response) {
				t.Fatalf("inspection: %d %s", w.Code, w.Body)
			}
			expected := services
			if "" != container {
				expected = services[1:]
			}
			if len(expected) != len(response.Images) || len(expected) != len(inspected) {
				t.Fatalf("inspection selection: %+v %v", response.Images, inspected)
			}
			for i, service := range expected {
				image := response.Images[i]
				if service.ID != image.ID || service.Name != image.Service.Name || service.Image != image.Target.Key.Ref || "local-"+service.ID != image.Target.LocalID || service.CheckInterval != image.Service.CheckInterval || service.AutoUpdate != image.Service.AutoUpdate {
					t.Fatalf("inspection identity/target: %+v", image)
				}
			}
			if 0 != len(a.ops) {
				t.Fatal("inspection started an operation")
			}
		})
	}
}

func TestImageInspectValidation(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	if err := os.Mkdir(filepath.Join(a.config.Global.StacksDir, "draft"), 0750); nil != err {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(a.config.Global.StacksDir, "demo"), filepath.Join(a.config.Global.StacksDir, "linked")); nil != err {
		t.Fatal(err)
	}
	write(t, filepath.Join(fake, "ps"), `[{"ID":"web-id","Name":"web-1","Service":"web","Image":"nginx:latest","State":"running"}]`)
	for _, test := range []struct {
		body string
		code int
	}{
		{`{}`, 400},
		{`{"stack":"../demo"}`, 400},
		{`{"stack":"missing"}`, 404},
		{`{"stack":"draft"}`, 404},
		{`{"stack":"linked"}`, 400},
		{`{"stack":"demo","container":"another-stack-web-1"}`, 404},
		{`{"stack":"demo","container":"web"}`, 404},
		{`{"stack":"demo","image":"untrusted.example/image"}`, 400},
	} {
		w := request(a, "POST", "/images/inspect", test.body)
		if test.code != w.Code {
			t.Fatalf("%s: %d %s", test.body, w.Code, w.Body)
		}
	}
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	if strings.Contains(string(argv), "<image><inspect>") || strings.Contains(string(argv), `{"id":`) {
		t.Fatalf("invalid selection inspected an image: %s", argv)
	}
	write(t, filepath.Join(fake, "ps"), `[]`)
	if w := request(a, "POST", "/images/inspect", `{"stack":"demo"}`); 200 != w.Code || "{\"images\":[]}\n" != w.Body.String() {
		t.Fatalf("empty inspection batch: %d %s", w.Code, w.Body)
	}
}

func TestImageInspectResults(t *testing.T) {
	for _, target := range []engine.ImageTarget{
		{Status: "unsupported", Reason: "pinned_digest"},
		{Status: "unsupported", Reason: "local_image"},
		{Status: "error", Reason: "inspect_error"},
	} {
		t.Run(target.Reason, func(t *testing.T) {
			a, _ := fixture(t)
			seed(t, a, "demo", "compose.yaml")
			a.engine = &inspectionDriver{
				Driver: a.engine,
				containers: func(context.Context, engine.Stack) ([]service, error) {
					return []service{{ID: "id", Name: "web-1"}}, nil
				},
				inspect: func(context.Context, service) engine.ImageTarget { return target },
			}
			w := request(a, "POST", "/images/inspect", `{"stack":"demo"}`)
			var response protocol.ImageInspectResponse
			if 200 != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &response) || 1 != len(response.Images) || target != response.Images[0].Target {
				t.Fatalf("typed inspection result: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestImageInspectContext(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	inspected := false
	a.engine = &inspectionDriver{
		Driver: a.engine,
		containers: func(ctx context.Context, _ engine.Stack) ([]service, error) {
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("container discovery lost request cancellation")
			}
			return []service{{Name: "web-1"}}, nil
		},
		inspect: func(ctx context.Context, _ service) engine.ImageTarget {
			inspected = true
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("image inspection lost request cancellation")
			}
			return engine.ImageTarget{Status: "error", Reason: "inspect_error"}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("POST", "/images/inspect", strings.NewReader(`{"stack":"demo"}`)).WithContext(ctx)
	r.RemoteAddr = "127.0.0.1:42"
	r.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if !inspected || 200 != w.Code {
		t.Fatalf("canceled inspection: %d %s", w.Code, w.Body)
	}
}

func TestImageInspectContainerError(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	a.engine = &inspectionDriver{
		Driver: a.engine,
		containers: func(context.Context, engine.Stack) ([]service, error) {
			return nil, &engine.Error{Code: 502, Message: "container status is unavailable", Cause: errors.New("private container stderr")}
		},
		inspect: func(context.Context, service) engine.ImageTarget {
			t.Fatal("inspection continued after discovery failed")
			return engine.ImageTarget{}
		},
	}
	w := request(a, "POST", "/images/inspect", `{"stack":"demo"}`)
	if 502 != w.Code || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("container failure: %d %s", w.Code, w.Body)
	}
}

func TestImageInspectNative(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	for _, name := range []string{"docker", "podman"} {
		t.Run(name, func(t *testing.T) {
			a, _ := fixture(t)
			seed(t, a, "demo", "compose.yaml")
			id := "sha256:" + strings.Repeat("a", 64)
			calls := 0
			run := func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				args := strings.Join(cmd.Args, " ")
				switch {
				case strings.Contains(args, "ps --all --format json"):
					return []byte(`[{"ID":"container-id","Name":"demo-web-1","Service":"web","Image":"docker.io/library/nginx:latest","State":"running"}]`), nil, nil
				case strings.Contains(args, "{{json .Config.Labels}}"):
					return []byte(`{}`), nil, nil
				case strings.Contains(args, "container inspect"):
					calls++
					return []byte(fmt.Sprintf(`{"id":%q,"ref":"docker.io/library/nginx:latest"}`, id)), nil, nil
				case strings.Contains(args, "image inspect"):
					calls++
					if "podman" == name {
						return []byte(`[{"Os":"linux","Architecture":"amd64","RepoTags":["docker.io/library/nginx:latest"],"RepoDigests":["docker.io/library/nginx@sha256:manifest"]}]`), nil, nil
					}
					return []byte(`{"os":"linux","architecture":"amd64","digests":["docker.io/library/nginx@sha256:manifest"]}`), nil, nil
				default:
					t.Fatalf("unexpected local inspection command: %+v", cmd)
					return nil, nil, errors.New("unexpected command")
				}
			}
			a.engine = docker.New(run)
			if "podman" == name {
				a.engine = podman.New(run)
			}
			w := request(a, "POST", "/images/inspect", `{"stack":"demo"}`)
			var response protocol.ImageInspectResponse
			if 200 != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &response) || 1 != len(response.Images) || 2 != calls {
				t.Fatalf("native inspection: %d %s calls=%d", w.Code, w.Body, calls)
			}
			image := response.Images[0]
			if "container-id" != image.ID || id != image.Target.LocalID || "docker.io/library/nginx:latest" != image.Target.Key.Ref || "amd64" != image.Target.Key.Architecture || "" != image.Target.Status {
				t.Fatalf("native target: %+v", image)
			}
		})
	}
}

func TestImageCheckRoutesRemoved(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	for _, path := range []string{"/images/check", "/stacks/demo/images/check"} {
		if w := request(a, "POST", path, ""); 404 != w.Code {
			t.Fatalf("obsolete route %s: %d %s", path, w.Code, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatal("obsolete checking route invoked runtime")
	}
}

func TestImagesInvalidated(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	write(t, filepath.Join(fake, "images"), "redis:latest\n")
	a.events.stacks["demo"] = stackRuntime{Name: "demo", Services: []service{{Image: "nginx:latest"}}}
	subscriber := newSubscriber()
	a.events.subscribers[subscriber] = struct{}{}
	a.invalidateImages("demo", map[string]bool{"docker.io/library/alpine:latest": true, "nginx:latest": true})
	e := <-subscriber.events
	var invalidated protocol.ImageInvalidated
	if "images.invalidated" != e.name || nil != json.Unmarshal(e.data, &invalidated) || "demo" != invalidated.Stack || !reflect.DeepEqual(invalidated.Refs, []string{"docker.io/library/alpine:latest", "docker.io/library/nginx:latest", "docker.io/library/redis:latest"}) {
		t.Fatalf("invalidation refs: %s %s", e.name, e.data)
	}
	a.invalidateImages("missing", nil)
	e = <-subscriber.events
	if nil != json.Unmarshal(e.data, &invalidated) || "missing" != invalidated.Stack || 0 != len(invalidated.Refs) || strings.Contains(string(e.data), `"refs":null`) {
		t.Fatalf("unknown refs lost stack invalidation: %s", e.data)
	}
}

func TestImageOperationInvalidation(t *testing.T) {
	for _, action := range []string{"pull", "update", "deploy"} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", action, failed), func(t *testing.T) {
				a, fake := executionFixture(t)
				write(t, filepath.Join(fake, "images"), "nginx:latest\n")
				if failed {
					write(t, filepath.Join(fake, "fail"), "")
				}
				subscriber := newSubscriber()
				a.events.subscribers[subscriber] = struct{}{}
				op := completed(t, a, request(a, "POST", "/stacks/demo/actions", fmt.Sprintf(`{"action":%q}`, action)))
				a.work.Wait()
				if failed != ("failed" == op.Status) {
					t.Fatalf("operation outcome: %+v", op)
				}
				for i := 0; 2 > i; i++ {
					select {
					case e := <-subscriber.events:
						if "images.invalidated" != e.name {
							i--
							continue
						}
						var invalidated protocol.ImageInvalidated
						if "images.invalidated" != e.name || nil != json.Unmarshal(e.data, &invalidated) || "demo" != invalidated.Stack || !reflect.DeepEqual(invalidated.Refs, []string{"docker.io/library/nginx:latest"}) {
							t.Fatalf("operation invalidation: %s %s", e.name, e.data)
						}
					default:
						t.Fatal("operation omitted initial or final invalidation")
					}
				}
			})
		}
	}
}

func TestRuntimeExecutionOnly(t *testing.T) {
	a, fake := executionFixture(t)
	write(t, filepath.Join(fake, "container-labels-web-1"), `{"io.dcompose.check-interval":"30m","io.dcompose.auto-update":"true"}`)
	applyRegistryFixture(t, a)
	a.startBackground()
	eventually(t, func() bool {
		a.events.mu.Lock()
		defer a.events.mu.Unlock()
		services := a.events.stacks["demo"].Services
		return 1 == len(services) && "30m" == services[0].CheckInterval && services[0].AutoUpdate
	})
	// Standalone runtime observation, even with check/update labels and managed
	// background credentials, does not inspect registry metadata or start work.
	a.mu.Lock()
	operations := len(a.ops)
	a.mu.Unlock()
	if 0 != operations {
		t.Fatal("standalone runtime made an image decision")
	}
	server := channelServer(t, a)
	conn := openChannel(t, server)
	channelUntil(t, conn, "event", "stack.runtime")
	payload, _ := json.Marshal(registrySnapshot())
	channelSend(t, conn, protocol.ChannelMessage{Type: "registry.sync", Payload: payload})
	channelUntil(t, conn, "registry.applied", "")
	if w := request(a, "GET", "/stacks/demo/status", ""); 200 != w.Code || strings.Contains(w.Body.String(), "image_update") {
		t.Fatalf("runtime status: %d %s", w.Code, w.Body)
	}
	a.mu.Lock()
	operations = len(a.ops)
	a.mu.Unlock()
	if 0 != operations {
		t.Fatal("channel connect or registry sync made an image decision")
	}
	argv, err := os.ReadFile(filepath.Join(fake, "argv"))
	if nil != err || strings.Contains(string(argv), "<image><inspect>") || strings.Contains(string(argv), `{"id":`) || strings.Contains(string(argv), "<pull>") || strings.Contains(string(argv), "<up>") {
		t.Fatalf("runtime observation executed image inspection or an update: %s %v", argv, err)
	}
}

func TestImageConfigOperations(t *testing.T) {
	for _, action := range []string{"pull", "deploy"} {
		for _, synced := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", action, synced), func(t *testing.T) {
				isolatedRegistry(t)
				root := os.Getenv("DOCKER_CONFIG")
				defaults := `{"auths":{"private.example":{"auth":"default-secret"}},"credsStore":"default-store"}`
				write(t, filepath.Join(root, "config.json"), defaults)
				a, fake := executionFixture(t)
				if synced {
					applyRegistryFixture(t, a)
				}
				op := completed(t, a, request(a, "POST", "/stacks/demo/actions", fmt.Sprintf(`{"action":%q}`, action)))
				if "succeeded" != op.Status {
					t.Fatalf("action failed: %+v", op)
				}
				data, err := os.ReadFile(filepath.Join(fake, "pull-config"))
				if nil != err || a.pullConfigDir() != string(data) {
					t.Fatal("action did not use managed pull credentials")
				}
				config := readDockerConfig(t, filepath.Join(a.pullConfigDir(), "config.json"))
				if synced {
					if config.Auths["https://index.docker.io/v1/"]["auth"] != loginAuth("pull", "pull-secret")["auth"] {
						t.Fatal("action selected background login instead of pull login")
					}
				} else if 0 != len(config.Auths) {
					t.Fatal("unsynced action inherited host credentials")
				}
				data, err = os.ReadFile(filepath.Join(fake, "argv"))
				if nil != err || strings.Contains(string(data), "<manifest>") || strings.Contains(string(data), "<--config>") {
					t.Fatalf("unexpected metadata subprocess/config override: %s %v", data, err)
				}
				data, err = os.ReadFile(filepath.Join(root, "config.json"))
				if nil != err || defaults != string(data) || root != os.Getenv("DOCKER_CONFIG") {
					t.Fatal("action changed host credentials")
				}
			})
		}
	}
}
