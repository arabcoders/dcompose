package agent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/engine/docker"
	"github.com/arabcoders/dcompose/internal/engine/podman"
	"github.com/arabcoders/dcompose/internal/protocol"
)

type apiError struct {
	code    int
	message string
}

func (e *apiError) Error() string { return e.message }
func fault(code int, message string) error {
	return &apiError{code, message}
}

type agent struct {
	ctx        context.Context
	config     config
	mu         sync.Mutex
	busy       map[string]bool
	active     int
	ops        map[string]*operation
	shells     map[string]*shellSession
	order      []string
	work       sync.WaitGroup
	events     *runtimeEvents
	registries *managedRegistries
	logs       *nodeLogBuffer
	engine     engine.Driver
	dns        hostCache

	detachedExecutor engine.DetachedOperations
	selfContainer    *engine.SelfContainer
	selfErr          error
	operationsDir    string

	startedAt    time.Time
	pairingToken string
}

func newAgent(ctx context.Context, c config) *agent {
	a := &agent{
		ctx: ctx, config: c, busy: make(map[string]bool), ops: make(map[string]*operation), shells: make(map[string]*shellSession),
		startedAt: time.Now().UTC(), events: newRuntimeEvents(), logs: &nodeLogBuffer{},
		dns: hostCache{entries: make(map[string]hostAddresses), lookup: net.DefaultResolver.LookupNetIP},
	}
	switch c.Global.Engine {
	case "podman":
		a.engine = podman.New(captureCommand)
	default:
		a.engine = docker.New(captureCommand)
	}
	if !c.Global.Paired {
		a.pairingToken = rand.Text()
	}
	return a
}

// any (the interface{} alias) is limited to JSON encoding of the concrete response structs below.
func respond(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}

func respondError(w http.ResponseWriter, r *http.Request, err error) {
	code := 500
	message := "Could not complete the request. Try again."
	if apiErr, ok := errors.AsType[*apiError](err); ok {
		code = apiErr.code
		message = apiErr.message
	} else if engineErr, ok := errors.AsType[*engine.Error](err); ok {
		code = engineErr.Code
		message = engineErr.Message
	}
	if 500 <= code || message != err.Error() {
		level := slog.LevelWarn
		if 500 <= code {
			level = slog.LevelError
		}
		if context.Canceled == r.Context().Err() {
			level = slog.LevelDebug
		}
		slog.Log(
			r.Context(),
			level,
			fmt.Sprintf("%s %s from peer %s failed (HTTP %d): %v", r.Method, r.URL.Path, r.RemoteAddr, code, err),
			"peer",
			r.RemoteAddr,
			"method",
			r.Method,
			"resource",
			r.URL.Path,
			"status",
			code,
			"error",
			err,
		)
	}
	respond(w, code, struct {
		Error string `json:"error"`
	}{message})
}

func decodeBody(r *http.Request, target any) error {
	// any (the interface{} alias) is the standard JSON decoder boundary; callers use typed request structs.
	decoder := json.NewDecoder(io.LimitReader(r.Body, (2<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); nil != err {
		slog.Warn(
			fmt.Sprintf("Could not decode %s %s from peer %s: %v", r.Method, r.URL.Path, r.RemoteAddr, err),
			"peer",
			r.RemoteAddr,
			"method",
			r.Method,
			"resource",
			r.URL.Path,
			"error",
			err,
		)
		return fault(400, "Invalid JSON request.")
	}
	var extra json.RawMessage
	if io.EOF != decoder.Decode(&extra) {
		return fault(400, "expected one JSON object")
	}
	return nil
}

func (a *agent) lockStack(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy[name] {
		return fault(409, "stack has an active operation")
	}
	if nil != a.ctx.Err() {
		return fault(409, "node is shutting down")
	}
	a.busy[name] = true
	return nil
}

func (a *agent) unlockStack(name string) {
	a.mu.Lock()
	delete(a.busy, name)
	a.mu.Unlock()
	a.signalRuntime(name)
}

type nodeHealth struct {
	Status             string    `json:"status"`
	Version            string    `json:"version"`
	APIVersion         int       `json:"api_version"`
	OS                 string    `json:"os"`
	Arch               string    `json:"arch"`
	StartedAt          time.Time `json:"started_at"`
	ImageCheckInterval string    `json:"image_check_interval"`
}

func (a *agent) health() nodeHealth {
	return nodeHealth{
		Status: "ok", Version: version, APIVersion: protocol.AgentAPI,
		OS: runtime.GOOS, Arch: runtime.GOARCH, StartedAt: a.startedAt,
		ImageCheckInterval: a.config.Global.ImageCheckInterval,
	}
}

func (a *agent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(protocol.Header, strconv.Itoa(protocol.AgentAPI))
	if "/pair" == r.URL.Path && http.MethodPost == r.Method {
		if err := a.pair(w, r); nil != err {
			respondError(w, r, err)
		}
		return
	}
	if !a.authorized(w, r) {
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if "/health" == r.URL.Path && http.MethodGet == r.Method {
		respond(w, 200, a.health())
		return
	}
	if supplied := r.Header.Values(
		protocol.Header,
	); 0 != len(supplied) &&
		(1 != len(supplied) || strconv.Itoa(protocol.AgentAPI) != supplied[0]) {
		respondError(w, r, fault(http.StatusUpgradeRequired, "Node version is incompatible. Update the node and server."))
		return
	}
	if "/unpair" == r.URL.Path && http.MethodPost == r.Method {
		if err := a.unpair(w, r); nil != err {
			respondError(w, r, err)
		}
		return
	}
	if "/channel" == r.URL.Path && http.MethodGet == r.Method {
		a.channel(w, r)
		return
	}
	if "/relays" == r.URL.Path && http.MethodGet == r.Method {
		a.listRelays(w)
		return
	}
	if 2 <= len(parts) && "shells" == parts[0] {
		if err := a.shellRequest(w, r, parts); nil != err {
			respondError(w, r, err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var err error
	if "/info" == r.URL.Path && http.MethodGet == r.Method {
		err = a.nodeInfo(ctx, w)
	} else if "/backup" == r.URL.Path && http.MethodGet == r.Method {
		err = a.backup(ctx, w)
	} else if "/stacks" == r.URL.Path && http.MethodGet == r.Method {
		err = a.listStacks(ctx, w)
	} else if "/images/inspect" == r.URL.Path && http.MethodPost == r.Method {
		err = a.inspectImages(ctx, w, r)
	} else if "/registry/request" == r.URL.Path && http.MethodPost == r.Method {
		err = a.registryRequest(ctx, w, r)
	} else if 2 <= len(parts) && "stacks" == parts[0] {
		name := parts[1]
		if !stackName.MatchString(name) {
			err = fault(400, "stack name must be 1-128 lowercase letters, digits, underscores or hyphens, starting with a letter or digit")
		} else if 2 == len(parts) {
			switch r.Method {
			case http.MethodGet:
				err = a.getStack(ctx, w, name)
			case http.MethodPost:
				err = a.createStack(ctx, w, r, name)
			case http.MethodPut:
				err = a.putStack(ctx, w, r, name)
			case http.MethodDelete:
				err = a.startOperation(ctx, w, name, "delete", "", false)
			default:
				err = fault(404, "route not found")
			}
		} else if 3 == len(parts) && "shells" == parts[2] && http.MethodPost == r.Method {
			err = a.createShell(ctx, w, r, name)
		} else if 3 == len(parts) && "actions" == parts[2] && http.MethodPost == r.Method {
			var input struct {
				Action    string `json:"action"`
				Service   string `json:"service"`
				Automatic bool   `json:"automatic,omitempty"`
			}
			if err = decodeBody(r, &input); nil == err {
				if "delete" == input.Action {
					err = fault(400, "use DELETE to delete a stack")
				} else {
					err = a.startOperation(ctx, w, name, input.Action, input.Service, input.Automatic)
				}
			}
		} else if 3 == len(parts) && "status" == parts[2] && http.MethodGet == r.Method {
			var state stackRuntime
			state, err = a.runtimeStatus(ctx, name)
			if nil == err {
				respond(w, 200, state)
			}
		} else {
			err = fault(404, "route not found")
		}
	} else {
		err = fault(404, "route not found")
	}
	if nil != err {
		respondError(w, r, err)
	}
}

func (a *agent) listStacks(ctx context.Context, w http.ResponseWriter) error {
	names, err := a.inventory()
	if nil != err {
		return err
	}
	type summary struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	stacks := make([]summary, len(names))
	jobs := make(chan int, len(names))
	for i, name := range names {
		stacks[i] = summary{name, "unknown"}
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; 4 > i && len(names) > i; i++ {
		workers.Go(func() {
			for index := range jobs {
				if state, err := a.runtimeStatus(ctx, names[index]); nil == err {
					stacks[index].Status = state.Status
				}
			}
		})
	}
	workers.Wait()
	respond(w, 200, struct {
		Stacks []summary `json:"stacks"`
	}{stacks})
	return nil
}

func (a *agent) getStack(ctx context.Context, w http.ResponseWriter, name string) error {
	dir, err := a.stackPath(name, false)
	if nil != err {
		return err
	}
	file, err := composeFile(dir)
	if nil != err {
		return err
	}
	env, err := managedFile(dir, ".env")
	if nil != err {
		return err
	}
	s := stack{Name: name, Status: "unknown", Services: []service{}}
	if s.Compose, err = readOptional(file); nil != err {
		return err
	}
	if s.Env, err = readOptional(env); nil != err {
		return err
	}
	if services, err := a.containers(ctx, name, dir); nil == err {
		s.Services = services
		s.Status = stackStatus(services)
	} else {
		if context.Canceled == ctx.Err() {
			slog.Debug(fmt.Sprintf("Container status request for stack %q was canceled: %v", name, err), "stack", name, "error", err)
		} else {
			slog.Error(fmt.Sprintf("Could not read container status for stack %q: %v", name, err), "stack", name, "error", err)
		}
	}
	respond(w, 200, s)
	return nil
}

func (a *agent) createStack(ctx context.Context, w http.ResponseWriter, r *http.Request, name string) error {
	var input struct {
		Compose   *string `json:"compose"`
		Env       string  `json:"env"`
		Overwrite bool    `json:"overwrite"`
	}
	if err := decodeBody(r, &input); nil != err {
		return err
	}
	if nil == input.Compose || "" == strings.TrimSpace(*input.Compose) {
		return fault(400, "compose must be a nonempty string")
	}
	if err := a.lockStack(name); nil != err {
		return err
	}
	defer a.unlockStack(name)
	if err := a.saveFiles(ctx, name, *input.Compose, input.Env, false, input.Overwrite); nil != err {
		return err
	}
	respond(w, 200, struct {
		Name string `json:"name"`
	}{name})
	return nil
}

func (a *agent) putStack(ctx context.Context, w http.ResponseWriter, r *http.Request, name string) error {
	var input struct {
		Compose *string `json:"compose"`
		Env     *string `json:"env"`
	}
	if err := decodeBody(r, &input); nil != err {
		return err
	}
	if nil == input.Compose || nil == input.Env || "" == strings.TrimSpace(*input.Compose) {
		return fault(400, "compose and env strings are required. The compose string cannot be empty")
	}
	if err := a.lockStack(name); nil != err {
		return err
	}
	defer a.unlockStack(name)
	if err := a.save(ctx, name, *input.Compose, *input.Env); nil != err {
		return err
	}
	respond(w, 200, struct {
		Name string `json:"name"`
	}{name})
	return nil
}

func exitCode(err error) int {
	if nil == err {
		return 0
	}
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) && 0 <= exit.ExitCode() {
		return exit.ExitCode()
	}
	return 1
}
