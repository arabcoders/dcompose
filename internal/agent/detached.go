package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/arabcoders/dcompose/internal/engine"
)

type detachedRequest struct {
	ID        string       `json:"id"`
	Engine    string       `json:"engine,omitempty"`
	Stack     engine.Stack `json:"stack"`
	Action    string       `json:"action"`
	Service   string       `json:"service"`
	Automatic bool         `json:"automatic,omitempty"`
	ConfigDir string       `json:"config_dir"`
	CreatedAt time.Time    `json:"created_at"`
}

func (a *agent) detachedOperation(name, action string) (bool, error) {
	if "pull" == action || nil == a.detachedExecutor {
		return false, nil
	}
	if nil != a.selfErr {
		return false, &engine.Error{Code: 503, Message: "Could not identify the node container. Check the DCOMPOSE_AGENT=true label or run this action from the host.", Cause: a.selfErr}
	}
	return nil != a.selfContainer && name == a.selfContainer.Project, nil
}

func (a *agent) initDetached(cacheDir string) {
	executor, ok := a.engine.(engine.DetachedOperations)
	if !ok {
		return
	}
	a.detachedExecutor = executor
	a.selfContainer, a.selfErr = executor.SelfContainer(a.ctx)
	if nil != a.selfErr {
		slog.Warn(fmt.Sprintf("Could not identify this node's container: %v. Lifecycle actions are disabled.", a.selfErr), "error", a.selfErr)
	}
	if nil != a.selfContainer {
		self := *a.selfContainer
		self.Mounts = append([]engine.ContainerMount(nil), self.Mounts...)
		a.selfContainer = &self
	}
	if "" == cacheDir {
		return
	}
	a.operationsDir = filepath.Join(cacheDir, "dcompose", "operations")
	entries, err := os.ReadDir(a.operationsDir)
	if os.IsNotExist(err) {
		return
	}
	if nil != err {
		slog.Warn(fmt.Sprintf("Could not restore detached operations from %q: %v", a.operationsDir, err), "error", err)
		return
	}
	requests := []detachedRequest{}
	for _, entry := range entries {
		if _, err := os.Stat(filepath.Join(a.operationsDir, entry.Name(), "collected")); nil == err {
			continue
		}
		requestFile := filepath.Join(a.operationsDir, entry.Name(), "request.json")
		if !entry.IsDir() {
			slog.Warn(fmt.Sprintf("Ignoring detached operation entry %q because it is not a directory.", filepath.Join(a.operationsDir, entry.Name())), "path", filepath.Join(a.operationsDir, entry.Name()))
			continue
		}
		request, err := readDetachedRequest(requestFile)
		if nil != err {
			slog.Warn(fmt.Sprintf("Could not restore detached operation from request %q: %v", requestFile, err), "path", requestFile, "error", err)
			continue
		}
		requests = append(requests, request)
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].CreatedAt.Before(requests[j].CreatedAt) })
	for _, request := range requests {
		op := &operation{ID: request.ID, Stack: request.Stack.Name, Action: request.Action, Status: "running", automatic: request.Automatic, service: request.Service}
		result, err := readDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op)
		if nil == err {
			_, _ = op.Write([]byte(result.Output))
			op.Status, op.ExitCode = result.Status, result.ExitCode
		} else if !os.IsNotExist(err) {
			slog.Warn(fmt.Sprintf("Could not restore the result of detached %s operation %s on stack %q from %q: %v", op.Action, op.ID, op.Stack, filepath.Join(a.operationsDir, op.ID, "result.json"), err), "operation", op.ID, "error", err)
		}
		a.ops[op.ID] = op
		a.order = append(a.order, op.ID)
		if "running" == op.Status {
			a.busy[op.Stack] = true
			a.active++
			a.setStackBusy(op.Stack, true)
		}
	}
	for _, id := range a.order {
		op := a.ops[id]
		if "running" != op.Status {
			if _, err := os.Stat(filepath.Join(a.operationsDir, id, "collected")); nil == err {
				continue
			}
			a.logOperationResult(op)
		}
		a.work.Go(func() {
			a.observeDetached(op)
		})
	}
}

func readDetachedRequest(path string) (detachedRequest, error) {
	var request detachedRequest
	data, err := os.ReadFile(path)
	if nil != err {
		return request, err
	}
	if err := json.Unmarshal(data, &request); nil != err {
		return request, err
	}
	id, err := hex.DecodeString(request.ID)
	if nil != err || 8 != len(id) || request.ID != hex.EncodeToString(id) || filepath.Base(filepath.Dir(path)) != request.ID || !stackName.MatchString(request.Stack.Name) {
		return request, fmt.Errorf("invalid detached operation ID or stack name")
	}
	if "" != request.Engine && "docker" != request.Engine && "podman" != request.Engine {
		return request, fmt.Errorf("invalid detached operation engine")
	}
	switch request.Action {
	case "deploy", "start", "stop", "restart", "update", "down", "delete":
	default:
		return request, fmt.Errorf("invalid detached operation action")
	}
	return request, nil
}

func (a *agent) prepareDetached(op *operation, base engine.Stack, service string) error {
	if "" == a.operationsDir {
		return &engine.Error{Code: 503, Message: "Actions on this node's own stack require a writable persistent cache directory.", Cause: errors.New("node cache directory is unavailable")}
	}
	directory := filepath.Join(a.operationsDir, op.ID)
	if err := os.MkdirAll(a.operationsDir, 0700); nil != err {
		return &engine.Error{Code: 503, Message: "Could not prepare the action on this node's own stack.", Cause: err}
	}
	if err := os.Mkdir(directory, 0700); nil != err {
		return &engine.Error{Code: 503, Message: "Could not prepare the action on this node's own stack.", Cause: err}
	}
	configDir := filepath.Dir(a.registries.root)
	data, err := json.Marshal(detachedRequest{ID: op.ID, Engine: a.config.Global.Engine, Stack: base, Action: op.Action, Service: service, Automatic: op.automatic, ConfigDir: configDir, CreatedAt: time.Now().UTC()})
	if nil == err {
		err = configutil.Write(filepath.Join(directory, "request.json"), data)
	}
	if nil != err {
		return &engine.Error{Code: 503, Message: "Could not prepare the action on this node's own stack.", Cause: err}
	}
	return nil
}

func (a *agent) runDetached(op *operation, base engine.Stack) {
	requestFile := filepath.Join(a.operationsDir, op.ID, "request.json")
	err := a.detachedExecutor.StartDetached(a.ctx, *a.selfContainer, "dagent-operation-"+op.ID, requestFile, base)
	if nil != a.ctx.Err() {
		// The runtime may have started the helper before replacing this node.
		return
	}
	if nil != err {
		a.failDetached(op, exitCode(err), fmt.Errorf("start action on this node's own stack: %w", err))
		return
	}
	a.observeDetached(op)
}

func readDetachedResult(path string, op *operation) (operationSnapshot, error) {
	var result operationSnapshot
	data, err := os.ReadFile(path)
	if nil != err {
		return result, err
	}
	if err := json.Unmarshal(data, &result); nil != err {
		return result, err
	}
	if result.ID != op.ID || result.Stack != op.Stack || result.Action != op.Action || nil == result.ExitCode || 1<<20 < len(result.Output) || ("succeeded" != result.Status && "failed" != result.Status) || (0 == *result.ExitCode) != ("succeeded" == result.Status) {
		return result, fmt.Errorf("invalid detached operation result")
	}
	return result, nil
}

func (a *agent) observeDetached(op *operation) {
	directory := filepath.Join(a.operationsDir, op.ID)
	helperName := "dagent-operation-" + op.ID
	var offset int64
	lastError := ""
	inspectError := ""
	terminal := "running" != op.snapshot().Status
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if nil != a.ctx.Err() {
			return
		}
		if !terminal {
			result, err := readDetachedResult(filepath.Join(directory, "result.json"), op)
			if nil == err {
				op.mu.Lock()
				// The result is authoritative, including a final incomplete UTF-8 rune.
				op.pendingUTF8 = ""
				if strings.HasPrefix(result.Output, op.Output) {
					op.appendOutput(result.Output[len(op.Output):])
				} else {
					op.Output = result.Output
				}
				op.mu.Unlock()
				a.finishOperation(op, *result.ExitCode)
				terminal = true
			} else {
				if !os.IsNotExist(err) && lastError != err.Error() {
					slog.Warn(fmt.Sprintf("Could not read the result of detached %s operation %s on stack %q from %q: %v", op.Action, op.ID, op.Stack, filepath.Join(directory, "result.json"), err), "operation", op.ID, "error", err)
					lastError = err.Error()
				}
				file, err := os.Open(filepath.Join(directory, "output.log"))
				if nil == err {
					if _, err = file.Seek(offset, io.SeekStart); nil == err {
						n, copyErr := io.Copy(op, io.LimitReader(file, (1<<20)-offset))
						offset += n
						err = copyErr
					}
					if closeErr := file.Close(); nil == err {
						err = closeErr
					}
				}
				if nil != err && !os.IsNotExist(err) && lastError != err.Error() {
					slog.Warn(fmt.Sprintf("Could not read output for detached %s operation %s on stack %q from %q: %v", op.Action, op.ID, op.Stack, filepath.Join(directory, "output.log"), err), "operation", op.ID, "error", err)
					lastError = err.Error()
				}
			}
		}
		state, err := a.detachedExecutor.DetachedState(a.ctx, helperName)
		if nil != a.ctx.Err() {
			return
		}
		if terminal && nil != err {
			// Completed helpers may already have been removed by the old node.
			slog.Debug(fmt.Sprintf("Could not inspect completed helper %q for detached %s operation %s on stack %q during cleanup: %v", helperName, op.Action, op.ID, op.Stack, err), "operation", op.ID, "error", err)
			return
		}
		if nil == err {
			inspectError = ""
		}
		if nil != err && (state.Running || (0 == state.ExitCode && "" == state.Output)) {
			if inspectError != err.Error() {
				slog.Warn(fmt.Sprintf("Could not inspect helper %q for detached %s operation %s on stack %q: %v. Waiting for its result.", helperName, op.Action, op.ID, op.Stack, err), "operation", op.ID, "error", err)
				inspectError = err.Error()
			}
		} else if !state.Running {
			if !terminal {
				// Check again after inspect: the helper writes its result before exiting.
				if _, err := readDetachedResult(filepath.Join(directory, "result.json"), op); nil == err {
					continue
				}
				if "" != state.Output {
					_, _ = fmt.Fprintf(op, "\n%s", state.Output)
				}
				cause := fmt.Errorf("action ended without a result (exit code %d)", state.ExitCode)
				if nil != err {
					cause = fmt.Errorf("%w: %v", cause, err)
				}
				code := state.ExitCode
				if 0 == code {
					code = 1
				}
				a.failDetached(op, code, cause)
			}
			// Never remove the only diagnostics unless a terminal result is durable.
			if _, err := readDetachedResult(filepath.Join(directory, "result.json"), op); nil == err {
				if err := a.detachedExecutor.RemoveDetached(a.ctx, helperName); nil != err && nil == a.ctx.Err() {
					slog.Warn(fmt.Sprintf("Could not remove helper container %q for detached %s operation %s on stack %q: %v", helperName, op.Action, op.ID, op.Stack, err), "operation", op.ID, "error", err)
				}
			}
			return
		}
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// A finished relay is recoverable until its consumer explicitly detaches after
// receiving a terminal snapshot or relay.done. Cleanup alone is not consumption.
func (a *agent) collectRelay(op *operation) {
	if "running" == op.snapshot().Status {
		return
	}
	path := filepath.Join(a.operationsDir, op.ID, "collected")
	if err := configutil.Write(path, nil); nil != err {
		slog.Warn(fmt.Sprintf("Could not mark relay %s for stack %q as consumed: %v", op.ID, op.Stack, err), "relay", op.ID, "stack", op.Stack, "error", err)
		return
	}
	a.mu.Lock()
	delete(a.ops, op.ID)
	for i, id := range a.order {
		if id == op.ID {
			a.order = append(a.order[:i], a.order[i+1:]...)
			break
		}
	}
	a.mu.Unlock()
}

func (a *agent) failDetached(op *operation, code int, err error) {
	_, _ = fmt.Fprintf(op, "\nError: %v\n", err)
	if writeErr := writeDetachedResult(filepath.Join(a.operationsDir, op.ID, "result.json"), op, code); nil != writeErr {
		slog.Error(fmt.Sprintf("Could not save the result of detached %s operation %s on stack %q to %q: %v", op.Action, op.ID, op.Stack, filepath.Join(a.operationsDir, op.ID, "result.json"), writeErr), "operation", op.ID, "error", writeErr)
	}
	a.finishOperation(op, code)
}

func writeDetachedResult(path string, op *operation, code int) error {
	op.mu.Lock()
	op.appendOutput(strings.ToValidUTF8(op.pendingUTF8, "\uFFFD"))
	op.pendingUTF8 = ""
	result := op.operationSnapshot
	op.mu.Unlock()
	result.ExitCode = &code
	result.Status = "succeeded"
	if 0 != code {
		result.Status = "failed"
	}
	data, err := json.Marshal(result)
	if nil != err {
		return err
	}
	return configutil.Write(path, data)
}

type detachedLog struct {
	file      *os.File
	remaining int
}

func (log *detachedLog) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > log.remaining {
		p = p[:log.remaining]
	}
	written, err := log.file.Write(p)
	log.remaining -= written
	if nil != err {
		return written, err
	}
	return n, nil
}

// RunOperation executes one private helper request without starting a node.
func RunOperation(ctx context.Context, requestFile string) error {
	request, err := readDetachedRequest(requestFile)
	if nil != err {
		return fmt.Errorf("read operation request: %w", err)
	}
	a := newAgent(ctx, config{Global: globalConfig{Engine: request.Engine, Paired: true}})
	if err := a.initRegistries(request.ConfigDir); nil != err {
		return err
	}
	op := &operation{ID: request.ID, Stack: request.Stack.Name, Action: request.Action, Status: "running", automatic: request.Automatic, service: request.Service}
	file, err := os.OpenFile(filepath.Join(filepath.Dir(requestFile), "output.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if nil != err {
		return fmt.Errorf("open operation output: %w", err)
	}
	op.output = io.MultiWriter(op, os.Stdout, &detachedLog{file: file, remaining: 1 << 20})
	runErr := a.runOperation(op, request.Stack.Dir, request.Stack, request.Service)
	if nil != runErr {
		_, err = fmt.Fprintf(op.output, "\nError: %v\n", runErr)
	}
	if closeErr := file.Close(); nil == err {
		err = closeErr
	}
	if nil != err {
		return fmt.Errorf("write operation output: %w", err)
	}
	if err := writeDetachedResult(filepath.Join(filepath.Dir(requestFile), "result.json"), op, exitCode(runErr)); nil != err {
		return fmt.Errorf("write operation result: %w", err)
	}
	return runErr
}
