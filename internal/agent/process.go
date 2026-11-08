package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/creack/pty"
)

func command(ctx context.Context, spec engine.Command) *exec.Cmd {
	cmd := exec.CommandContext(ctx, spec.Program, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// Kill the entire CLI process group, including children, on cancellation.
func runCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); nil != err {
		return err
	}
	return waitCommand(ctx, cmd)
}

func waitCommand(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	<-watchDone
	return err
}

// Give action output a terminal while leaving stdin non-interactive.
func runTerminalCommand(ctx context.Context, cmd *exec.Cmd, output io.Writer) error {
	master, slave, err := pty.Open()
	if nil != err {
		return err
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Rows: 30, Cols: 120}); nil != err {
		return err
	}
	cmd.Stdout, cmd.Stderr = slave, slave
	cmd.Env = append(cmd.Environ(), "TERM=xterm-256color")
	copied := make(chan error, 1)
	go func() { _, err := io.Copy(output, master); copied <- err }()
	err = runCommand(ctx, cmd)
	_ = slave.Close()
	copyErr := <-copied
	if nil != err {
		return err
	}
	// Linux signals the end of PTY output with EIO instead of EOF.
	if errors.Is(copyErr, syscall.EIO) {
		return nil
	}
	return copyErr
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := (1 << 20) - b.Len(); 0 < remaining {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

func captureCommand(ctx context.Context, spec engine.Command) ([]byte, []byte, error) {
	var stdout, stderr cappedBuffer
	cmd := command(ctx, spec)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runCommand(ctx, cmd)
	return stdout.Bytes(), stderr.Bytes(), err
}
