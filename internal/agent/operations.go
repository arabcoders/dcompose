package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/protocol"
)

type operationSnapshot struct {
	ID       string `json:"id"`
	Stack    string `json:"stack"`
	Action   string `json:"action"`
	Status   string `json:"status"`
	Output   string `json:"output"`
	ExitCode *int   `json:"exit_code"`
}

func (a *agent) listRelays(w http.ResponseWriter) {
	type summary struct {
		ID     string `json:"id"`
		Stack  string `json:"stack"`
		Action string `json:"action"`
	}
	operations := []summary{}
	a.mu.Lock()
	for _, id := range a.order {
		op := a.ops[id].snapshot()
		operations = append(operations, summary{op.ID, op.Stack, op.Action})
	}
	a.mu.Unlock()
	respond(w, 200, struct {
		Operations []summary `json:"relays"`
	}{operations})
}

type operation struct {
	mu sync.Mutex
	operationSnapshot
	subscribers map[*eventSubscriber]struct{}
	pendingUTF8 string
	retainTail  bool
	output      io.Writer
	automatic   bool
	service     string
}

func (o *operation) snapshot() operationSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.operationSnapshot
}

func (o *operation) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(p)
	data := o.pendingUTF8 + string(p)
	end := 0
	for len(data) > end && utf8.FullRuneInString(data[end:]) {
		_, size := utf8.DecodeRuneInString(data[end:])
		end += size
	}
	o.pendingUTF8 = data[end:]
	o.appendOutput(strings.ToValidUTF8(data[:end], "\uFFFD"))
	return n, nil
}

// Output and incremental event chunks share valid UTF-8 boundaries, including at
// the retained-output cap. Terminal writers may split a rune across writes.
func (o *operation) appendOutput(output string) {
	if o.retainTail {
		o.Output += output
		if 1<<20 < len(o.Output) {
			start := len(o.Output) - (1 << 20)
			for len(o.Output) > start && !utf8.RuneStart(o.Output[start]) {
				start++
			}
			o.Output = o.Output[start:]
		}
		if 0 < len(output) {
			o.publish(event("operation.output", struct {
				ID     string `json:"id"`
				Output string `json:"output"`
			}{o.ID, output}))
		}
		return
	}
	if remaining := (1 << 20) - len(o.Output); 0 < remaining {
		if len(output) > remaining {
			output = output[:remaining]
			for !utf8.ValidString(output) {
				output = output[:len(output)-1]
			}
		}
		o.Output += output
		if 0 < len(output) {
			o.publish(event("operation.output", struct {
				ID     string `json:"id"`
				Output string `json:"output"`
			}{o.ID, output}))
		}
	}
}

func (o *operation) publish(e streamEvent) {
	for s := range o.subscribers {
		s.send(e)
		select {
		case <-s.done:
			delete(o.subscribers, s)
		default:
		}
	}
}

func (op *operation) subscribe() (*eventSubscriber, []streamEvent) {
	s := newSubscriber()
	op.mu.Lock()
	if nil == op.subscribers {
		op.subscribers = make(map[*eventSubscriber]struct{})
	}
	op.subscribers[s] = struct{}{}
	snapshot := op.operationSnapshot
	initial := []streamEvent{event("operation.snapshot", snapshot)}
	if "running" != snapshot.Status {
		initial = append(initial, event("operation.done", snapshot))
	}
	op.mu.Unlock()
	return s, initial
}

func (op *operation) unsubscribe(s *eventSubscriber) {
	op.mu.Lock()
	delete(op.subscribers, s)
	op.mu.Unlock()
}

func (a *agent) startOperation(ctx context.Context, w http.ResponseWriter, name, action, service string, automatic bool) (err error) {
	id := ""
	defer func() {
		if !automatic || nil == err {
			return
		}
		code, reason := 500, "operation startup failed"
		var apiErr *apiError
		var engineErr *engine.Error
		if errors.As(err, &apiErr) {
			code, reason = apiErr.code, apiErr.message
		} else if errors.As(err, &engineErr) {
			code, reason = engineErr.Code, engineErr.Message
		}
		level := slog.LevelError
		if http.StatusConflict == code {
			level = slog.LevelDebug
		}
		slog.Log(ctx, level, fmt.Sprintf("Automatic update could not start for stack %q, service %q (HTTP %d): %s", name, service, code, reason), "automatic", true, "stack", name, "service", service, "operation", id, "status", "rejected", "http_status", code, "reason", reason)
	}()
	switch action {
	case "deploy", "start", "stop", "restart", "pull", "update", "down", "delete":
	default:
		return fault(400, "unsupported action")
	}
	if "" != service && ("start" != action && "stop" != action && "restart" != action && "update" != action) {
		return fault(400, "service is allowed only for start, stop, restart, and update")
	}
	detached, err := a.detachedOperation(name, action)
	if nil != err {
		return err
	}
	if err := a.lockStack(name); nil != err {
		return err
	}
	started := false
	defer func() {
		if !started {
			a.unlockStack(name)
		}
	}()
	dir, err := a.stackPath(name, false)
	if nil != err {
		return err
	}
	base, err := a.engineStack(name, dir)
	if nil != err {
		return err
	}
	if "" != service {
		if strings.HasPrefix(service, "-") || strings.ContainsAny(service, " \t\r\n\x00") {
			return fault(400, "invalid service name")
		}
		services, err := a.engine.Services(ctx, base)
		if nil != err {
			return err
		}
		found := false
		for _, candidate := range services {
			if service == candidate {
				found = true
				break
			}
		}
		if !found {
			return fault(400, "service is not defined in the Compose file")
		}
	}
	a.mu.Lock()
	if 16 <= a.active {
		a.mu.Unlock()
		return fault(409, "too many active operations")
	}
	a.active++
	a.mu.Unlock()
	defer func() {
		if !started {
			a.mu.Lock()
			a.active--
			a.mu.Unlock()
		}
	}()
	var op *operation
	if detached {
		var randomID [8]byte
		if _, err := rand.Read(randomID[:]); nil != err {
			return fmt.Errorf("relay ID: %w", err)
		}
		id = hex.EncodeToString(randomID[:])
		op = &operation{operationSnapshot: operationSnapshot{ID: id, Stack: name, Action: action, Status: "running"}, automatic: automatic, service: service}
		if err := a.prepareDetached(op, base, service); nil != err {
			return err
		}
		a.mu.Lock()
		a.ops[id] = op
		a.order = append(a.order, id)
		a.mu.Unlock()
	}
	a.work.Add(1)
	started = true
	a.setStackBusy(name, true)
	if detached {
		op.logAutomatic()
	} else {
		a.publishRuntime(event("stack.action", protocol.StackAction{Name: name, Action: action, Phase: "start"}))
		a.logAction(name, action, service, automatic, "running", nil, "")
	}
	respond(w, 202, struct {
		Status string `json:"status"`
		Relay  string `json:"relay,omitempty"`
	}{"accepted", id})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	go func() {
		defer a.work.Done()
		if detached {
			a.runDetached(op, base)
			return
		}
		output := &actionOutput{agent: a, name: name}
		err := a.runAction(action, dir, base, service, output)
		output.flush()
		code := exitCode(err)
		status, message := "succeeded", ""
		if nil != err {
			status = "failed"
			cause := err.Error()
			if detail := strings.Join(strings.Fields(operationANSI.ReplaceAllString(output.diagnostic, "")), " "); "" != detail {
				cause += ": " + detail
			}
			message = fmt.Sprintf("%s on stack %q", action, name)
			if "" != service {
				message += fmt.Sprintf(", service %q", service)
			}
			message += fmt.Sprintf(" failed (exit code %d): %s", code, cause)
		}
		a.logAction(name, action, service, automatic, status, &code, message)
		a.mu.Lock()
		a.active--
		delete(a.busy, name)
		a.setStackBusy(name, false)
		a.publishRuntime(event("stack.action", protocol.StackAction{Name: name, Action: action, Phase: "exit", ExitCode: &code, Message: message}))
		a.mu.Unlock()
		a.signalRuntime(name)
	}()
	return nil
}

func (a *agent) finishOperation(op *operation, code int) {
	a.mu.Lock()
	op.mu.Lock()
	op.appendOutput(strings.ToValidUTF8(op.pendingUTF8, "\uFFFD"))
	op.pendingUTF8 = ""
	op.ExitCode = &code
	op.Status = "succeeded"
	if 0 != code {
		op.Status = "failed"
	}
	op.publish(event("operation.done", op.operationSnapshot))
	op.mu.Unlock()
	delete(a.busy, op.Stack)
	if 0 < a.active {
		a.active--
	}
	a.setStackBusy(op.Stack, false)
	a.mu.Unlock()
	a.logOperationResult(op)
	a.signalRuntime(op.Stack)
}

var operationANSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

// Only an incomplete rune and a small, execution-local diagnostic tail survive
// each write. No command output is retained after this writer is discarded.
type actionOutput struct {
	agent      *agent
	name       string
	pending    string
	diagnostic string
}

func (w *actionOutput) Write(p []byte) (int, error) {
	data := w.pending + string(p)
	end := 0
	for len(data) > end && utf8.FullRuneInString(data[end:]) {
		_, size := utf8.DecodeRuneInString(data[end:])
		end += size
	}
	w.pending = data[end:]
	w.emit(strings.ToValidUTF8(data[:end], "\uFFFD"))
	return len(p), nil
}

func (w *actionOutput) emit(output string) {
	if "" == output {
		return
	}
	w.agent.publishRuntime(event("stack.output", protocol.StackOutput{Name: w.name, Output: output}))
	w.diagnostic += output
	if 4096 < len(w.diagnostic) {
		start := len(w.diagnostic) - 4096
		for !utf8.RuneStart(w.diagnostic[start]) {
			start++
		}
		w.diagnostic = w.diagnostic[start:]
	}
}

func (w *actionOutput) flush() {
	w.emit(strings.ToValidUTF8(w.pending, "\uFFFD"))
	w.pending = ""
}

func (a *agent) logAction(name, action, service string, automatic bool, status string, code *int, failure string) {
	if !automatic && "failed" != status {
		return
	}
	level := slog.LevelInfo
	message := fmt.Sprintf("Automatic %s %s for stack %q, service %q on node %q", action, status, name, service, a.config.Global.NodeID)
	if "failed" == status {
		level = slog.LevelError
		message = fmt.Sprintf("%s on node %q", failure, a.config.Global.NodeID)
	}
	attrs := []slog.Attr{slog.Bool("automatic", automatic), slog.String("action", action), slog.String("stack", name), slog.String("service", service), slog.String("node", a.config.Global.NodeID), slog.String("status", status)}
	if nil != code {
		attrs = append(attrs, slog.Int("exit_code", *code))
	}
	slog.LogAttrs(context.Background(), level, message, attrs...)
}

func (a *agent) logOperationResult(op *operation) {
	if op.automatic {
		op.logAutomatic()
		return
	}
	snapshot := op.snapshot()
	if "failed" != snapshot.Status {
		return
	}
	detail := strings.Join(strings.Fields(operationANSI.ReplaceAllString(snapshot.Output, "")), " ")
	if 4096 < len(detail) {
		start := len(detail) - 4096
		for !utf8.RuneStart(detail[start]) {
			start++
		}
		detail = "... " + detail[start:]
	}
	if "" == detail {
		detail = "no command output was captured"
	}
	message := fmt.Sprintf("Manual %s operation %s on stack %q", snapshot.Action, snapshot.ID, snapshot.Stack)
	if "" != op.service {
		message += fmt.Sprintf(", service %q", op.service)
	}
	if "" != a.config.Global.NodeID {
		message += fmt.Sprintf(", node %q", a.config.Global.NodeID)
	}
	message += fmt.Sprintf(" failed (exit code %d): %s", *snapshot.ExitCode, detail)
	slog.Error(message, "operation", snapshot.ID, "action", snapshot.Action, "stack", snapshot.Stack, "service", op.service, "node", a.config.Global.NodeID, "status", snapshot.Status, "exit_code", *snapshot.ExitCode)
}

func (op *operation) logAutomatic() {
	if !op.automatic {
		return
	}
	snapshot := op.snapshot()
	level := slog.LevelInfo
	outcome := snapshot.Status
	if "running" == outcome {
		outcome = "started"
	}
	message := fmt.Sprintf("Automatic update %s for stack %q, service %q", outcome, snapshot.Stack, op.service)
	if "failed" == snapshot.Status {
		level = slog.LevelError
		if nil != snapshot.ExitCode {
			message += fmt.Sprintf(" (exit code %d)", *snapshot.ExitCode)
		}
		if detail := strings.Join(strings.Fields(operationANSI.ReplaceAllString(snapshot.Output, "")), " "); "" != detail {
			if 4096 < len(detail) {
				start := len(detail) - 4096
				for !utf8.RuneStart(detail[start]) {
					start++
				}
				detail = detail[start:]
			}
			message += ": " + detail
		}
	}
	attrs := []slog.Attr{slog.Bool("automatic", true), slog.String("stack", snapshot.Stack), slog.String("service", op.service), slog.String("operation", snapshot.ID), slog.String("status", snapshot.Status)}
	if nil != snapshot.ExitCode {
		attrs = append(attrs, slog.Int("exit_code", *snapshot.ExitCode))
	}
	slog.LogAttrs(context.Background(), level, message+".", attrs...)
}

func (a *agent) runOperation(op *operation, dir string, base engine.Stack, service string) error {
	var output io.Writer = op
	if nil != op.output {
		output = op.output
	}
	return a.runAction(op.Action, dir, base, service, output)
}

func (a *agent) runAction(action, dir string, base engine.Stack, service string, output io.Writer) error {
	if "pull" == action || "update" == action || "deploy" == action {
		refs := a.stackImageRefs(base.Name, dir)
		a.invalidateImages(base.Name, refs)
		defer func() {
			for ref := range a.stackImageRefs(base.Name, dir) {
				refs[ref] = true
			}
			// Pull/up may change some images even when the command fails.
			a.invalidateImages(base.Name, refs)
		}()
	}
	run := func(action, service string) error {
		spec := a.engine.Action(base, action, service)
		spec.Env = append(spec.Env, "DOCKER_CONFIG="+a.pullConfigDir())
		cmd := command(a.ctx, spec)
		return runTerminalCommand(a.ctx, cmd, output)
	}
	switch action {
	case "start", "deploy":
		return run(action, service)
	case "stop", "restart":
		return run(action, service)
	case "pull", "down":
		return run(action, "")
	case "update":
		services, err := a.containers(a.ctx, base.Name, dir)
		if nil != err {
			return err
		}
		running := false
		for _, s := range services {
			if "running" == s.State && ("" == service || service == s.Service) {
				running = true
			}
		}
		if "" != service && !running {
			_, _ = output.Write([]byte("The selected service is not running. No containers were started.\n"))
			return nil
		}
		if err := run("pull", service); nil != err {
			return err
		}
		if running {
			for _, s := range services {
				if "running" != s.State || ("" != service && service != s.Service) {
					continue
				}
				changed, err := a.engine.ImageChanged(a.ctx, s)
				if nil != err {
					return err
				}
				if changed {
					if "" != service {
						return run("update", service)
					}
					return run("start", "")
				}
			}
			_, _ = output.Write([]byte("Running containers are already up to date. No containers were recreated.\n"))
			return nil
		}
		_, _ = output.Write([]byte("No containers were running. Images were pulled without starting containers.\n"))
		return nil
	case "delete":
		file, err := composeFile(dir)
		if nil != err {
			return err
		}
		for _, name := range composeNames {
			path, err := managedFile(dir, name)
			if nil != err {
				return err
			}
			if path == file {
				continue
			}
			if _, err := os.Stat(path); nil == err {
				return fault(409, "cannot delete a stack with multiple Compose files. Remove alternate Compose files first")
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		env, err := managedFile(dir, ".env")
		if nil != err {
			return err
		}
		if err := run("down", ""); nil != err {
			return err
		}
		for _, path := range []string{file, env} {
			if err := os.Remove(path); nil != err && !os.IsNotExist(err) {
				return err
			}
		}
		if err := os.Remove(dir); nil != err {
			if !errors.Is(err, syscall.ENOTEMPTY) {
				return err
			}
			_, _ = output.Write([]byte("Configuration was removed. Remaining stack directory contents were preserved.\n"))
		}
		return nil
	}
	return fmt.Errorf("unsupported action")
}
