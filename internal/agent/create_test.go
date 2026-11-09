package agent

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func TestCreate(t *testing.T) {
	a, fake := fixture(t)
	compose := "# INVALID to the fake validator; preserve this comment\nservices:\n  web:\n    image: ${IMAGE:?required}\n    env_file: ./missing.env\n    volumes: ['./missing:/data']\nsecrets:\n  key:\n    file: ./missing.key\n"
	env := "# saved raw\nIMAGE='example:latest'\nSECRET=private-value\n"
	body, err := json.Marshal(map[string]string{"compose": compose, "env": env})
	if nil != err {
		t.Fatal(err)
	}
	w := request(a, http.MethodPost, "/stacks/copy", string(body))
	var result struct {
		Name string `json:"name"`
	}
	if 200 != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &result) || "copy" != result.Name ||
		strconv.Itoa(protocol.AgentAPI) != w.Header().Get(protocol.Header) {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	dir := filepath.Join(a.config.Global.StacksDir, "copy")
	for name, want := range map[string]string{"compose.yaml": compose, ".env": env} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if nil != err || want != string(got) {
			t.Fatalf("saved %s: %q %v", name, got, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if nil != err || 2 != len(entries) {
		t.Fatalf("created extra files: %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatal("create invoked the engine")
	}
	if 0 != len(a.ops) || 0 != len(a.order) || a.busy["copy"] || !a.events.pending["copy"] {
		t.Fatal("create retained an operation or failed to signal discovery")
	}
	if names, err := a.inventory(); nil != err || !reflect.DeepEqual([]string{"copy"}, names) {
		t.Fatalf("copy not discovered: %v %v", names, err)
	}
	w = request(a, http.MethodGet, "/stacks/copy", "")
	var saved stack
	if 200 != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &saved) || compose != saved.Compose || env != saved.Env ||
		"draft" != saved.Status {
		t.Fatalf("copy not readable: %d %s", w.Code, w.Body)
	}
}

func TestCreateNoEnv(t *testing.T) {
	a, fake := fixture(t)
	w := request(a, http.MethodPost, "/stacks/copy", `{"compose":"services: {}"}`)
	if 200 != w.Code {
		t.Fatalf("create without env: %d %s", w.Code, w.Body)
	}
	compose, err := os.ReadFile(filepath.Join(a.config.Global.StacksDir, "copy", "compose.yaml"))
	if nil != err || "services: {}" != string(compose) {
		t.Fatalf("saved compose: %q %v", compose, err)
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatal("create invoked the engine")
	}
}

func TestCreateConflict(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(strconv.FormatBool(existing), func(t *testing.T) {
			a, fake := fixture(t)
			dir := filepath.Join(a.config.Global.StacksDir, "copy")
			files := map[string]string{"keep.txt": "keep"}
			if existing {
				dir = seed(t, a, "copy", "compose.yml")
				files["compose.yml"] = "old compose"
				files[".env"] = "OLD=secret\n"
			} else if err := os.Mkdir(dir, 0750); nil != err {
				t.Fatal(err)
			}
			for name, content := range files {
				write(t, filepath.Join(dir, name), content)
			}
			for _, body := range []string{`{"compose":"new","env":"new"}`, `{"compose":"new","env":"new","overwrite":false}`} {
				w := request(a, http.MethodPost, "/stacks/copy", body)
				var response struct {
					Error string `json:"error"`
				}
				if 409 != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &response) || "" == response.Error ||
					strings.Contains(w.Body.String(), "secret") {
					t.Fatalf("conflict: %d %s", w.Code, w.Body)
				}
				for name, want := range files {
					got, err := os.ReadFile(filepath.Join(dir, name))
					if nil != err || want != string(got) {
						t.Fatalf("conflict changed %s: %q %v", name, got, err)
					}
				}
				entries, err := os.ReadDir(dir)
				if nil != err || len(files) != len(entries) {
					t.Fatalf("conflict wrote files: %v %v", entries, err)
				}
			}
			if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
				t.Fatal("conflict invoked the engine")
			}
		})
	}
}

func TestCreateOverwrite(t *testing.T) {
	for _, composeName := range composeNames {
		t.Run(composeName, func(t *testing.T) {
			a, fake := fixture(t)
			dir := seed(t, a, "copy", composeName)
			write(t, filepath.Join(dir, ".env"), "OLD=value\n")
			if err := os.Mkdir(filepath.Join(dir, "data"), 0750); nil != err {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "data", "keep"), "persistent data")
			write(t, filepath.Join(dir, "override.yaml"), "user override")
			runtime := `[{"Name":"copy-web-1","State":"running"}]`
			write(t, filepath.Join(fake, "ps"), runtime)
			w := request(a, http.MethodPost, "/stacks/copy", `{"compose":"INVALID raw compose\n","env":"NEW=value\n","overwrite":true}`)
			if 200 != w.Code {
				t.Fatalf("overwrite: %d %s", w.Code, w.Body)
			}
			for name, want := range map[string]string{composeName: "INVALID raw compose\n", ".env": "NEW=value\n", "data/keep": "persistent data", "override.yaml": "user override"} {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if nil != err || want != string(got) {
					t.Fatalf("overwrite changed %s: %q %v", name, got, err)
				}
			}
			entries, err := os.ReadDir(dir)
			if nil != err || 4 != len(entries) {
				t.Fatalf("overwrite left extra files: %v %v", entries, err)
			}
			if got, err := os.ReadFile(filepath.Join(fake, "ps")); nil != err || runtime != string(got) {
				t.Fatal("overwrite changed running containers")
			}
			if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
				t.Fatal("overwrite invoked the engine")
			}
			if 0 != len(a.ops) || 0 != len(a.order) || a.busy["copy"] {
				t.Fatal("overwrite retained an operation")
			}
		})
	}
}

func TestCreatePaths(t *testing.T) {
	for _, target := range []string{"stack", "compose.yaml", ".env"} {
		t.Run(target, func(t *testing.T) {
			a, fake := fixture(t)
			outside := t.TempDir()
			write(t, filepath.Join(outside, "secret"), "outside-secret")
			dir := filepath.Join(a.config.Global.StacksDir, "copy")
			link, destination := dir, outside
			if "stack" != target {
				if err := os.Mkdir(dir, 0750); nil != err {
					t.Fatal(err)
				}
				link, destination = filepath.Join(dir, target), filepath.Join(outside, "secret")
			}
			if err := os.Symlink(destination, link); nil != err {
				t.Fatal(err)
			}
			w := request(a, http.MethodPost, "/stacks/copy", `{"compose":"raw","env":"raw","overwrite":true}`)
			if 400 != w.Code || strings.Contains(w.Body.String(), "outside-secret") {
				t.Fatalf("accepted symlink: %d %s", w.Code, w.Body)
			}
			if got, err := os.ReadFile(filepath.Join(outside, "secret")); nil != err || "outside-secret" != string(got) {
				t.Fatal("changed symlink target")
			}
			if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
				t.Fatal("symlink request invoked the engine")
			}
		})
	}
}

func TestCreateInput(t *testing.T) {
	a, fake := fixture(t)
	for _, body := range []string{`null`, `{}`, `{"compose":"","env":""}`, `{"compose":"raw","env":3}`, `{"compose":"raw","env":"","overwrite":"true"}`, `{"compose":"raw","env":"","extra":true}`, `{"compose":"raw","env":""}{}`} {
		if w := request(a, http.MethodPost, "/stacks/copy", body); 400 != w.Code {
			t.Fatalf("accepted body %s: %d", body, w.Code)
		}
	}
	if err := a.lockStack("copy"); nil != err {
		t.Fatal(err)
	}
	defer a.unlockStack("copy")
	if w := request(a, http.MethodPost, "/stacks/copy", `{"compose":"raw","env":"","overwrite":true}`); 409 != w.Code {
		t.Fatalf("ignored active stack lock: %d %s", w.Code, w.Body)
	}
	if names, err := a.inventory(); nil != err || 0 != len(names) {
		t.Fatalf("rejected input created stack: %v %v", names, err)
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatal("rejected input invoked the engine")
	}
}
