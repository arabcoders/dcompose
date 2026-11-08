package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const shellIdle = 5 * time.Minute

type shellSession struct {
	mu           sync.Mutex
	op           *operation
	container    string
	shell        string
	terminal     *os.File
	resizePTY    *os.File
	lastActivity time.Time
	closing      bool
	cleanupErr   error
	cleanupMu    sync.Mutex
	closeOnce    sync.Once
	close        chan struct{}
	ready        chan struct{}
	done         chan struct{}
}

func (s *shellSession) requestClose() {
	s.closeOnce.Do(func() { close(s.close) })
}

func (s *shellSession) touch() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
}

func (s *shellSession) idle(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return shellIdle <= now.Sub(s.lastActivity)
}

func (a *agent) resolveShellContainer(ctx context.Context, stack, reference string) (string, error) {
	dir, err := a.stackPath(stack, false)
	if nil != err {
		return "", err
	}
	s, err := a.engineStack(stack, dir)
	if nil != err {
		return "", err
	}
	return a.engine.ResolveContainer(ctx, s, reference)
}

func (a *agent) createShell(ctx context.Context, w http.ResponseWriter, r *http.Request, stack string) error {
	shell, cols, rows := "sh", 120, 30
	input := struct {
		Container string  `json:"container"`
		Shell     *string `json:"shell"`
		Cols      *int    `json:"cols"`
		Rows      *int    `json:"rows"`
	}{Shell: &shell, Cols: &cols, Rows: &rows}
	if err := decodeBody(r, &input); nil != err {
		return err
	}
	if nil == input.Shell || nil == input.Cols || nil == input.Rows || ("sh" != *input.Shell && "bash" != *input.Shell && "ash" != *input.Shell && "dash" != *input.Shell) || !shellSize(*input.Cols, *input.Rows) {
		return fault(400, "shell must be sh, bash, ash or dash. The cols and rows values must each be between 1 and 500")
	}
	container, err := a.resolveShellContainer(ctx, stack, input.Container)
	if nil != err {
		if context.Canceled == ctx.Err() {
			slog.Debug(fmt.Sprintf("Shell lookup for container %q in stack %q requested by %s was canceled: %v", input.Container, stack, r.RemoteAddr, err), "container", input.Container, "stack", stack, "peer", r.RemoteAddr, "error", err)
		} else {
			slog.Error(fmt.Sprintf("Could not resolve shell container %q in stack %q requested by %s: %v", input.Container, stack, r.RemoteAddr, err), "container", input.Container, "stack", stack, "peer", r.RemoteAddr, "error", err)
		}
		return err
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); nil != err {
		return err
	}
	id := hex.EncodeToString(random[:])
	s := &shellSession{
		op:        &operation{operationSnapshot: operationSnapshot{ID: id, Stack: stack, Action: "shell", Status: "running"}, retainTail: true},
		container: container, shell: *input.Shell, lastActivity: time.Now(),
		close: make(chan struct{}), ready: make(chan struct{}), done: make(chan struct{}),
	}
	a.mu.Lock()
	if nil != a.ctx.Err() {
		a.mu.Unlock()
		return fault(409, "node is shutting down")
	}
	if 16 <= len(a.shells) {
		for id, old := range a.shells {
			select {
			case <-old.done:
				old.mu.Lock()
				if nil == old.cleanupErr {
					delete(a.shells, id)
				}
				old.mu.Unlock()
			default:
			}
		}
	}
	if 16 <= len(a.shells) {
		a.mu.Unlock()
		return fault(409, "too many shell sessions")
	}
	a.shells[id] = s
	a.work.Add(1)
	a.mu.Unlock()
	go a.runShell(s, *input.Cols, *input.Rows)
	select {
	case <-s.ready:
		slog.Debug(fmt.Sprintf("Shell %s running %s in container %q of stack %q is ready for %s.", id, s.shell, container, stack, r.RemoteAddr), "shell", id, "stack", stack, "container", container, "command", s.shell, "peer", r.RemoteAddr)
		respond(w, 201, struct {
			ID        string `json:"id"`
			Stack     string `json:"stack"`
			Container string `json:"container"`
			Shell     string `json:"shell"`
		}{id, stack, container, *input.Shell})
		return nil
	case <-s.done:
		return fault(502, "shell did not become ready")
	case <-ctx.Done():
		s.requestClose()
		if context.Canceled == ctx.Err() {
			slog.Debug(fmt.Sprintf("Startup of shell %s in container %q of stack %q was canceled by DCompose peer %s: %v", id, container, stack, r.RemoteAddr, ctx.Err()), "peer", r.RemoteAddr, "shell", id, "stack", stack, "container", container, "error", ctx.Err())
		} else {
			slog.Error(fmt.Sprintf("Startup of shell %s in container %q of stack %q for %s timed out: %v", id, container, stack, r.RemoteAddr, ctx.Err()), "peer", r.RemoteAddr, "shell", id, "stack", stack, "container", container, "error", ctx.Err())
		}
		return fault(502, "shell startup canceled")
	}
}

func shellSize(cols, rows int) bool {
	return 1 <= cols && 500 >= cols && 1 <= rows && 500 >= rows
}

func (a *agent) shellRequest(w http.ResponseWriter, r *http.Request, parts []string) error {
	a.mu.Lock()
	s := a.shells[parts[1]]
	a.mu.Unlock()
	if nil == s {
		return fault(404, "shell not found")
	}
	if 2 == len(parts) && http.MethodDelete == r.Method {
		s.requestClose()
		select {
		case <-s.done:
			if err := a.retryShellCleanup(s); nil != err {
				return fault(502, "shell cleanup failed. Retry closing the shell when the container engine is available")
			}
			respond(w, 200, struct {
				Status string `json:"status"`
			}{"ok"})
			return nil
		case <-r.Context().Done():
			return r.Context().Err()
		}
	}
	return fault(404, "route not found")
}

func (s *shellSession) writeInput(data string) error {
	if 64<<10 < len(data) {
		return fault(400, "data must be a string of at most 64 KiB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || nil == s.terminal {
		return fault(409, "shell is closed")
	}
	s.lastActivity = time.Now()
	if err := s.terminal.SetWriteDeadline(time.Now().Add(2 * time.Second)); nil != err {
		slog.Error(fmt.Sprintf("Could not set the input deadline for shell %s in container %q of stack %q: %v", s.op.ID, s.container, s.op.Stack, err), "shell", s.op.ID, "stack", s.op.Stack, "container", s.container, "error", err)
		return fault(502, "shell input failed")
	}
	if _, err := io.WriteString(s.terminal, data); nil != err {
		slog.Error(fmt.Sprintf("Could not write input to shell %s in container %q of stack %q: %v", s.op.ID, s.container, s.op.Stack, err), "shell", s.op.ID, "stack", s.op.Stack, "container", s.container, "error", err)
		return fault(502, "shell input failed")
	}
	return nil
}

func (s *shellSession) resize(cols, rows int) error {
	if !shellSize(cols, rows) {
		return fault(400, "cols and rows must each be between 1 and 500")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || nil == s.terminal {
		return fault(409, "shell is closed")
	}
	s.lastActivity = time.Now()
	if err := pty.Setsize(s.resizePTY, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); nil != err {
		slog.Error(fmt.Sprintf("Could not resize shell %s in container %q of stack %q to %d columns by %d rows: %v", s.op.ID, s.container, s.op.Stack, cols, rows, err), "shell", s.op.ID, "stack", s.op.Stack, "container", s.container, "cols", cols, "rows", rows, "error", err)
		return fault(502, "shell resize failed")
	}
	return nil
}

// Reopen a nonblocking master so Go's poller can interrupt reads/writes on
// close and enforce input deadlines even if the container stops reading.
func shellTerminal() (*os.File, *os.File, error) {
	master, slave, err := pty.Open()
	if nil != err {
		return nil, nil, err
	}
	fd, err := syscall.Dup(int(master.Fd()))
	_ = master.Close()
	if nil == err {
		err = syscall.SetNonblock(fd, true)
	}
	if nil != err {
		if 0 <= fd {
			_ = syscall.Close(fd)
		}
		_ = slave.Close()
		return nil, nil, err
	}
	return os.NewFile(uintptr(fd), "shell-pty"), slave, nil
}

func (a *agent) cleanupShell(s *shellSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return a.engine.CleanupShell(ctx, s.container, s.op.ID)
}

func (a *agent) retryShellCleanup(s *shellSession) error {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	s.mu.Lock()
	err := s.cleanupErr
	s.mu.Unlock()
	if nil != err {
		err = a.cleanupShell(s)
		s.mu.Lock()
		s.cleanupErr = err
		s.mu.Unlock()
		if nil != err {
			slog.Error(fmt.Sprintf("Could not clean up processes for shell %s in container %q of stack %q after retrying: %v", s.op.ID, s.container, s.op.Stack, err), "shell", s.op.ID, "stack", s.op.Stack, "container", s.container, "error", err)
		} else {
			slog.Debug(fmt.Sprintf("Cleaned up processes for shell %s in container %q of stack %q after retrying.", s.op.ID, s.container, s.op.Stack), "shell", s.op.ID, "stack", s.op.Stack, "container", s.container)
		}
	}
	return err
}

func (a *agent) runShell(s *shellSession, cols, rows int) {
	defer a.work.Done()
	defer close(s.done)
	code := 1
	reason := "startup did not become ready within 5 seconds"
	defer func() {
		s.mu.Lock()
		s.closing = true
		cleanupErr := s.cleanupErr
		s.mu.Unlock()
		s.op.mu.Lock()
		s.op.appendOutput(strings.ToValidUTF8(s.op.pendingUTF8, "\uFFFD"))
		s.op.pendingUTF8 = ""
		s.op.ExitCode = &code
		s.op.Status = "failed"
		if 0 == code {
			s.op.Status = "succeeded"
		}
		s.op.publish(event("operation.done", s.op.operationSnapshot))
		s.op.mu.Unlock()
		if 0 == code {
			slog.Debug(fmt.Sprintf("Shell %s in container %q of stack %q stopped with exit code %d: %s", s.op.ID, s.container, s.op.Stack, code, reason), "shell", s.op.ID, "stack", s.op.Stack, "container", s.container, "exit_code", code, "reason", reason)
		} else if nil != a.ctx.Err() && nil == cleanupErr {
			slog.Debug(fmt.Sprintf("Shell %s in container %q of stack %q stopped during node shutdown with exit code %d: %s", s.op.ID, s.container, s.op.Stack, code, reason), "shell", s.op.ID, "stack", s.op.Stack, "container", s.container, "exit_code", code, "reason", reason)
		} else {
			slog.Error(fmt.Sprintf("Shell %s in container %q of stack %q stopped with exit code %d: %s", s.op.ID, s.container, s.op.Stack, code, reason), "shell", s.op.ID, "stack", s.op.Stack, "container", s.container, "exit_code", code, "reason", reason)
		}
	}()
	master, slave, err := shellTerminal()
	if nil != err {
		reason = "could not create the shell terminal: " + err.Error()
		return
	}
	defer master.Close()
	defer slave.Close()
	// pty.Setsize calls File.Fd(), which switches pollable files back to
	// blocking mode. Use the slave for resize; the master must stay pollable.
	if err := pty.Setsize(slave, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); nil != err {
		reason = "could not set the initial terminal size: " + err.Error()
		return
	}
	// Unlike noninteractive action commands, this CLI needs a controlling PTY.
	cmd := command(context.Background(), a.engine.Shell(s.container, s.shell, s.op.ID))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); nil != err {
		reason = "could not start the container shell process: " + err.Error()
		return
	}
	s.mu.Lock()
	s.terminal = master
	s.resizePTY = slave
	s.mu.Unlock()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		reader := bufio.NewReader(master)
		marker := "\x1e" + s.op.ID + "\x1f"
		for {
			line, err := reader.ReadSlice('\n')
			if nil != err {
				return
			}
			if marker == strings.TrimRight(string(line), "\r\n") {
				close(s.ready)
				_, _ = io.Copy(s.op, reader)
				return
			}
			// Runtime diagnostics are not exposed as shell output before readiness.
		}
	}()
	startup := time.NewTimer(5 * time.Second)
	defer startup.Stop()
	select {
	case <-s.ready:
	case err = <-waited:
		waited = nil
		reason = "the container shell process exited before becoming ready"
		if nil != err {
			reason += ": " + err.Error()
		} else {
			reason += " with exit status 0"
		}
	case <-startup.C:
	}
	if nil != waited {
		select {
		case <-s.ready:
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
		loop:
			for {
				select {
				case err = <-waited:
					waited = nil
					code = exitCode(err)
					reason = "the container shell process exited"
					if nil != err {
						reason += ": " + err.Error()
					}
					break loop
				case <-s.close:
					code = 0
					reason = "DCompose requested that the shell close"
					break loop
				case <-a.ctx.Done():
					reason = "the node is shutting down: " + a.ctx.Err().Error()
					break loop
				case now := <-ticker.C:
					if s.idle(now) {
						reason = "no shell input or resize was received for 5 minutes"
						break loop
					}
				}
			}
		default:
		}
	}
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	// Cleanup is required even on unexpected CLI exit: the shell can still be
	// running in the runtime. Its context deliberately outlives a canceled agent.
	cleanupErr := a.cleanupShell(s)
	s.mu.Lock()
	s.cleanupErr = cleanupErr
	s.mu.Unlock()
	if nil != cleanupErr {
		reason += ". Container process cleanup failed: " + cleanupErr.Error()
		code = 1
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if nil != waited {
		<-waited
	}
	_ = slave.Close()
	select {
	case <-readDone:
	case <-time.After(100 * time.Millisecond):
	}
	_ = master.Close()
	<-readDone
}
