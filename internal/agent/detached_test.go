package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/engine/podman"
)

type detachedDriver struct {
	engine.Driver
	self    *engine.SelfContainer
	selfErr error
	start   func(context.Context, engine.SelfContainer, string, string, engine.Stack) error
	state   func(context.Context, string) (engine.DetachedState, error)
	removed atomic.Int32
}

func (d *detachedDriver) SelfContainer(context.Context) (*engine.SelfContainer, error) {
	return d.self, d.selfErr
}

func (d *detachedDriver) StartDetached(ctx context.Context, self engine.SelfContainer, name, path string, base engine.Stack) error {
	return d.start(ctx, self, name, path, base)
}

func (d *detachedDriver) DetachedState(ctx context.Context, name string) (engine.DetachedState, error) {
	return d.state(ctx, name)
}

func (d *detachedDriver) RemoveDetached(context.Context, string) error {
	d.removed.Add(1)
	return nil
}

func detachedFixture(t *testing.T) (*agent, *detachedDriver, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a := newAgent(ctx, config{Global: globalConfig{Engine: "docker", Paired: true}})
	if err := a.initRegistries(t.TempDir()); nil != err {
		t.Fatal(err)
	}
	driver := &detachedDriver{Driver: a.engine, self: &engine.SelfContainer{ID: "node", Image: "sha256:old", Project: "self"}}
	driver.state = func(context.Context, string) (engine.DetachedState, error) {
		return engine.DetachedState{Running: true}, nil
	}
	a.engine, a.detachedExecutor, a.selfContainer = driver, driver, driver.self
	a.operationsDir = filepath.Join(t.TempDir(), "dcompose", "operations")
	t.Cleanup(func() { cancel(); a.work.Wait() })
	return a, driver, cancel
}

func detachedOp(t *testing.T, a *agent, id string) *operation {
	t.Helper()
	op := &operation{operationSnapshot: operationSnapshot{ID: id, Stack: "self", Action: "update", Status: "running"}}
	if err := a.prepareDetached(op, engine.Stack{Name: "self", Dir: "/stacks/self", ComposeFile: "/stacks/self/compose.yaml", EnvFile: "/stacks/self/.env"}, ""); nil != err {
		t.Fatal(err)
	}
	a.ops[id], a.busy[op.Stack] = op, true
	a.order = append(a.order, id)
	return op
}

func TestDetachedSelection(t *testing.T) {
	a, _, _ := detachedFixture(t)
	for _, action := range []string{"deploy", "start", "stop", "restart", "update", "down", "delete"} {
		detached, err := a.detachedOperation("self", action)
		if !detached || nil != err {
			t.Fatalf("%s: %v %v", action, detached, err)
		}
	}
	if detached, err := a.detachedOperation("other", "update"); detached || nil != err {
		t.Fatalf("other stack: %v %v", detached, err)
	}
	a.selfErr = errors.New("private Docker diagnostic")
	if detached, err := a.detachedOperation("self", "pull"); detached || nil != err {
		t.Fatalf("pull: %v %v", detached, err)
	}
	_, err := a.detachedOperation("self", "stop")
	var safe *engine.Error
	if !errors.As(err, &safe) || !errors.Is(err, a.selfErr) || strings.Contains(safe.Message, "private") {
		t.Fatalf("unsafe detection error: %v", err)
	}
	a.detachedExecutor = nil
	if detached, err := a.detachedOperation("self", "update"); detached || nil != err {
		t.Fatalf("unknown self: %v %v", detached, err)
	}
}

func TestSelfDetectionError(t *testing.T) {
	a, _ := fixture(t)
	a.engine = &detachedDriver{Driver: a.engine, selfErr: errors.New("engine unavailable")}
	a.initDetached(t.TempDir())
	for _, name := range []string{"self", "other"} {
		if w := request(a, "POST", "/stacks/"+name+"/actions", `{"action":"update"}`); 503 != w.Code {
			t.Fatalf("%s: %d", name, w.Code)
		}
	}
	if detached, err := a.detachedOperation("self", "pull"); detached || nil != err {
		t.Fatalf("pull: %v %v", detached, err)
	}
}

func TestSelfOperation(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "self", "compose.yaml")
	driver := &detachedDriver{Driver: a.engine, self: &engine.SelfContainer{Project: "self"}}
	driver.start = func(_ context.Context, _ engine.SelfContainer, name, path string, base engine.Stack) error {
		input, err := readDetachedRequest(path)
		if nil != err {
			return err
		}
		if "dagent-operation-"+input.ID != name || "self" != base.Name || "update" != input.Action {
			return errors.New("incorrect helper request")
		}
		op := &operation{operationSnapshot: operationSnapshot{ID: input.ID, Stack: "self", Action: "update", Output: "helper completed\n"}}
		return writeDetachedResult(filepath.Join(filepath.Dir(path), "result.json"), op, 0)
	}
	driver.state = func(context.Context, string) (engine.DetachedState, error) {
		return engine.DetachedState{}, nil
	}
	a.engine = driver
	a.initDetached(t.TempDir())
	result := completed(t, a, request(a, "POST", "/stacks/self/actions", `{"action":"update"}`))
	if "succeeded" != result.Status || "helper completed\n" != result.Output || 0 != *result.ExitCode {
		t.Fatalf("self-stack result: %+v", result)
	}
}

func TestPodmanSelfOperation(t *testing.T) {
	for _, action := range []string{"deploy", "start", "stop", "restart", "update", "down", "delete"} {
		t.Run(action, func(t *testing.T) {
			a, _ := fixture(t)
			seed(t, a, "self", "compose.yaml")
			a.config.Global.Engine = "podman"
			cache := t.TempDir()
			host := "unix:///run/podman/podman.sock"
			t.Setenv("CONTAINER_HOST", host)
			self := engine.SelfContainer{
				ID: strings.Repeat("a", 64), Image: strings.Repeat("b", 64), Project: "self",
				Mounts: []engine.ContainerMount{
					{Type: "bind", Source: cache, Destination: cache, RW: true},
					{Type: "bind", Source: a.config.Global.StacksDir, Destination: a.config.Global.StacksDir, RW: true},
				},
			}
			var started, removed atomic.Int32
			driver := podman.New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if "podman" != cmd.Program || 4 > len(cmd.Args) || "--remote" != cmd.Args[0] || "--url" != cmd.Args[1] || host != cmd.Args[2] {
					return nil, nil, fmt.Errorf("unexpected Podman command: %+v", cmd)
				}
				args := cmd.Args[3:]
				switch args[0] {
				case "ps":
					return []byte(self.ID + "\n"), nil, nil
				case "inspect":
					if strings.Contains(strings.Join(args, " "), ".State.Running") {
						return []byte(`{"Running":false,"ExitCode":0}`), nil, nil
					}
					data, err := json.Marshal(self)
					return data, nil, err
				case "run":
					path := args[len(args)-1]
					input, err := readDetachedRequest(path)
					if nil != err {
						return nil, nil, err
					}
					if "podman" != input.Engine || "self" != input.Stack.Name || action != input.Action {
						return nil, nil, fmt.Errorf("unexpected helper request: %+v", input)
					}
					started.Add(1)
					op := &operation{operationSnapshot: operationSnapshot{ID: input.ID, Stack: input.Stack.Name, Action: input.Action, Output: "podman helper completed\n"}}
					return nil, nil, writeDetachedResult(filepath.Join(filepath.Dir(path), "result.json"), op, 0)
				case "logs":
					return nil, nil, nil
				case "rm":
					removed.Add(1)
					return nil, nil, nil
				default:
					return nil, nil, fmt.Errorf("unexpected Podman command: %+v", cmd)
				}
			})
			a.engine = driver
			a.initDetached(cache)
			if nil != a.selfErr || nil == a.selfContainer || self.ID != a.selfContainer.ID || driver != a.detachedExecutor {
				t.Fatalf("Podman detached selection: self=%+v executor=%T error=%v", a.selfContainer, a.detachedExecutor, a.selfErr)
			}
			method, path, body := "POST", "/stacks/self/actions", `{"action":"`+action+`"}`
			if "delete" == action {
				method, path, body = "DELETE", "/stacks/self", ""
			}
			result := completed(t, a, request(a, method, path, body))
			a.work.Wait()
			if "succeeded" != result.Status || nil == result.ExitCode || 0 != *result.ExitCode || "podman helper completed\n" != result.Output || 1 != started.Load() || 1 != removed.Load() {
				t.Fatalf("Podman helper result: %+v started=%d removed=%d", result, started.Load(), removed.Load())
			}
		})
	}
}

func TestDetachedRequestEngine(t *testing.T) {
	for _, name := range []string{"docker", "podman", "", "unsupported"} {
		t.Run(name, func(t *testing.T) {
			a, _, _ := detachedFixture(t)
			a.config.Global.Engine = name
			op := detachedOp(t, a, "000000000000000e")
			path := filepath.Join(a.operationsDir, op.ID, "request.json")
			input, err := readDetachedRequest(path)
			if "unsupported" == name {
				if nil == err {
					t.Fatal("unsupported engine accepted")
				}
				return
			}
			if nil != err || name != input.Engine {
				t.Fatalf("request engine: %q error=%v", input.Engine, err)
			}
			if "" == name {
				data, err := os.ReadFile(path)
				if nil != err || strings.Contains(string(data), `"engine"`) {
					t.Fatalf("legacy request: %s error=%v", data, err)
				}
			}
		})
	}
}

func TestDetachedPrivateRequest(t *testing.T) {
	a, _, _ := detachedFixture(t)
	op := detachedOp(t, a, "0000000000000001")
	directory := filepath.Join(a.operationsDir, op.ID)
	for path, mode := range map[string]os.FileMode{directory: 0700, filepath.Join(directory, "request.json"): 0600} {
		info, err := os.Stat(path)
		if nil != err || mode != info.Mode().Perm() {
			t.Fatalf("private path %s: %v %v", path, info, err)
		}
	}
	request, err := readDetachedRequest(filepath.Join(directory, "request.json"))
	if nil != err || op.ID != request.ID || "self" != request.Stack.Name || "update" != request.Action || request.CreatedAt.IsZero() {
		t.Fatalf("request: %+v %v", request, err)
	}
	a.operationsDir = ""
	err = a.prepareDetached(op, request.Stack, "")
	var safe *engine.Error
	if !errors.As(err, &safe) || nil == safe.Cause {
		t.Fatalf("cache error: %v", err)
	}
}

func TestDetachedLaunchFailure(t *testing.T) {
	a, driver, _ := detachedFixture(t)
	op := detachedOp(t, a, "0000000000000002")
	driver.start = func(_ context.Context, self engine.SelfContainer, name, path string, base engine.Stack) error {
		if "node" != self.ID || "dagent-operation-"+op.ID != name || filepath.Join(a.operationsDir, op.ID, "request.json") != path || "self" != base.Name {
			t.Fatal("incorrect launch metadata")
		}
		return errors.New("helper launch denied")
	}
	a.runDetached(op, engine.Stack{Name: "self"})
	result, err := readDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op)
	if nil != err || "failed" != result.Status || !strings.Contains(result.Output, "helper launch denied") || a.busy[op.Stack] {
		t.Fatalf("launch result: %+v %v", result, err)
	}
}

func TestDetachedLaunchCanceled(t *testing.T) {
	a, driver, cancel := detachedFixture(t)
	op := detachedOp(t, a, "0000000000000003")
	driver.start = func(context.Context, engine.SelfContainer, string, string, engine.Stack) error {
		cancel()
		return context.Canceled
	}
	a.runDetached(op, engine.Stack{Name: "self"})
	if _, err := os.Stat(filepath.Join(a.operationsDir, op.ID, "result.json")); !os.IsNotExist(err) || "running" != op.snapshot().Status {
		t.Fatalf("cancellation wrote a terminal result: %v", err)
	}
}

func TestDetachedMissingResult(t *testing.T) {
	for _, code := range []int{0, 9} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			a, driver, _ := detachedFixture(t)
			op := detachedOp(t, a, "0000000000000004")
			driver.state = func(context.Context, string) (engine.DetachedState, error) {
				return engine.DetachedState{ExitCode: code, Output: "helper panic details"}, nil
			}
			a.observeDetached(op)
			result, err := readDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op)
			if nil != err || "failed" != result.Status || nil == result.ExitCode || 0 == *result.ExitCode || (0 != code && code != *result.ExitCode) || !strings.Contains(result.Output, "helper panic details") || 1 != driver.removed.Load() || a.busy[op.Stack] {
				t.Fatalf("missing result: %+v %v", result, err)
			}
		})
	}
}

func TestDetachedInspectOutage(t *testing.T) {
	logs := captureAutomaticLogs(t)
	a, driver, _ := detachedFixture(t)
	op := detachedOp(t, a, "0000000000000005")
	calls := 0
	driver.state = func(context.Context, string) (engine.DetachedState, error) {
		calls++
		if 1 == calls {
			if err := writeDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op, 0); nil != err {
				t.Fatal(err)
			}
			return engine.DetachedState{}, errors.New("Docker daemon unavailable")
		}
		return engine.DetachedState{}, nil
	}
	a.observeDetached(op)
	if snapshot := op.snapshot(); "succeeded" != snapshot.Status || "" != snapshot.Output || 2 != calls {
		t.Fatalf("outage changed helper result: %+v calls=%d", snapshot, calls)
	}
	matches := 0
	for _, line := range strings.Split(strings.TrimSpace(logs.text()), "\n") {
		var record struct {
			Level, Operation, Error string
			Message                 string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &record); nil != err {
			t.Fatal(err)
		}
		if "WARN" != record.Level || op.ID != record.Operation || "Docker daemon unavailable" != record.Error {
			continue
		}
		matches++
		for _, detail := range []string{op.ID, op.Stack, op.Action, "dagent-operation-" + op.ID, record.Error} {
			if !strings.Contains(record.Message, detail) {
				t.Fatalf("inspection message hides %q: %s", detail, record.Message)
			}
		}
	}
	if 1 != matches {
		t.Fatalf("inspection warning count: %d; logs: %s", matches, logs.text())
	}
}

func TestDetachedCompletedRemoved(t *testing.T) {
	a, driver, _ := detachedFixture(t)
	op := detachedOp(t, a, "000000000000000b")
	_, _ = op.Write([]byte("already completed\n"))
	if err := writeDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op, 0); nil != err {
		t.Fatal(err)
	}
	calls := 0
	driver.state = func(context.Context, string) (engine.DetachedState, error) {
		calls++
		return engine.DetachedState{}, errors.New("container already removed")
	}
	a.ops, a.busy, a.order = map[string]*operation{}, map[string]bool{}, nil
	a.initDetached(filepath.Dir(filepath.Dir(a.operationsDir)))
	a.work.Wait()
	if 1 != calls || "succeeded" != a.ops[op.ID].snapshot().Status || a.busy[op.Stack] {
		t.Fatalf("completed cleanup: calls=%d snapshot=%+v", calls, a.ops[op.ID].snapshot())
	}
}

func TestDetachedCollected(t *testing.T) {
	a, driver, _ := detachedFixture(t)
	op := detachedOp(t, a, "000000000000000d")
	if err := writeDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op, 0); nil != err {
		t.Fatal(err)
	}
	driver.state = func(context.Context, string) (engine.DetachedState, error) {
		return engine.DetachedState{}, nil
	}
	a.observeDetached(op)
	if _, err := os.Stat(filepath.Join(a.operationsDir, op.ID, "collected")); !os.IsNotExist(err) {
		t.Fatal("helper cleanup incorrectly consumed the relay")
	}
	a.collectRelay(op)
	driver.state = func(context.Context, string) (engine.DetachedState, error) {
		t.Error("completed and collected helpers must not be inspected on restart")
		return engine.DetachedState{}, errors.New("already collected")
	}
	a.ops, a.busy, a.order = map[string]*operation{}, map[string]bool{}, nil
	a.initDetached(filepath.Dir(filepath.Dir(a.operationsDir)))
	a.work.Wait()
	if nil != a.ops[op.ID] || 1 != driver.removed.Load() {
		t.Fatal("collected result was restored")
	}
}

func TestDetachedInspectCanceled(t *testing.T) {
	a, driver, cancel := detachedFixture(t)
	op := detachedOp(t, a, "000000000000000c")
	driver.state = func(context.Context, string) (engine.DetachedState, error) {
		cancel()
		return engine.DetachedState{}, nil
	}
	a.observeDetached(op)
	if _, err := os.Stat(filepath.Join(a.operationsDir, op.ID, "result.json")); !os.IsNotExist(err) || "running" != op.snapshot().Status {
		t.Fatalf("canceled inspection wrote a terminal result: %v", err)
	}
}

func TestDetachedOutputAndCleanup(t *testing.T) {
	a, driver, _ := detachedFixture(t)
	op := detachedOp(t, a, "0000000000000006")
	file := filepath.Join(a.operationsDir, op.ID, "output.log")
	if err := os.WriteFile(file, []byte("progress \xe2\x82"), 0600); nil != err {
		t.Fatal(err)
	}
	calls := 0
	driver.state = func(context.Context, string) (engine.DetachedState, error) {
		calls++
		if 1 == calls {
			_, _ = op.Write([]byte("\xac done\n"))
			if err := writeDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op, 7); nil != err {
				t.Fatal(err)
			}
		}
		if 3 > calls {
			if 0 != driver.removed.Load() {
				t.Fatal("removed a running helper")
			}
			return engine.DetachedState{Running: true}, nil
		}
		return engine.DetachedState{ExitCode: 7}, nil
	}
	a.observeDetached(op)
	result := op.snapshot()
	if "progress € done\n" != result.Output || "failed" != result.Status || 7 != *result.ExitCode || !utf8.ValidString(result.Output) || 1 != driver.removed.Load() || a.busy[op.Stack] {
		t.Fatalf("output/cleanup: %+v", result)
	}
}

func TestDetachedRestore(t *testing.T) {
	a, driver, cancel := detachedFixture(t)
	cache := filepath.Dir(filepath.Dir(a.operationsDir))
	newer := detachedOp(t, a, "0000000000000007")
	older := detachedOp(t, a, "0000000000000008")
	for op, created := range map[*operation]time.Time{newer: time.Now(), older: time.Now().Add(-time.Hour)} {
		path := filepath.Join(a.operationsDir, op.ID, "request.json")
		request, err := readDetachedRequest(path)
		if nil != err {
			t.Fatal(err)
		}
		request.CreatedAt = created
		data, err := json.Marshal(request)
		if nil != err {
			t.Fatal(err)
		}
		if err := configutil.Write(path, data); nil != err {
			t.Fatal(err)
		}
	}
	_, _ = older.Write([]byte("completed on old node\n"))
	if err := writeDetachedResult(filepath.Join(a.operationsDir, older.ID, "result.json"), older, 7); nil != err {
		t.Fatal(err)
	}
	driver.self = &engine.SelfContainer{Project: "different-project"}
	a.ops, a.busy, a.order = map[string]*operation{}, map[string]bool{}, nil
	a.initDetached(cache)
	if 2 != len(a.order) || older.ID != a.order[0] || newer.ID != a.order[1] || !a.busy["self"] {
		t.Fatalf("restored order/busy: %v %v", a.order, a.busy)
	}
	result := a.ops[older.ID].snapshot()
	if "failed" != result.Status || 7 != *result.ExitCode || "completed on old node\n" != result.Output {
		t.Fatalf("restored snapshot: %+v", result)
	}
	cancel()
	a.work.Wait()
}

func TestDetachedRestoreCorrupt(t *testing.T) {
	a, _, cancel := detachedFixture(t)
	op := detachedOp(t, a, "0000000000000009")
	path := filepath.Join(a.operationsDir, op.ID, "request.json")
	request, err := readDetachedRequest(path)
	if nil != err {
		t.Fatal(err)
	}
	request.ID = "../elsewhere"
	data, err := json.Marshal(request)
	if nil != err {
		t.Fatal(err)
	}
	if err := configutil.Write(path, data); nil != err {
		t.Fatal(err)
	}
	a.ops, a.busy, a.order = map[string]*operation{}, map[string]bool{}, nil
	a.initDetached(filepath.Dir(filepath.Dir(a.operationsDir)))
	if 0 != len(a.ops) || 0 != len(a.order) {
		t.Fatal("restored corrupt operation")
	}
	cancel()
}

func TestDetachedUnconsumed(t *testing.T) {
	a, driver, _ := detachedFixture(t)
	for i := 0; 101 > i; i++ {
		op := detachedOp(t, a, fmt.Sprintf("%016x", i))
		if err := writeDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op, 0); nil != err {
			t.Fatal(err)
		}
	}
	driver.state = func(context.Context, string) (engine.DetachedState, error) {
		return engine.DetachedState{}, errors.New("container already removed")
	}
	a.ops, a.busy, a.order = map[string]*operation{}, map[string]bool{}, nil
	a.initDetached(filepath.Dir(filepath.Dir(a.operationsDir)))
	a.work.Wait()
	entries, err := os.ReadDir(a.operationsDir)
	if nil != err || 101 != len(entries) || 101 != len(a.ops) || 101 != len(a.order) {
		t.Fatalf("retained jobs: files=%d memory=%d order=%d error=%v", len(entries), len(a.ops), len(a.order), err)
	}
}

func TestDetachedNoStartupDirectory(t *testing.T) {
	a, _, _ := detachedFixture(t)
	cache := filepath.Dir(filepath.Dir(a.operationsDir))
	a.initDetached(cache)
	if _, err := os.Stat(a.operationsDir); !os.IsNotExist(err) {
		t.Fatalf("startup created operations directory: %v", err)
	}
}

func TestDetachedLogCap(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "output")
	if nil != err {
		t.Fatal(err)
	}
	defer file.Close()
	log := &detachedLog{file: file, remaining: 1 << 20}
	data := []byte(strings.Repeat("x", (1<<20)+32))
	if n, err := log.Write(data); len(data) != n || nil != err {
		t.Fatalf("bounded writer shortened write: %d %v", n, err)
	}
	info, err := file.Stat()
	if nil != err || 1<<20 != info.Size() {
		t.Fatalf("log size: %v %v", info, err)
	}
}

func TestRunOperation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			a, directory := fixture(t)
			stackDir := seed(t, a, "self", "compose.yaml")
			base, err := a.engineStack("self", stackDir)
			if nil != err {
				t.Fatal(err)
			}
			a.operationsDir = filepath.Join(directory, "operations")
			op := &operation{operationSnapshot: operationSnapshot{ID: "000000000000000a", Stack: "self", Action: "restart", Status: "running"}}
			if err := a.prepareDetached(op, base, "web"); nil != err {
				t.Fatal(err)
			}
			if failed {
				write(t, filepath.Join(directory, "fail"), "")
			}
			err = RunOperation(a.ctx, filepath.Join(a.operationsDir, op.ID, "request.json"))
			if failed != (nil != err) {
				t.Fatalf("helper error: %v", err)
			}
			result, err := readDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op)
			if nil != err || !strings.Contains(result.Output, "started") || !utf8.ValidString(result.Output) {
				t.Fatalf("helper result: %+v %v", result, err)
			}
			if failed && (7 != *result.ExitCode || !strings.Contains(result.Output, "docker failed")) {
				t.Fatalf("lost Compose failure: %+v", result)
			}
			output, err := os.ReadFile(filepath.Join(a.operationsDir, op.ID, "output.log"))
			if nil != err || !strings.Contains(string(output), "started") {
				t.Fatalf("helper log: %q %v", output, err)
			}
		})
	}
}

func TestRunOperationPodman(t *testing.T) {
	a, directory := fixture(t)
	a.config.Global.Engine = "podman"
	host := "unix:///run/user/1000/podman/podman.sock"
	t.Setenv("CONTAINER_HOST", host)
	t.Setenv("DOCKER_HOST", "tcp://wrong-engine:2376")
	t.Setenv("DOCKER_CONTEXT", "wrong-context")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", "/wrong/certificates")
	write(t, filepath.Join(directory, "docker"), "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$DOCKER_HOST\" \"${DOCKER_CONTEXT-unset}\" \"${DOCKER_TLS_VERIFY-unset}\" \"${DOCKER_CERT_PATH-unset}\" >> \"$FAKE_DIR/compose-env\"\n"+readFixture(t, "docker.sh"))
	stackDir := seed(t, a, "self", "compose.yaml")
	base, err := a.engineStack("self", stackDir)
	if nil != err {
		t.Fatal(err)
	}
	a.operationsDir = filepath.Join(directory, "operations")
	op := &operation{operationSnapshot: operationSnapshot{ID: "000000000000000f", Stack: "self", Action: "restart", Status: "running"}}
	if err := a.prepareDetached(op, base, "web"); nil != err {
		t.Fatal(err)
	}
	if err := RunOperation(a.ctx, filepath.Join(a.operationsDir, op.ID, "request.json")); nil != err {
		t.Fatal(err)
	}
	result, err := readDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op)
	if nil != err || "succeeded" != result.Status || nil == result.ExitCode || 0 != *result.ExitCode || !strings.Contains(result.Output, "restart started") {
		t.Fatalf("Podman helper result: %+v error=%v", result, err)
	}
	environment, err := os.ReadFile(filepath.Join(directory, "compose-env"))
	if nil != err || "" == strings.TrimSpace(string(environment)) {
		t.Fatalf("Compose environment: %q error=%v", environment, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(environment)), "\n") {
		if host+"|||" != line {
			t.Fatalf("Compose transport: %q", line)
		}
	}
	args, err := os.ReadFile(filepath.Join(directory, "argv"))
	if nil != err || !strings.Contains(string(args), "<compose>") || !strings.Contains(string(args), "<restart><--><web>") {
		t.Fatalf("Compose command: %s error=%v", args, err)
	}
}
