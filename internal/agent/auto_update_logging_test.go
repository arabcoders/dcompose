package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

type automaticLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *automaticLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}

func (b *automaticLogBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

type automaticLogRecord struct {
	Level     string `json:"level"`
	Message   string `json:"msg"`
	Automatic bool   `json:"automatic"`
	Stack     string `json:"stack"`
	Service   string `json:"service"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code"`
	HTTPCode  int    `json:"http_status"`
	Reason    string `json:"reason"`
}

func captureAutomaticLogs(t *testing.T) *automaticLogBuffer {
	t.Helper()
	var logs automaticLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func (b *automaticLogBuffer) records(t *testing.T) []automaticLogRecord {
	t.Helper()
	records := []automaticLogRecord{}
	for line := range strings.SplitSeq(strings.TrimSpace(b.text()), "\n") {
		if "" == line {
			continue
		}
		var record automaticLogRecord
		if err := json.Unmarshal([]byte(line), &record); nil != err {
			t.Fatal(err)
		}
		if record.Automatic {
			records = append(records, record)
		}
	}
	return records
}

func requestAutomaticUpdate(a *agent, target string) {
	payload, _ := json.Marshal(struct {
		Action    string `json:"action"`
		Service   string `json:"service"`
		Automatic bool   `json:"automatic"`
	}{"update", target, true})
	request(a, "POST", "/stacks/demo/actions", string(payload))
}

func TestAutoUpdateLogs(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			logs := captureAutomaticLogs(t)
			a, fake := executionFixture(t)
			write(t, filepath.Join(fake, "ps"), `[{"ID":"one","Name":"web-1","Service":"web","State":"running"}]`)
			if failed {
				write(t, filepath.Join(fake, "fail"), "")
			}
			requestAutomaticUpdate(a, "web")
			a.work.Wait()
			records := logs.records(t)
			if 2 != len(records) {
				t.Fatalf("lifecycle records: %+v", records)
			}
			for _, record := range records {
				if "demo" != record.Stack || "web" != record.Service || "" != record.Operation {
					t.Fatalf("lifecycle fields: %+v", record)
				}
				if !strings.Contains(record.Message, record.Stack) || !strings.Contains(record.Message, record.Service) {
					t.Fatalf("lifecycle message hides context: %+v", record)
				}
			}
			level, status, code := "INFO", "succeeded", 0
			if failed {
				level, status, code = "ERROR", "failed", 7
			}
			if "INFO" != records[0].Level || "running" != records[0].Status || nil != records[0].ExitCode || level != records[1].Level ||
				status != records[1].Status ||
				nil == records[1].ExitCode ||
				code != *records[1].ExitCode {
				t.Fatalf("lifecycle status: %+v", records)
			}
			if failed && !strings.Contains(records[1].Message, fmt.Sprint(code)) {
				t.Fatalf("failure message hides exit code: %+v", records[1])
			}
			if failed && !strings.Contains(records[1].Message, "docker failed") {
				t.Fatal("failure log omitted the command cause")
			}
		})
	}
}

func TestAutoUpdateStartLogs(t *testing.T) {
	for _, test := range []struct {
		name, level string
		code        int
		allocated   bool
	}{
		{"busy", "DEBUG", 409, false},
		{"service", "ERROR", 400, false},
		{"self", "ERROR", 503, false},
		{"prepare", "ERROR", 503, true},
		{"limit", "DEBUG", 409, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs := captureAutomaticLogs(t)
			a, _ := executionFixture(t)
			target := "web"
			switch test.name {
			case "busy":
				a.busy["demo"] = true
			case "service":
				target = "missing"
			case "self":
				a.detachedExecutor = &detachedDriver{}
				a.selfErr = errors.New("private engine diagnostic")
			case "prepare":
				a.detachedExecutor = &detachedDriver{}
				a.selfContainer = &engine.SelfContainer{Project: "demo"}
			case "limit":
				a.active = 16
			}
			_ = a.startOperation(a.ctx, httptest.NewRecorder(), "demo", "update", target, true)
			a.work.Wait()
			records := logs.records(t)
			if 1 != len(records) {
				t.Fatalf("startup records: %+v", records)
			}
			record := records[0]
			if test.level != record.Level || test.code != record.HTTPCode || "rejected" != record.Status || "demo" != record.Stack ||
				target != record.Service ||
				"" == record.Reason ||
				test.allocated != ("" != record.Operation) {
				t.Fatalf("startup fields: %+v", record)
			}
			for _, detail := range []string{record.Stack, record.Service, record.Reason, fmt.Sprint(test.code)} {
				if !strings.Contains(record.Message, detail) {
					t.Fatalf("startup message hides rejection context: %+v", record)
				}
			}
			if strings.Contains(logs.text(), "private engine diagnostic") {
				t.Fatal("startup logs include private engine diagnostics")
			}
		})
	}
}

func TestManualUpdateLogs(t *testing.T) {
	logs := captureAutomaticLogs(t)
	a, fake := executionFixture(t)
	write(t, filepath.Join(fake, "ps"), `[{"Name":"web-1","Service":"web","State":"running"}]`)
	completed(t, a, request(a, "POST", "/stacks/demo/actions", `{"action":"update","service":"web"}`))
	a.work.Wait()
	if err := a.startOperation(a.ctx, httptest.NewRecorder(), "demo", "update", "missing", false); nil == err {
		t.Fatal("manual rejection succeeded")
	}
	if records := logs.records(t); 0 != len(records) {
		t.Fatalf("manual update emitted automatic logs: %+v", records)
	}
}

func TestAutoDetachedLogs(t *testing.T) {
	for _, launchFailed := range []bool{false, true} {
		t.Run(fmt.Sprint(launchFailed), func(t *testing.T) {
			logs := captureAutomaticLogs(t)
			a, fake := executionFixture(t)
			write(t, filepath.Join(fake, "ps"), `[{"ID":"one","Name":"web-1","Service":"web","State":"running"}]`)
			driver := &detachedDriver{Driver: a.engine, self: &engine.SelfContainer{Project: "demo"}}
			driver.start = func(_ context.Context, _ engine.SelfContainer, _, path string, _ engine.Stack) error {
				input, err := readDetachedRequest(path)
				if nil != err {
					return err
				}
				if !input.Automatic || "web" != input.Service {
					return errors.New("lost automatic helper metadata")
				}
				if launchFailed {
					return errors.New("private launch output")
				}
				return RunOperation(a.ctx, path)
			}
			driver.state = func(context.Context, string) (engine.DetachedState, error) { return engine.DetachedState{}, nil }
			a.engine = driver
			a.initDetached(t.TempDir())
			requestAutomaticUpdate(a, "web")
			a.work.Wait()
			records := logs.records(t)
			status, level, code := "succeeded", "INFO", 0
			if launchFailed {
				status, level, code = "failed", "ERROR", 1
			}
			if 2 != len(records) || "running" != records[0].Status || status != records[1].Status || level != records[1].Level ||
				nil == records[1].ExitCode ||
				code != *records[1].ExitCode {
				t.Fatalf("detached lifecycle: %+v", records)
			}
			for _, record := range records {
				if "demo" != record.Stack || "web" != record.Service || a.order[0] != record.Operation {
					t.Fatalf("detached fields: %+v", record)
				}
			}
			result, err := readDetachedResult(filepath.Join(a.operationsDir, a.order[0], "result.json"), a.ops[a.order[0]])
			if nil != err || status != result.Status || code != *result.ExitCode {
				t.Fatalf("detached result: %+v %v", result, err)
			}
			if launchFailed && !strings.Contains(records[1].Message, "private launch output") {
				t.Fatal("automatic helper failure omitted the launch cause")
			}
		})
	}
}

func TestAutoDetachedRestoreLogs(t *testing.T) {
	for _, ready := range []bool{false, true} {
		for _, code := range []int{0, 7} {
			t.Run(fmt.Sprintf("%t/%d", ready, code), func(t *testing.T) {
				logs := captureAutomaticLogs(t)
				a, driver, _ := detachedFixture(t)
				op := &operation{
					ID:        "0123456789abcdef",
					Stack:     "self",
					Action:    "update",
					Status:    "running",
					automatic: true,
					service:   "web",
				}
				if err := a.prepareDetached(op, engine.Stack{Name: "self", Dir: "/stacks/self"}, "web"); nil != err {
					t.Fatal(err)
				}
				resultPath := filepath.Join(a.operationsDir, op.ID, "result.json")
				if ready {
					if err := writeDetachedResult(resultPath, op, code); nil != err {
						t.Fatal(err)
					}
				}
				driver.state = func(context.Context, string) (engine.DetachedState, error) {
					if !ready {
						return engine.DetachedState{}, writeDetachedResult(resultPath, op, code)
					}
					return engine.DetachedState{}, nil
				}
				a.initDetached(filepath.Dir(filepath.Dir(a.operationsDir)))
				a.work.Wait()
				restored := a.ops[op.ID]
				if !restored.automatic || "web" != restored.service {
					t.Fatal("restored operation lost automatic metadata")
				}
				records := logs.records(t)
				status, level := "succeeded", "INFO"
				if 0 != code {
					status, level = "failed", "ERROR"
				}
				if 1 != len(records) || status != records[0].Status || level != records[0].Level || "self" != records[0].Stack ||
					"web" != records[0].Service ||
					op.ID != records[0].Operation ||
					nil == records[0].ExitCode ||
					code != *records[0].ExitCode ||
					status != restored.snapshot().Status {
					t.Fatalf("restored lifecycle: %+v", records)
				}
				// Collected history is not a fresh automatic completion on restart.
				a.collectRelay(restored)
				a.ops, a.busy, a.order = map[string]*operation{}, map[string]bool{}, nil
				a.initDetached(filepath.Dir(filepath.Dir(a.operationsDir)))
				a.work.Wait()
				if records := logs.records(t); 1 != len(records) {
					t.Fatalf("collected result logged again: %+v", records)
				}
			})
		}
	}
}
