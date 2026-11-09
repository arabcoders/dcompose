package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/arabcoders/dcompose/internal/engine"
)

type operationLogRecord struct {
	Level     string `json:"level"`
	Message   string `json:"msg"`
	Operation string `json:"operation"`
	Action    string `json:"action"`
	Stack     string `json:"stack"`
	Service   string `json:"service"`
	Node      string `json:"node"`
	ExitCode  int    `json:"exit_code"`
}

func operationLogs(t *testing.T, logs *automaticLogBuffer) []operationLogRecord {
	t.Helper()
	var records []operationLogRecord
	for line := range strings.SplitSeq(strings.TrimSpace(logs.text()), "\n") {
		if "" == line {
			continue
		}
		var record operationLogRecord
		if err := json.Unmarshal([]byte(line), &record); nil != err {
			t.Fatal(err)
		}
		if "" != record.Action {
			records = append(records, record)
		}
	}
	return records
}

func checkFailureLog(t *testing.T, logs *automaticLogBuffer, a *agent, op *operation, cause string, code int) {
	t.Helper()
	records := operationLogs(t, logs)
	if 1 != len(records) {
		t.Fatalf("failure records: %+v", records)
	}
	record := records[0]
	if "ERROR" != record.Level || op.ID != record.Operation || op.Action != record.Action || op.Stack != record.Stack ||
		op.service != record.Service ||
		a.config.Global.NodeID != record.Node ||
		code != record.ExitCode {
		t.Fatalf("failure context: %+v", record)
	}
	for _, detail := range []string{op.ID, op.Action, op.Stack, op.service, a.config.Global.NodeID, fmt.Sprint(code), cause} {
		if !strings.Contains(record.Message, detail) {
			t.Fatalf("failure message hides %q: %s", detail, record.Message)
		}
	}
}

func TestManualFailureLogs(t *testing.T) {
	for _, action := range []string{"deploy", "start", "stop", "restart", "pull", "update", "down", "delete"} {
		t.Run(action, func(t *testing.T) {
			logs := captureAutomaticLogs(t)
			a, fake := executionFixture(t)
			a.config.Global.NodeID = "node-one"
			write(t, filepath.Join(fake, "ps"), `[{"ID":"one","Name":"web-1","Service":"web","State":"running"}]`)
			write(t, filepath.Join(fake, "fail"), "")
			service := ""
			if "start" == action || "stop" == action || "restart" == action || "update" == action {
				service = "web"
			}
			method, path := "POST", "/stacks/demo/actions"
			if "delete" == action {
				method, path = "DELETE", "/stacks/demo"
			}
			body, err := json.Marshal(struct {
				Action  string `json:"action"`
				Service string `json:"service"`
			}{action, service})
			if nil != err {
				t.Fatal(err)
			}
			result := completed(t, a, request(a, method, path, string(body)))
			a.work.Wait()
			if "failed" != result.Status || nil == result.ExitCode || 7 != *result.ExitCode {
				t.Fatalf("operation result: %+v", result)
			}
			checkFailureLog(t, logs, a, &operation{operationSnapshot: result, service: service}, "docker failed", 7)
		})
	}
}

func TestManualQuietLogs(t *testing.T) {
	logs := captureAutomaticLogs(t)
	a, _ := executionFixture(t)
	completed(t, a, request(a, "POST", "/stacks/demo/actions", `{"action":"restart","service":"web"}`))
	a.work.Wait()
	if w := request(a, "POST", "/stacks/demo/actions", `{"action":"restart","service":"missing"}`); 400 != w.Code {
		t.Fatalf("rejection status: %d", w.Code)
	}
	if records := operationLogs(t, logs); 0 != len(records) {
		t.Fatalf("unexpected operation logs: %+v", records)
	}
}

func TestManualDetachedLogs(t *testing.T) {
	for _, mode := range []string{"result", "launch", "missing"} {
		t.Run(mode, func(t *testing.T) {
			logs := captureAutomaticLogs(t)
			a, driver, _ := detachedFixture(t)
			a.config.Global.NodeID = "node-one"
			op := detachedOp(t, a, "0000000000000010")
			op.service = "web"
			cause, code := "registry.example: manifest unavailable", 7
			driver.state = func(context.Context, string) (engine.DetachedState, error) {
				return engine.DetachedState{ExitCode: code, Output: cause}, nil
			}
			switch mode {
			case "result":
				_, _ = fmt.Fprintf(op, "pulling\n\x1b[31m%s\x1b[0m\nError: exit status %d\n", cause, code)
				if err := writeDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op, code); nil != err {
					t.Fatal(err)
				}
				a.observeDetached(op)
			case "launch":
				code = 1
				driver.start = func(context.Context, engine.SelfContainer, string, string, engine.Stack) error {
					return errors.New(cause)
				}
				a.runDetached(op, engine.Stack{Name: "self"})
			case "missing":
				a.observeDetached(op)
			}
			checkFailureLog(t, logs, a, op, cause, code)
			if strings.Contains(logs.text(), `\u001b`) {
				t.Fatal("failure log contains terminal escapes")
			}
		})
	}
}

func TestManualRestoreLogs(t *testing.T) {
	logs := captureAutomaticLogs(t)
	a, driver, _ := detachedFixture(t)
	a.config.Global.NodeID = "node-one"
	op := detachedOp(t, a, "0000000000000011")
	cause := "registry.example: access denied"
	_, _ = op.Write([]byte(cause))
	if err := writeDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op, 7); nil != err {
		t.Fatal(err)
	}
	driver.state = func(context.Context, string) (engine.DetachedState, error) {
		return engine.DetachedState{ExitCode: 7}, nil
	}
	a.ops, a.busy, a.order = map[string]*operation{}, map[string]bool{}, nil
	a.initDetached(filepath.Dir(filepath.Dir(a.operationsDir)))
	a.work.Wait()
	checkFailureLog(t, logs, a, a.ops[op.ID], cause, 7)
	a.collectRelay(a.ops[op.ID])
	a.initDetached(filepath.Dir(filepath.Dir(a.operationsDir)))
	a.work.Wait()
	if 1 != len(operationLogs(t, logs)) || 0 != len(a.ops) {
		t.Fatal("consumed relay was restored")
	}
}

func TestManualLogBound(t *testing.T) {
	logs := captureAutomaticLogs(t)
	a, _ := executionFixture(t)
	cause := "registry.example: access denied"
	op := &operation{ID: "manual", Stack: "demo", Action: "pull", Status: "running"}
	_, _ = op.Write([]byte(strings.Repeat("progress €\r\n", 1000) + "\x1b[31m" + cause + "\x1b[0m\nError: exit status 7\n"))
	a.finishOperation(op, 7)
	checkFailureLog(t, logs, a, op, cause, 7)
	record := operationLogs(t, logs)[0]
	if 4500 < len(record.Message) || !utf8.ValidString(record.Message) || strings.ContainsAny(record.Message, "\r\n\x1b") {
		t.Fatalf("invalid bounded summary: length=%d", len(record.Message))
	}
}
