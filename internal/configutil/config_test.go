package configutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, path string) {
	t.Helper()
	if err := Write(path, []byte("[global]\nlisten = '127.0.0.1:8080'\n")); nil != err {
		t.Fatal(err)
	}
}

func configEnvironment(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DCOMPOSE_CONFIG_DIR", "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "user"))
	t.Setenv("XDG_CONFIG_DIRS", filepath.Join(root, "system"))
	return root
}

func TestFindOrder(t *testing.T) {
	root := configEnvironment(t)
	user := filepath.Join(root, "user/dcompose/dcompose.toml")
	system := filepath.Join(root, "system/dcompose/dcompose.toml")
	writeConfig(t, system)
	if path, err := Find("", "dcompose.toml"); nil != err || system != path {
		t.Fatalf("system path: %q, %v", path, err)
	}
	writeConfig(t, user)
	if path, err := Find("", "dcompose.toml"); nil != err || user != path {
		t.Fatalf("user path: %q, %v", path, err)
	}
	t.Setenv("DCOMPOSE_CONFIG_DIR", filepath.Join(root, "container"))
	container := filepath.Join(root, "container/dcompose.toml")
	if path, err := Find("", "dcompose.toml"); nil != err || container != path {
		t.Fatalf("container path: %q, %v", path, err)
	}
	explicit := filepath.Join(root, "explicit.toml")
	if path, err := Find(explicit, "dcompose.toml"); nil != err || explicit != path {
		t.Fatalf("explicit path: %q, %v", path, err)
	}
}

func TestFindHome(t *testing.T) {
	root := configEnvironment(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	want := filepath.Join(root, "home/.config/dcompose/agent.toml")
	writeConfig(t, want)
	if path, err := Find("", "agent.toml"); nil != err || want != path {
		t.Fatalf("home path: %q, %v", path, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if path, err := Find("", "agent.toml"); nil != err || want != path {
		t.Fatalf("relative XDG home was not ignored: %q, %v", path, err)
	}
}

func TestFindSystemOrder(t *testing.T) {
	root := configEnvironment(t)
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	t.Setenv("XDG_CONFIG_DIRS", "relative"+string(filepath.ListSeparator)+first+string(filepath.ListSeparator)+second)
	for _, directory := range []string{first, second} {
		writeConfig(t, filepath.Join(directory, "dcompose/agent.toml"))
	}
	want := filepath.Join(first, "dcompose/agent.toml")
	if path, err := Find("", "agent.toml"); nil != err || want != path {
		t.Fatalf("system order: %q, %v", path, err)
	}
}

func TestFindFallback(t *testing.T) {
	root := configEnvironment(t)
	for _, name := range []string{"dcompose.toml", "agent.toml"} {
		want := filepath.Join(root, "user", "dcompose", name)
		if path, err := Find("", name); nil != err || want != path {
			t.Fatalf("fallback: %q, %v", path, err)
		}
	}
}

func TestFindExplicitRelative(t *testing.T) {
	configEnvironment(t)
	want, err := filepath.Abs("missing.toml")
	if nil != err {
		t.Fatal(err)
	}
	if path, err := Find("missing.toml", "dcompose.toml"); nil != err || want != path {
		t.Fatalf("explicit relative path: %q, %v", path, err)
	}
}

func TestResolveRelative(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config/dcompose.toml")
	secret := filepath.Join(root, "config/secrets/password-hash")
	if err := Write(secret, []byte("hash\r\n")); nil != err {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if nil != err {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); nil != err {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); nil != err {
			t.Error(err)
		}
	})
	for _, value := range []string{"file:secrets/password-hash", "file:" + secret} {
		if got, err := Resolve(value, config); nil != err || "hash" != got {
			t.Fatalf("resolve %q: %q, %v", value, got, err)
		}
	}
}

func TestResolveValues(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "dcompose.toml")
	if got, err := Resolve("inline", config); nil != err || "inline" != got {
		t.Fatalf("inline: %q, %v", got, err)
	}
	for _, suffix := range []string{"", "\n", "\r", "\r\n"} {
		if err := Write(filepath.Join(root, "secret"), []byte(" value "+suffix)); nil != err {
			t.Fatal(err)
		}
		if got, err := Resolve("file:secret", config); nil != err || " value " != got {
			t.Fatalf("newline %q: %q, %v", suffix, got, err)
		}
	}
	for _, value := range []string{"file:", "file:missing"} {
		if _, err := Resolve(value, config); nil == err {
			t.Fatalf("accepted %q", value)
		}
	}
	if err := Write(filepath.Join(root, "empty"), []byte("\n")); nil != err {
		t.Fatal(err)
	}
	if _, err := Resolve("file:empty", config); nil == err {
		t.Fatal("accepted empty secret")
	}
}

func TestPath(t *testing.T) {
	if got := Path("/config/dcompose.toml", "secrets/hash"); "/config/secrets/hash" != got {
		t.Fatal(got)
	}
	if got := Path("/config/dcompose.toml", "/secrets/hash"); "/secrets/hash" != got {
		t.Fatal(got)
	}
	if got := Path("/config/dcompose.toml", ""); "" != got {
		t.Fatal(got)
	}
}

func TestWritePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested/config.toml")
	for _, data := range []string{"before", strings.Repeat("after", 1000)} {
		if err := Write(path, []byte(data)); nil != err {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if nil != err || data != string(got) {
			t.Fatalf("written data: %v", err)
		}
		info, err := os.Stat(path)
		if nil != err || 0600 != info.Mode().Perm() {
			t.Fatalf("private mode: %v", err)
		}
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".config.toml-*"))
	if nil != err || 0 != len(files) {
		t.Fatalf("temporary files remain: %v, %v", files, err)
	}
}
