package agent

import (
	"bufio"
	"context"
	"errors"
	"io"

	"github.com/arabcoders/dcompose/internal/engine"
)

func (a *agent) streamLogs(parent context.Context, name, container string, send func(streamEvent) bool) error {
	dir, err := a.stackPath(name, false)
	if nil != err {
		code := 502
		if apiErr, ok := errors.AsType[*apiError](err); ok {
			code = apiErr.code
		}
		return fault(code, "stack logs unavailable")
	}
	stack, err := a.engineStack(name, dir)
	if nil != err {
		code := 502
		if apiErr, ok := errors.AsType[*apiError](err); ok {
			code = apiErr.code
		}
		return fault(code, "stack logs unavailable")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var spec engine.Command
	if "" == container {
		spec = a.engine.Logs(stack)
	} else {
		// The driver resolves the container inside this stack and returns the
		// native engine logs command for it.
		filtered, err := a.engine.ContainerLogs(ctx, stack, container)
		if nil != err {
			code, message := 502, "container logs unavailable"
			if engineErr, ok := errors.AsType[*engine.Error](err); ok {
				code, message = engineErr.Code, engineErr.Message
			}
			return fault(code, message)
		}
		spec = filtered
	}
	reader, writer := io.Pipe()
	cmd := command(ctx, spec)
	cmd.Stdout, cmd.Stderr = writer, writer
	finished := make(chan struct{})
	var commandErr error
	go func() {
		commandErr = runCommand(ctx, cmd)
		_ = writer.Close()
		close(finished)
	}()
	defer func() {
		cancel()
		_ = reader.Close()
		<-finished
	}()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		if !send(event("message", struct {
			Message string `json:"message"`
		}{scanner.Text()})) {
			return nil
		}
	}
	if nil != ctx.Err() {
		return nil
	}
	if nil != scanner.Err() {
		cancel()
		_ = reader.Close()
	}
	<-finished
	if nil == cmd.Process {
		return fault(502, "stack logs unavailable")
	}
	code := exitCode(commandErr)
	if nil != scanner.Err() && 0 == code {
		code = 1
	}
	send(event("done", struct {
		ExitCode int `json:"exit_code"`
	}{code}))
	return nil
}
