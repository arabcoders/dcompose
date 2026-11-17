//go:build !embed

package frontend

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestAssets(t *testing.T) {
	t.Chdir(t.TempDir())
	assets := Assets()
	if _, err := fs.ReadFile(assets, "index.html"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing generated assets: %v", err)
	}

	directory := filepath.Join("frontend", "exported")
	if err := os.MkdirAll(directory, 0o755); nil != err {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "index.html")
	for _, content := range []string{"<html>first</html>", "<html>updated</html>"} {
		if err := os.WriteFile(path, []byte(content), 0o644); nil != err {
			t.Fatal(err)
		}
		data, err := fs.ReadFile(assets, "index.html")
		if nil != err {
			t.Fatal(err)
		}
		if content != string(data) {
			t.Fatalf("generated assets: got %q, want %q", data, content)
		}
	}
}
