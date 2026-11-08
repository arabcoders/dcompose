package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if nil != err {
		t.Fatal(err)
	}
	return string(data)
}

func fixture(t *testing.T) (*agent, string) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "docker"), readFixture(t, "docker.sh"))
	if err := os.Chmod(filepath.Join(dir, "docker"), 0700); nil != err {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	c, err := loadConfig(configFixture(t, "", "allowed_ips = ['127.0.0.1', '::1']"))
	if nil != err {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := newAgent(ctx, c)
	subscriber := newSubscriber()
	subscriber.events = make(chan streamEvent, 4096)
	testRuntime.Store(a, subscriber)
	t.Cleanup(func() { testRuntime.Delete(a) })
	if err := a.initRegistries(filepath.Dir(c.path)); nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); a.work.Wait() })
	return a, dir
}

func request(a *agent, method, path, body string) *httptest.ResponseRecorder {
	if ("POST" == method && strings.HasSuffix(path, "/actions")) || ("DELETE" == method && strings.HasPrefix(path, "/stacks/")) {
		collectRuntime(a)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:42"
	r.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func seed(t *testing.T, a *agent, name, file string) string {
	t.Helper()
	dir := filepath.Join(a.config.Global.StacksDir, name)
	if err := os.Mkdir(dir, 0750); nil != err {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, file), "# keep comments\nservices:\n  web:\n    image: example\n")
	return dir
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}

var testRuntime sync.Map

func collectRuntime(a *agent) {
	if value, ok := testRuntime.Load(a); ok {
		a.events.mu.Lock()
		a.events.subscribers[value.(*eventSubscriber)] = struct{}{}
		a.events.mu.Unlock()
	}
}

func completed(t *testing.T, a *agent, w *httptest.ResponseRecorder) operationSnapshot {
	t.Helper()
	if 202 != w.Code {
		t.Fatalf("operation: %d %s", w.Code, w.Body)
	}
	var accepted struct {
		Status string `json:"status"`
		Relay  string `json:"relay"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); nil != err {
		t.Fatal(err)
	}
	if "accepted" != accepted.Status {
		t.Fatalf("acceptance: %s", w.Body)
	}
	if "" != accepted.Relay {
		a.mu.Lock()
		op := a.ops[accepted.Relay]
		a.mu.Unlock()
		eventually(t, func() bool { return "running" != op.snapshot().Status })
		return op.snapshot()
	}
	value, ok := testRuntime.Load(a)
	if !ok {
		t.Fatal("missing runtime collector")
	}
	subscriber := value.(*eventSubscriber)
	op := operationSnapshot{Status: "running"}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-subscriber.events:
			switch e.name {
			case "stack.output":
				var output protocol.StackOutput
				if err := json.Unmarshal(e.data, &output); nil != err {
					t.Fatal(err)
				}
				op.Output += output.Output
			case "stack.action":
				var action protocol.StackAction
				if err := json.Unmarshal(e.data, &action); nil != err {
					t.Fatal(err)
				}
				op.Stack, op.Action = action.Name, action.Action
				if "exit" == action.Phase {
					op.ExitCode, op.Status = action.ExitCode, "succeeded"
					if nil == action.ExitCode || 0 != *action.ExitCode {
						op.Status = "failed"
					}
					return op
				}
			}
		case <-deadline:
			t.Fatal("action did not exit")
		}
	}
}

func TestHealth(t *testing.T) {
	a, _ := fixture(t)
	w := request(a, http.MethodGet, "/health", "")
	var health nodeHealth
	if err := json.Unmarshal(w.Body.Bytes(), &health); nil != err {
		t.Fatal(err)
	}
	if 200 != w.Code || "ok" != health.Status || version != health.Version || runtime.GOOS != health.OS || runtime.GOARCH != health.Arch || !a.startedAt.Equal(health.StartedAt) || a.config.Global.ImageCheckInterval != health.ImageCheckInterval {
		t.Fatalf("health: %s", w.Body)
	}
	if protocol.AgentAPI != health.APIVersion || strconv.Itoa(protocol.AgentAPI) != w.Header().Get(protocol.Header) {
		t.Fatalf("health protocol metadata: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), `"sha256"`) {
		t.Fatal("health advertises executable checksum")
	}
	if a.startedAt.Equal(newAgent(a.ctx, a.config).startedAt) {
		t.Fatal("start timestamp was reused")
	}
}

func TestHealthVersions(t *testing.T) {
	a, _ := fixture(t)
	original := version
	t.Cleanup(func() { version = original })
	for _, build := range []string{"dev", "0.7.3", "99.0.0"} {
		version = build
		w := request(a, http.MethodGet, "/health", "")
		var health nodeHealth
		if err := json.Unmarshal(w.Body.Bytes(), &health); nil != err {
			t.Fatal(err)
		}
		if build != health.Version || protocol.AgentAPI != health.APIVersion {
			t.Fatalf("application and API versions are coupled: %s", w.Body)
		}
	}
}

func TestRouteAuth(t *testing.T) {
	a, _ := fixture(t)
	for _, route := range []struct{ method, path string }{{"GET", "/health"}, {"POST", "/stacks/demo"}, {"PUT", "/stacks/demo"}, {"POST", "/images/inspect"}, {"POST", "/registry/request"}} {
		for _, test := range []struct {
			peer, token string
			code        int
		}{{"127.0.0.1:42", "", 401}, {"127.0.0.1:42", "wrong", 401}, {"192.0.2.1:42", "test-secret", 403}} {
			r := httptest.NewRequest(route.method, route.path, strings.NewReader("untrusted"))
			r.RemoteAddr = test.peer
			r.Header.Set("Authorization", "Bearer "+test.token)
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			if test.code != w.Code {
				t.Fatalf("%s: %d %s", route.path, w.Code, w.Body)
			}
			if http.StatusForbidden == w.Code {
				var body struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); nil != err {
					t.Fatal(err)
				}
				if "" == body.Error || strings.Contains(body.Error, "192.0.2.1") || strings.Contains(body.Error, "127.0.0.1") || strings.Contains(body.Error, "test-secret") {
					t.Fatalf("forbidden response exposed access rules: %s", w.Body)
				}
			}
		}
	}
}

func TestBinaryRemoved(t *testing.T) {
	a, fake := fixture(t)
	if w := request(a, http.MethodPut, "/binary", "untrusted"); 404 != w.Code {
		t.Fatalf("obsolete route: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatal("obsolete route invoked the engine")
	}
}

func TestAPIVersion(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	for _, headers := range [][]string{{strconv.Itoa(protocol.AgentAPI + 1)}, {"invalid"}, {""}, {"1", "2"}} {
		for _, route := range []struct{ method, path, body string }{
			{"POST", "/stacks/demo/actions", `{"action":"up"}`},
			{"DELETE", "/stacks/demo", ""},
			{"PUT", "/stacks/new", `{"compose":"services: {}","env":""}`},
			{"POST", "/stacks/new", `{"compose":"services: {}","env":""}`},
			{"POST", "/stacks/demo/shells", `{}`},
			{"POST", "/images/inspect", `{"stack":"demo"}`},
			{"POST", "/registry/request", `{}`},
			{"GET", "/stacks/demo/status", ""},
			{"GET", "/operations", ""},
			{"GET", "/channel", ""},
		} {
			r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
			r.RemoteAddr = "127.0.0.1:42"
			r.Header.Set("Authorization", "Bearer test-secret")
			for _, header := range headers {
				r.Header.Add(protocol.Header, header)
			}
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			if http.StatusUpgradeRequired != w.Code || strconv.Itoa(protocol.AgentAPI) != w.Header().Get(protocol.Header) {
				t.Fatalf("%s %s header %q: %d %s", route.method, route.path, headers, w.Code, w.Body)
			}
		}
	}
	if 0 != len(a.ops) || 0 != len(a.shells) {
		t.Fatal("incompatible request started an operation")
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatal("incompatible request invoked the engine")
	}
	if _, err := os.Stat(filepath.Join(a.config.Global.StacksDir, "new")); !os.IsNotExist(err) {
		t.Fatal("incompatible request created a stack")
	}
	for _, header := range []string{"", strconv.Itoa(protocol.AgentAPI)} {
		r := httptest.NewRequest(http.MethodGet, "/relays", nil)
		r.RemoteAddr = "127.0.0.1:42"
		r.Header.Set("Authorization", "Bearer test-secret")
		if "" != header {
			r.Header.Set(protocol.Header, header)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if 200 != w.Code {
			t.Fatalf("compatible or absent header %q: %d %s", header, w.Code, w.Body)
		}
	}
	for _, token := range []string{"test-secret", "wrong"} {
		r := httptest.NewRequest(http.MethodGet, "/health", nil)
		r.RemoteAddr = "127.0.0.1:42"
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set(protocol.Header, "999")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		want := 200
		if "wrong" == token {
			want = 401
		}
		if want != w.Code {
			t.Fatalf("health bootstrap token %q: %d %s", token, w.Code, w.Body)
		}
	}
}

func TestPaths(t *testing.T) {
	a, _ := fixture(t)
	for _, name := range []string{"..", "../escape", "-flag", ".hidden", "foo/bar", "foo\\bar", "UPPER"} {
		if _, err := a.stackPath(name, true); nil == err {
			t.Fatalf("accepted %q", name)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(a.config.Global.StacksDir, "outside")); nil != err {
		t.Fatal(err)
	}
	if _, err := a.stackPath("outside", false); nil == err {
		t.Fatal("accepted symlink stack")
	}
	dir := seed(t, a, "files", "compose.yml")
	outside := filepath.Join(t.TempDir(), "secret")
	write(t, outside, "outside-secret")
	if err := os.Symlink(outside, filepath.Join(dir, ".env")); nil != err {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		w := request(a, method, "/stacks/files", `{"compose":"services: {}","env":""}`)
		if 400 != w.Code {
			t.Fatalf("%s followed .env symlink: %d", method, w.Code)
		}
	}
	_ = os.Remove(filepath.Join(dir, ".env"))
	_ = os.Remove(filepath.Join(dir, "compose.yml"))
	if err := os.Symlink(outside, filepath.Join(dir, "compose.yaml")); nil != err {
		t.Fatal(err)
	}
	if w := request(a, "GET", "/stacks/files", ""); 400 != w.Code {
		t.Fatalf("followed compose symlink: %d", w.Code)
	}
}

func TestSave(t *testing.T) {
	a, fake := fixture(t)
	dir := seed(t, a, "demo", "docker-compose.yml")
	write(t, filepath.Join(dir, ".env"), "OLD=value\n")
	compose := "# preserved\nservices:\n  web:\n    image: ${IMAGE}\n    env_file: .env\n    volumes: ['./data:/data']\n"
	input, _ := json.Marshal(struct {
		Compose string `json:"compose"`
		Env     string `json:"env"`
	}{compose, "IMAGE=new\n"})
	w := request(a, "PUT", "/stacks/demo", string(input))
	if 200 != w.Code {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if compose != string(data) {
		t.Fatal("compose changed")
	}
	for _, path := range []string{filepath.Join(dir, ".env"), filepath.Join(fake, "validated-env")} {
		data, _ := os.ReadFile(path)
		if "IMAGE=new\n" != string(data) {
			t.Fatalf("wrong env %s: %q", path, data)
		}
	}
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	if !strings.Contains(string(argv), "<--project-directory><"+dir+">") || !strings.Contains(string(argv), "<config><--quiet><--no-env-resolution><--services>") {
		t.Fatalf("wrong validation argv: %s", argv)
	}
	cwd, _ := os.ReadFile(filepath.Join(fake, "cwd"))
	if dir != string(cwd) {
		t.Fatal("relative mount context changed")
	}
	w = request(a, "PUT", "/stacks/demo", `{"compose":"INVALID","env":"BAD=changed"}`)
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || 400 != w.Code || "" == response.Error || strings.Contains(w.Body.String(), "invalid compose") {
		t.Fatalf("invalid save: %d %s", w.Code, w.Body)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if compose != string(data) {
		t.Fatal("invalid save overwrote compose")
	}
	data, _ = os.ReadFile(filepath.Join(dir, ".env"))
	if "IMAGE=new\n" != string(data) {
		t.Fatal("invalid save overwrote env")
	}
	entries, _ := os.ReadDir(dir)
	if 2 != len(entries) {
		t.Fatalf("temporary files leaked: %v", entries)
	}
	if w := request(a, "PUT", "/stacks/new", string(input)); 200 != w.Code {
		t.Fatalf("new stack: %s", w.Body)
	}
	if w := request(a, "PUT", "/stacks/rejected", `{"compose":"INVALID","env":""}`); 400 != w.Code {
		t.Fatalf("invalid new stack: %s", w.Body)
	}
	if _, err := os.Stat(filepath.Join(a.config.Global.StacksDir, "rejected")); !os.IsNotExist(err) {
		t.Fatal("rejected creation left directory")
	}
	for _, body := range []string{`null`, `{}`, `{"compose":"x"}`, `{"compose":"","env":""}`, `{"compose":"x","env":3}`, `{"compose":"x","env":"","extra":true}`, `{"compose":"x","env":""}{}`} {
		if w := request(a, "PUT", "/stacks/demo", body); 400 != w.Code {
			t.Fatalf("accepted body %s", body)
		}
	}
}

func TestStatus(t *testing.T) {
	a, fake := fixture(t)
	dir := seed(t, a, "demo", "compose.yaml")
	write(t, filepath.Join(dir, ".env"), "SECRET=example\n")
	if err := os.Mkdir(filepath.Join(a.config.Global.StacksDir, "data-only"), 0750); nil != err {
		t.Fatal(err)
	}
	for _, test := range []struct {
		json, status string
	}{
		{`[]`, "draft"},
		{`[{"Name":"demo-web-1","Service":"web","Image":"example","State":"running","Health":"healthy","Publishers":[{"URL":"0.0.0.0","TargetPort":80,"PublishedPort":8080,"Protocol":"tcp"}]}]`, "running"},
		{"{\"State\":\"running\"}\n{\"State\":\"exited\"}\n", "partial"},
		{`[{"State":"exited"}]`, "stopped"},
		{`[{"State":"restarting"}]`, "unknown"},
	} {
		write(t, filepath.Join(fake, "ps"), test.json)
		w := request(a, "GET", "/stacks/demo", "")
		var result stack
		if err := json.Unmarshal(w.Body.Bytes(), &result); nil != err || test.status != result.Status || "SECRET=example\n" != result.Env {
			t.Fatalf("detail: %s %v", w.Body, err)
		}
		if "running" == test.status && "0.0.0.0:8080->80/tcp" != result.Services[0].Ports {
			t.Fatal("ports not normalized")
		}
		w = request(a, "GET", "/stacks", "")
		if strings.Contains(w.Body.String(), "data-only") || !strings.Contains(w.Body.String(), `"status":"`+test.status+`"`) {
			t.Fatalf("list: %s", w.Body)
		}
	}
	write(t, filepath.Join(fake, "failps"), "")
	if w := request(a, "GET", "/stacks/demo", ""); !strings.Contains(w.Body.String(), `"status":"unknown"`) {
		t.Fatal("failed status not unknown")
	}
}

func TestOperations(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	for _, body := range []string{`{"action":"shell"}`, `{"action":"delete"}`, `{"action":"pull","service":"web"}`, `{"action":"start","service":"--help"}`, `{"action":"start","service":"missing"}`} {
		if w := request(a, "POST", "/stacks/demo/actions", body); 400 != w.Code {
			t.Fatalf("accepted %s: %d", body, w.Code)
		}
	}
	write(t, filepath.Join(fake, "block"), "")
	r := httptest.NewRequest("POST", "/stacks/demo/actions", strings.NewReader(`{"action":"start","service":"web"}`))
	ctx, cancel := context.WithCancel(r.Context())
	r = r.WithContext(ctx)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	collectRuntime(a)
	a.ServeHTTP(w, r)
	cancel() // Browser/request disconnect must not cancel an accepted operation.
	eventually(t, func() bool { _, err := os.Stat(filepath.Join(fake, "started")); return nil == err })
	for _, test := range []struct{ method, path, body string }{
		{"POST", "/stacks/demo/actions", `{"action":"stop"}`},
		{"PUT", "/stacks/demo", `{"compose":"services: {}","env":""}`},
		{"DELETE", "/stacks/demo", ""},
	} {
		if r := request(a, test.method, test.path, test.body); 409 != r.Code {
			t.Fatalf("conflict: %d %s", r.Code, r.Body)
		}
	}
	_ = os.Remove(filepath.Join(fake, "block"))
	op := completed(t, a, w)
	if "succeeded" != op.Status || nil == op.ExitCode || 0 != *op.ExitCode {
		t.Fatalf("operation failed after disconnect: %+v", op)
	}
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	if !strings.Contains(string(argv), "<up><-d><--><web>") {
		t.Fatalf("start argv: %s", argv)
	}
	write(t, filepath.Join(fake, "fail"), "")
	op = completed(t, a, request(a, "POST", "/stacks/demo/actions", `{"action":"pull"}`))
	if "failed" != op.Status || 7 != *op.ExitCode || !strings.Contains(op.Output, "docker failed") {
		t.Fatalf("failure hidden: %+v", op)
	}
	if w := request(a, "GET", "/operations/missing", ""); 404 != w.Code {
		t.Fatal("unknown operation not 404")
	}
}

func TestUpdateImages(t *testing.T) {
	for _, test := range []struct {
		name, state, localID, failure string
		start, failed                 bool
	}{
		{name: "current", state: "running", localID: "sha256:old"},
		{name: "changed", state: "running", localID: "sha256:new", start: true},
		{name: "stopped", state: "exited", localID: "sha256:new"},
		{name: "container_error", state: "running", failure: "fail-container-image", failed: true},
		{name: "image_error", state: "running", failure: "fail-local-image", failed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, fake := fixture(t)
			seed(t, a, "demo", "compose.yaml")
			write(t, filepath.Join(fake, "ps"), fmt.Sprintf(`[{"Name":"web","State":%q}]`, test.state))
			write(t, filepath.Join(fake, "local-image-example"), test.localID)
			if "" != test.failure {
				write(t, filepath.Join(fake, test.failure), "")
			}
			op := completed(t, a, request(a, "POST", "/stacks/demo/actions", `{"action":"update"}`))
			argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
			if test.failed != ("failed" == op.Status) || !strings.Contains(string(argv), "<pull>") || test.start != strings.Contains(string(argv), "<up><-d>") {
				t.Fatalf("update: %s %+v", argv, op)
			}
			if !test.failed && ("succeeded" != op.Status || nil == op.ExitCode || 0 != *op.ExitCode) {
				t.Fatalf("update did not complete: %+v", op)
			}
		})
	}
}

func TestUpdateMixed(t *testing.T) {
	for _, state := range []string{"running", "exited"} {
		t.Run(state, func(t *testing.T) {
			a, fake := fixture(t)
			seed(t, a, "demo", "compose.yaml")
			write(t, filepath.Join(fake, "ps"), fmt.Sprintf(`[{"Name":"web","State":"running"},{"Name":"worker","State":%q}]`, state))
			write(t, filepath.Join(fake, "local-image-example"), "sha256:old")
			write(t, filepath.Join(fake, "container-image-worker"), `{"id":"sha256:older","ref":"example"}`)
			op := completed(t, a, request(a, "POST", "/stacks/demo/actions", `{"action":"update"}`))
			argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
			if "succeeded" != op.Status || ("running" == state) != strings.Contains(string(argv), "<up><-d>") {
				t.Fatalf("update: %s %+v", argv, op)
			}
		})
	}
}

func TestUpdateDelete(t *testing.T) {
	a, fake := fixture(t)
	dir := seed(t, a, "demo", "compose.yaml")
	for _, running := range []bool{false, true} {
		_ = os.Remove(filepath.Join(fake, "argv"))
		state := "exited"
		if running {
			state = "running"
		}
		write(t, filepath.Join(fake, "ps"), fmt.Sprintf(`[{"State":%q}]`, state))
		op := completed(t, a, request(a, "POST", "/stacks/demo/actions", `{"action":"update"}`))
		argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
		if "succeeded" != op.Status || !strings.Contains(string(argv), "<pull>") || running != strings.Contains(string(argv), "<up><-d>") {
			t.Fatalf("update running=%t: %s %+v", running, argv, op)
		}
	}
	write(t, filepath.Join(dir, "docker-compose.yml"), "backup")
	write(t, filepath.Join(dir, ".env"), "secret")
	if err := os.Mkdir(filepath.Join(dir, "data"), 0750); nil != err {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "data", "keep"), "data")
	_ = os.Remove(filepath.Join(fake, "argv"))
	op := completed(t, a, request(a, "DELETE", "/stacks/demo", ""))
	if "failed" != op.Status || nil == op.ExitCode || 0 == *op.ExitCode {
		t.Fatalf("ambiguous delete: %+v", op)
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatal("ambiguous delete invoked Docker")
	}
	for file, content := range map[string]string{
		"compose.yaml":       "# keep comments\nservices:\n  web:\n    image: example\n",
		"docker-compose.yml": "backup",
		".env":               "secret",
		"data/keep":          "data",
	} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if nil != err || content != string(data) {
			t.Fatalf("ambiguous delete changed %s: %s %v", file, data, err)
		}
	}
	if err := os.Rename(filepath.Join(dir, "docker-compose.yml"), filepath.Join(dir, "compose.backup.yml")); nil != err {
		t.Fatal(err)
	}
	write(t, filepath.Join(fake, "fail"), "")
	op = completed(t, a, request(a, "DELETE", "/stacks/demo", ""))
	if "failed" != op.Status {
		t.Fatal("delete ignored down failure")
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); nil != err {
		t.Fatal("failed down removed compose")
	}
	_ = os.Remove(filepath.Join(fake, "fail"))
	op = completed(t, a, request(a, "DELETE", "/stacks/demo", ""))
	if "succeeded" != op.Status {
		t.Fatalf("delete: %+v", op)
	}
	for _, file := range []string{"compose.backup.yml", "data/keep"} {
		if _, err := os.Stat(filepath.Join(dir, file)); nil != err {
			t.Fatalf("deleted user data %s: %v", file, err)
		}
	}
	for _, file := range []string{"compose.yaml", ".env"} {
		if _, err := os.Stat(filepath.Join(dir, file)); !os.IsNotExist(err) {
			t.Fatalf("managed file retained: %s", file)
		}
	}
	if names, err := a.inventory(); nil != err || 0 != len(names) {
		t.Fatalf("deleted stack rediscovered: %v %v", names, err)
	}
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	if strings.Contains(string(argv), "--volumes") {
		t.Fatal("requested volume deletion")
	}
}

func TestLogs(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	server := channelServer(t, a)
	conn := openChannel(t, server)
	channelSend(t, conn, protocol.ChannelMessage{Type: "logs.attach", Stream: "logs", Resource: "demo"})
	message := channelUntil(t, conn, "stream.event", "message")
	if "logs" != message.Stream || `{"message":"web | hello"}` != string(message.Payload) {
		t.Fatalf("invalid log frame: %+v", message)
	}
	eventually(t, func() bool { _, err := os.Stat(filepath.Join(fake, "log-child")); return nil == err })
	pidText, _ := os.ReadFile(filepath.Join(fake, "log-pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidText)))
	childText, _ := os.ReadFile(filepath.Join(fake, "log-child"))
	child, _ := strconv.Atoi(strings.TrimSpace(string(childText)))
	channelSend(t, conn, protocol.ChannelMessage{Type: "stream.detach", Stream: "logs"})
	eventually(t, func() bool { return syscall.ESRCH == syscall.Kill(pid, 0) })
	eventually(t, func() bool {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child))
		return os.IsNotExist(err) || strings.Contains(string(data), ") Z")
	})
	// A finite failing logs process sends a terminal event with its exit status.
	write(t, filepath.Join(fake, "log-exit"), "")
	channelSend(t, conn, protocol.ChannelMessage{Type: "logs.attach", Stream: "logs", Resource: "demo"})
	message = channelUntil(t, conn, "stream.event", "done")
	if `{"exit_code":3}` != string(message.Payload) {
		t.Fatalf("terminal event: %s", message.Payload)
	}
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	if !strings.Contains(string(argv), "<logs><--follow><--tail><100><--no-color>") {
		t.Fatalf("logs argv: %s", argv)
	}
}

func TestLimits(t *testing.T) {
	op := &operation{}
	p := []byte(strings.Repeat("x", 2<<20))
	if n, err := op.Write(p); nil != err || len(p) != n || 1<<20 != len(op.snapshot().Output) {
		t.Fatal("output not bounded")
	}
	a, _ := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	a.mu.Lock()
	a.active = 16
	a.mu.Unlock()
	if w := request(a, "POST", "/stacks/demo/actions", `{"action":"pull"}`); 409 != w.Code {
		t.Fatal("active operation limit ignored")
	}
}

func TestShutdown(t *testing.T) {
	a, fake := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	ctx, cancel := context.WithCancel(a.ctx)
	defer cancel()
	a.ctx = ctx
	write(t, filepath.Join(fake, "block"), "")
	w := request(a, "POST", "/stacks/demo/actions", `{"action":"deploy"}`)
	eventually(t, func() bool { _, err := os.Stat(filepath.Join(fake, "op-pid")); return nil == err })
	pidText, _ := os.ReadFile(filepath.Join(fake, "op-pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidText)))
	cancel()
	op := completed(t, a, w)
	if "failed" != op.Status || nil == op.ExitCode || 0 == *op.ExitCode {
		t.Fatalf("shutdown operation: %+v", op)
	}
	eventually(t, func() bool { return syscall.ESRCH == syscall.Kill(pid, 0) })
}

func TestNoOperationHistory(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	current := completed(t, a, request(a, "POST", "/stacks/demo/actions", `{"action":"pull"}`))
	if "" != current.ID || 0 != len(a.ops) || 0 != len(a.order) {
		t.Fatal("ordinary action allocated an operation")
	}
	for _, path := range []string{"/operations", "/operations/missing"} {
		if w := request(a, "GET", path, ""); 404 != w.Code {
			t.Fatalf("history route remains: %s", path)
		}
	}
}
