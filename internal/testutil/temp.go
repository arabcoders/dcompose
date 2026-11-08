// Package testutil contains shared test setup.
package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Run keeps temporary test files and subprocess output within one run directory.
func Run(m *testing.M) int {
	base := filepath.Join(os.TempDir(), "tests-dcompose")
	if err := os.MkdirAll(base, 0700); nil != err {
		fmt.Fprintf(os.Stderr, "Could not create the test directory: %v\n", err)
		return 1
	}
	dir, err := os.MkdirTemp(base, "")
	if nil != err {
		fmt.Fprintf(os.Stderr, "Could not create the test run directory: %v\n", err)
		return 1
	}
	if err := os.Setenv("TMPDIR", dir); nil != err {
		_ = os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "Could not set the test temporary directory: %v\n", err)
		return 1
	}
	code := m.Run()
	if err := os.RemoveAll(dir); nil != err {
		fmt.Fprintf(os.Stderr, "Could not remove the test run directory %s: %v\n", dir, err)
		return 1
	}
	return code
}
