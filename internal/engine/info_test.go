package engine_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

func TestNodeInfoCancel(t *testing.T) {
	t.Setenv("CONTAINER_HOST", "")
	for _, test := range []struct{ runtime, info string }{
		{"docker", `{"Name":"docker-home","ServerVersion":"1","ContainersRunning":1}`},
		{"podman", `{"host":{"hostname":"podman-home"},"version":{"Version":"1"},"store":{"containerStore":{"running":1}}}`},
	} {
		for _, stage := range []string{"info", "stats"} {
			t.Run(test.runtime+"/"+stage, func(t *testing.T) {
				var logs bytes.Buffer
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
				t.Cleanup(func() { slog.SetDefault(previous) })
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				driver := testDriver(test.runtime, func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
					if stage == cmd.Args[0] {
						cancel()
						return nil, nil, errors.New("signal: killed")
					}
					return []byte(test.info), nil, nil
				})
				_, err := driver.NodeInfo(ctx)
				if !errors.Is(err, context.Canceled) || 0 != logs.Len() {
					t.Fatalf("node info cancellation: %v; logs: %s", err, &logs)
				}
			})
		}
	}
}
