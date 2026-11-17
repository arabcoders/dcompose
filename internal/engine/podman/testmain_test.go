package podman

import (
	"os"
	"testing"

	"github.com/arabcoders/dcompose/internal/testutil"
)

func TestMain(m *testing.M) {
	// Local command fixtures must not inherit the developer's remote endpoint.
	// Socket tests select their endpoint explicitly with t.Setenv.
	if err := os.Unsetenv("CONTAINER_HOST"); nil != err {
		panic(err)
	}
	os.Exit(testutil.Run(m))
}
