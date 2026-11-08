package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

func TestShellInspectionMessage(t *testing.T) {
	for _, stderr := range []string{"", "permission denied for Docker socket"} {
		t.Run(stderr, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			d := New(func(context.Context, engine.Command) ([]byte, []byte, error) {
				return []byte("private-container-environment"), []byte(stderr), errors.New("exit status 1")
			})
			_, err := d.ResolveContainer(context.Background(), testStack(), "demo-web")
			var apiErr *engine.Error
			if !errors.As(err, &apiErr) || 404 != apiErr.Code || strings.Contains(apiErr.Message, "permission denied") {
				t.Fatalf("public error changed: %v", err)
			}
			var record struct {
				Level, Runtime, Stack, Container, Error, Stderr string
				Message                                         string `json:"msg"`
			}
			if err := json.Unmarshal(logs.Bytes(), &record); nil != err {
				t.Fatal(err)
			}
			if "ERROR" != record.Level || "docker" != record.Runtime || "demo" != record.Stack || "demo-web" != record.Container || "exit status 1" != record.Error || stderr != record.Stderr {
				t.Fatalf("inspection fields: %+v", record)
			}
			for _, detail := range []string{record.Stack, record.Container, record.Error, stderr} {
				if "" != detail && !strings.Contains(record.Message, detail) {
					t.Fatalf("inspection message hides %q: %s", detail, record.Message)
				}
			}
			if strings.Contains(logs.String(), "private-container-environment") {
				t.Fatal("container environment leaked")
			}
		})
	}
}
