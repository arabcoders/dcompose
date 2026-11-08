package server

import (
	"os"
	"testing"

	"github.com/arabcoders/dcompose/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.Run(m))
}
