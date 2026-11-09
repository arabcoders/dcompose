package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

func TestNodeInfo(t *testing.T) {
	infoFixture := readFixture(t, "info.json")
	statsFixture := readFixture(t, "stats.jsonl")
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	calls := 0
	d := New(func(got context.Context, cmd engine.Command) ([]byte, []byte, error) {
		if ctx != got || "docker" != cmd.Program || "" != cmd.Dir || 0 != len(cmd.Env) {
			t.Fatalf("command: %+v", cmd)
		}
		calls++
		args, output := []string{"info", "--format", "{{json .}}"}, infoFixture
		if 2 == calls {
			args, output = []string{"stats", "--no-stream", "--no-trunc", "--format", "{{json .}}"}, statsFixture
		}
		if !reflect.DeepEqual(args, cmd.Args) {
			t.Fatalf("args: %v", cmd.Args)
		}
		return []byte(output), []byte("warning must not be decoded"), nil
	})
	info, err := d.NodeInfo(ctx)
	want := engine.RuntimeInfo{
		Name: "docker-home", Version: "27.5.1", OperatingSystem: "Ubuntu 24.04.1 LTS", Architecture: "x86_64",
		KernelVersion: "6.8.0-51-generic", CPUs: 8, MemoryBytes: 16777216000, Containers: 5,
		ContainersRunning: 2, ContainersStopped: 2, ContainersPaused: 1, Images: 12, StorageDriver: "overlay2",
	}
	if nil != err || 2 != calls || want != info.Runtime || 2 != len(info.Containers) {
		t.Fatalf("info: %+v, calls: %d, error: %v", info, calls, err)
	}
	wantStats := engine.ContainerStats{
		ID: strings.Repeat("a", 64), Name: "demo-web-1", CPU: "2.50%", Memory: "12MiB / 1GiB", MemoryPercent: "1.17%",
		NetworkIO: "1.2MB / 300kB", BlockIO: "8.19kB / 0B", PIDs: "4",
	}
	if wantStats != info.Containers[0] || "other-worker-1" != info.Containers[1].Name {
		t.Fatalf("stats: %+v", info.Containers)
	}
	encoded, err := json.Marshal(info)
	if nil != err || strings.Contains(string(encoded), "do-not-expose") || strings.Contains(string(encoded), "DockerRootDir") {
		t.Fatalf("encoded: %s, error: %v", encoded, err)
	}
	if !strings.Contains(string(encoded), `"runtime":`) || strings.Contains(string(encoded), `"docker":`) {
		t.Fatalf("runtime contract: %s", encoded)
	}
}

func TestNodeInfoIdle(t *testing.T) {
	infoFixture := readFixture(t, "info.json")
	calls := 0
	d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		calls++
		if "info" != cmd.Args[0] {
			t.Fatalf("idle node ran stats: %+v", cmd)
		}
		return []byte(strings.Replace(infoFixture, `"ContainersRunning":2`, `"ContainersRunning":0`, 1)), nil, nil
	})
	info, err := d.NodeInfo(context.Background())
	if nil != err || 1 != calls || nil == info.Containers || 0 != len(info.Containers) {
		t.Fatalf("idle info: %+v, calls: %d, error: %v", info, calls, err)
	}
}

func TestNodeInfoErrors(t *testing.T) {
	infoFixture := readFixture(t, "info.json")
	statsFixture := readFixture(t, "stats.jsonl")
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	for _, test := range []struct {
		name, info, stats, fail string
	}{
		{"info command", infoFixture, statsFixture, "info"},
		{"stats command", infoFixture, statsFixture, "stats"},
		{"info syntax", "secret-output", statsFixture, ""},
		{"info null", "null", statsFixture, ""},
		{"info array", "[]", statsFixture, ""},
		{"info empty", "{}", statsFixture, ""},
		{"info type", strings.Replace(infoFixture, `"NCPU":8`, `"NCPU":"secret-output"`, 1), statsFixture, ""},
		{"info trailing", infoFixture + `{}`, statsFixture, ""},
		{"stats syntax", infoFixture, "secret-output", ""},
		{"stats null", infoFixture, "null", ""},
		{"stats array", infoFixture, "[]", ""},
		{"stats empty", infoFixture, "{}", ""},
		{"stats type", infoFixture, `{"ID":"id","Name":"web","PIDs":4}`, ""},
		{"stats trailing", infoFixture, statsFixture + "secret-output", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			d := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if test.fail == cmd.Args[0] {
					return []byte("secret-output"), []byte("permission denied for /var/run/docker.sock"), errors.New("exit status 1")
				}
				if "info" == cmd.Args[0] {
					return []byte(test.info), nil, nil
				}
				return []byte(test.stats), nil, nil
			})
			_, err := d.NodeInfo(context.Background())
			var apiErr *engine.Error
			if !errors.As(err, &apiErr) || 502 != apiErr.Code || "" == apiErr.Message || strings.Contains(apiErr.Message, "secret-output") || strings.Contains(apiErr.Message, "/var/run/docker.sock") {
				t.Fatalf("error: %v", err)
			}
			var record struct {
				Level, Runtime, Command, Error, Stderr string
				Message                                string `json:"msg"`
			}
			if err := json.Unmarshal(logs.Bytes(), &record); nil != err {
				t.Fatalf("decode logs: %v", err)
			}
			command := "docker info"
			if strings.HasPrefix(test.name, "stats ") {
				command = "docker stats"
			}
			if "ERROR" != record.Level || "docker" != record.Runtime || command != record.Command || "" == record.Error || strings.Contains(logs.String(), "secret-") {
				t.Fatalf("logs: %s", &logs)
			}
			if "" != test.fail && ("exit status 1" != record.Error || "permission denied for /var/run/docker.sock" != record.Stderr) {
				t.Fatalf("missing Docker failure details: %s", &logs)
			}
			details := []string{command, record.Stderr}
			if "" != test.fail {
				details = append(details, "exit status 1")
			}
			for _, detail := range details {
				if "" != detail && !strings.Contains(record.Message, detail) {
					t.Fatalf("message hides diagnostic %q: %s", detail, record.Message)
				}
			}
		})
	}
}
