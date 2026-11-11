package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"syscall"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

func (a *agent) streamStats(ctx context.Context, name string, send func(streamEvent) bool) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if nil != ctx.Err() {
			return nil
		}
		dir, err := a.stackPath(name, false)
		var stack engine.Stack
		if nil == err {
			stack, err = a.engineStack(name, dir)
		}
		var containers []engine.ContainerStats
		if nil == err {
			sampleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			containers, err = a.engine.Stats(sampleCtx, stack)
			cancel()
		}
		if nil != ctx.Err() {
			return nil
		}
		if nil != err {
			code := 502
			if apiErr, ok := errors.AsType[*apiError](err); ok {
				code = apiErr.code
			}
			slog.Error(
				fmt.Sprintf(
					"Could not read %s resource statistics for stack %q on node %q: %s",
					a.config.Global.Engine,
					name,
					a.config.Global.NodeID,
					statsFailureCause(err),
				),
			)
			return fault(code, "stack statistics unavailable")
		}
		if nil == containers {
			containers = []engine.ContainerStats{}
		}
		if !send(event("stack.stats", struct {
			Containers []engine.ContainerStats `json:"containers"`
		}{containers})) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Compose stderr can contain interpolated secrets. Log typed system/parse causes
// rather than raw command output or unclassified runtime errors.
func statsFailureCause(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded.Error()
	}
	if cause, ok := errors.AsType[*exec.ExitError](err); ok {
		return cause.Error()
	}
	if cause, ok := errors.AsType[syscall.Errno](err); ok {
		return cause.Error()
	}
	if cause, ok := errors.AsType[*json.SyntaxError](err); ok {
		return cause.Error()
	}
	if cause, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return cause.Error()
	}
	if cause, ok := errors.AsType[*engine.Error](err); ok {
		return cause.Message
	}
	if cause, ok := errors.AsType[*apiError](err); ok {
		return cause.Error()
	}
	return "container engine request failed"
}
