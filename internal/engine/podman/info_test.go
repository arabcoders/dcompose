package podman

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
	// Native Podman 4.3 info schema and stats JSON from podman-stats(1).
	infoFixture := readFixture(t, "info.json")
	statsFixture := readFixture(t, "stats.json")
	ctx := context.WithValue(context.Background(), struct{}{}, "request")
	calls := 0
	p := New(func(got context.Context, cmd engine.Command) ([]byte, []byte, error) {
		if ctx != got || "podman" != cmd.Program || "" != cmd.Dir || 0 != len(cmd.Env) {
			t.Fatalf("command: %+v", cmd)
		}
		calls++
		args, output := []string{"info", "--format", "json"}, infoFixture
		if 2 == calls {
			args, output = []string{"stats", "--no-stream", "--no-trunc", "--format", "json"}, statsFixture
		}
		if !reflect.DeepEqual(args, cmd.Args) {
			t.Fatalf("args: %v", cmd.Args)
		}
		return []byte(output), []byte("warning must not be decoded"), nil
	})
	info, err := p.NodeInfo(ctx)
	want := engine.RuntimeInfo{
		Name: "podman-home", Version: "4.3.1", OperatingSystem: "debian 12", Architecture: "amd64",
		KernelVersion: "6.1.0-54-amd64", CPUs: 8, MemoryBytes: 16777216000, Containers: 5,
		ContainersRunning: 2, ContainersStopped: 2, ContainersPaused: 1, Images: 12, StorageDriver: "overlay",
	}
	if nil != err || 2 != calls || want != info.Runtime || 2 != len(info.Containers) {
		t.Fatalf("info: %+v, calls: %d, error: %v", info, calls, err)
	}
	wantStats := engine.ContainerStats{
		ID: strings.Repeat("a", 64), Name: "demo-web-1", CPU: "2.50%", Memory: "12MB / 1GB", MemoryPercent: "1.17%",
		NetworkIO: "-- / --", BlockIO: "8.19kB / 0B", PIDs: "4",
	}
	if wantStats != info.Containers[0] || "-- / --" != info.Containers[1].NetworkIO {
		t.Fatalf("stats: %+v", info.Containers)
	}
	encoded, err := json.Marshal(info)
	if nil != err || strings.Contains(string(encoded), "do-not-expose") || strings.Contains(string(encoded), "secret-storage") || !strings.Contains(string(encoded), `"runtime":`) {
		t.Fatalf("encoded: %s, error: %v", encoded, err)
	}
}

func TestStatsFields(t *testing.T) {
	infoFixture := readFixture(t, "info.json")
	for _, test := range []struct{ name, file, network, block string }{
		{"legacy", "stats.json", "-- / --", "8.19kB / 0B"},
		{"underscored", "stats-underscored.json", "1.92kB / 978B", "0B / 0B"},
	} {
		t.Run(test.name, func(t *testing.T) {
			statsFixture := readFixture(t, test.file)
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if "info" == cmd.Args[0] {
					return []byte(infoFixture), nil, nil
				}
				return []byte(statsFixture), nil, nil
			})
			info, err := p.NodeInfo(context.Background())
			if nil != err || 0 == len(info.Containers) {
				t.Fatalf("stats: %+v, error: %v", info, err)
			}
			if stat := info.Containers[0]; test.network != stat.NetworkIO || test.block != stat.BlockIO {
				t.Fatalf("stats: %+v", stat)
			}
		})
	}
}

func TestNodeInfoIdle(t *testing.T) {
	infoFixture := readFixture(t, "info.json")
	calls := 0
	p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
		calls++
		if "info" != cmd.Args[0] {
			t.Fatalf("idle node ran stats: %+v", cmd)
		}
		fixture := strings.Replace(infoFixture, `"running":2`, `"running":0`, 1)
		return []byte(strings.Replace(fixture, `"distribution":"debian","version":"12"`, `"distribution":"","version":""`, 1)), nil, nil
	})
	info, err := p.NodeInfo(context.Background())
	if nil != err || 1 != calls || nil == info.Containers || 0 != len(info.Containers) || "linux" != info.Runtime.OperatingSystem {
		t.Fatalf("idle info: %+v, calls: %d, error: %v", info, calls, err)
	}
}

func TestNodeInfoErrors(t *testing.T) {
	infoFixture := readFixture(t, "info.json")
	statsFixture := readFixture(t, "stats.json")
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	for _, test := range []struct{ name, info, stats, fail string }{
		{"info command", infoFixture, statsFixture, "info"},
		{"stats command", infoFixture, statsFixture, "stats"},
		{"info syntax", "secret-output", statsFixture, ""},
		{"info null", "null", statsFixture, ""},
		{"info empty", "{}", statsFixture, ""},
		{"info type", strings.Replace(infoFixture, `"cpus":8`, `"cpus":"secret-output"`, 1), statsFixture, ""},
		{"info trailing", infoFixture + `{}`, statsFixture, ""},
		{"stats syntax", infoFixture, "secret-output", ""},
		{"stats object", infoFixture, "{}", ""},
		{"stats empty item", infoFixture, "[{}]", ""},
		{"stats type", infoFixture, `[{"id":"id","name":"web","pids":4}]`, ""},
		{"stats trailing", infoFixture, statsFixture + "secret-output", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			p := New(func(_ context.Context, cmd engine.Command) ([]byte, []byte, error) {
				if test.fail == cmd.Args[0] {
					return []byte("secret-output"), []byte("permission denied for Podman socket"), errors.New("exit status 1")
				}
				if "info" == cmd.Args[0] {
					return []byte(test.info), nil, nil
				}
				return []byte(test.stats), nil, nil
			})
			_, err := p.NodeInfo(context.Background())
			var apiErr *engine.Error
			if !errors.As(err, &apiErr) || 502 != apiErr.Code || "" == apiErr.Message || strings.Contains(apiErr.Message, "secret-output") || strings.Contains(apiErr.Message, "permission denied for Podman socket") {
				t.Fatalf("error: %v", err)
			}
			var record struct {
				Level, Runtime, Command, Error, Stderr string
				Message                                string `json:"msg"`
			}
			if err := json.Unmarshal(logs.Bytes(), &record); nil != err {
				t.Fatalf("decode logs: %v", err)
			}
			command := "podman info"
			if strings.HasPrefix(test.name, "stats ") {
				command = "podman stats"
			}
			if "ERROR" != record.Level || "podman" != record.Runtime || command != record.Command || "" == record.Error || strings.Contains(logs.String(), "secret-") {
				t.Fatalf("logs: %s", &logs)
			}
			if "" != test.fail && (!strings.Contains(record.Error, "exit status 1") || !strings.Contains(record.Error+record.Stderr, "permission denied for Podman socket")) {
				t.Fatalf("missing failure details: %s", &logs)
			}
			if "stats" == test.fail && "permission denied for Podman socket" != record.Stderr {
				t.Fatalf("missing stats stderr: %s", &logs)
			}
			details := []string{command, record.Stderr}
			if "" != test.fail {
				details = append(details, "exit status 1", "permission denied for Podman socket")
			}
			for _, detail := range details {
				if "" != detail && !strings.Contains(record.Message, detail) {
					t.Fatalf("message hides diagnostic %q: %s", detail, record.Message)
				}
			}
		})
	}
}
