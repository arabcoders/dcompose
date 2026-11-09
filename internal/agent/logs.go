package agent

import (
	"bufio"
	"context"
	"errors"
	"io"
)

func (a *agent) streamLogs(parent context.Context, name string, send func(streamEvent) bool) error {
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
	reader, writer := io.Pipe()
	cmd := command(ctx, a.engine.Logs(stack))
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
