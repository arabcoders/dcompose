package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(Run(m))
}

func TestTempRoot(t *testing.T) {
	root := os.TempDir()
	if "tests-dcompose" != filepath.Base(filepath.Dir(root)) {
		t.Fatalf("test temporary root is outside tests-dcompose: %s", root)
	}
	if dir := t.TempDir(); !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
		t.Fatalf("test directory is outside the run root: %s", dir)
	}
}
