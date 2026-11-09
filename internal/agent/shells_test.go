package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/creack/pty"
)

func shellFixture(t *testing.T) (*agent, string) {
	t.Helper()
	a, fake := fixture(t)
	write(t, filepath.Join(fake, "docker"), readFixture(t, "shell.sh"))
	seed(t, a, "demo", "compose.yaml")
	write(t, filepath.Join(fake, "inspect"), shellInspect("demo", true))
	return a, fake
}

func shellInspect(stack string, running bool) string {
	return fmt.Sprintf(
		`[{"Id":%q,"State":{"Running":%t},"Config":{"Labels":{"com.docker.compose.project":%q}}}]`,
		strings.Repeat("a", 64),
		running,
		stack,
	)
}

func createdShell(t *testing.T, a *agent, body string) *shellSession {
	t.Helper()
	w := request(a, "POST", "/stacks/demo/shells", body)
	if 201 != w.Code {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var result struct {
		ID        string `json:"id"`
		Stack     string `json:"stack"`
		Container string `json:"container"`
		Shell     string `json:"shell"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); nil != err {
		t.Fatal(err)
	}
	if "demo" != result.Stack || strings.Repeat("a", 64) != result.Container || 16 != len(result.ID) {
		t.Fatalf("creation response: %s", w.Body)
	}
	a.mu.Lock()
	s := a.shells[result.ID]
	if nil != a.ops[result.ID] || a.busy["demo"] {
		t.Fatal("shell registered as stack operation")
	}
	a.mu.Unlock()
	return s
}

func shellInput(t *testing.T, _ *agent, s *shellSession, data string) {
	t.Helper()
	if err := s.writeInput(data); nil != err {
		t.Fatal(err)
	}
}

func TestShellTerminal(t *testing.T) {
	a, fake := shellFixture(t)
	s := createdShell(t, a, `{"container":"demo-web-1"}`)
	size, _ := os.ReadFile(filepath.Join(fake, "tty-size"))
	if "30 120" != strings.TrimSpace(string(size)) {
		t.Fatalf("initial PTY size: %q", size)
	}
	shellInput(t, a, s, "stty size\n")
	eventually(t, func() bool { return strings.Contains(s.op.snapshot().Output, "30 120") })
	if err := s.resize(90, 25); nil != err {
		t.Fatal(err)
	}
	shellInput(t, a, s, "stty size\n")
	eventually(t, func() bool { return strings.Contains(s.op.snapshot().Output, "25 90") })
	shellInput(t, a, s, "printf '\\342'; printf '\\202\\254\\n'\n")
	eventually(t, func() bool { return strings.Contains(s.op.snapshot().Output, "€") })
	if strings.Contains(s.op.snapshot().Output, "\x1e"+s.op.ID) {
		t.Fatal("readiness marker leaked")
	}
	argv, _ := os.ReadFile(filepath.Join(fake, "argv"))
	if !strings.Contains(
		string(argv),
		"<exec><-it><--detach-keys=><--env><DOCKER_NODE_SHELL_ID="+s.op.ID+"><--env><TERM=xterm-256color><"+s.container+"><sh><-c>",
	) {
		t.Fatalf("exec not pinned to ID: %s", argv)
	}
	for i := 0; 2 > i; i++ {
		w := request(a, "DELETE", "/shells/"+s.op.ID, "")
		if 200 != w.Code || !strings.Contains(w.Body.String(), `"status":"ok"`) {
			t.Fatalf("close: %d %s", w.Code, w.Body)
		}
	}
	if op := s.op.snapshot(); "succeeded" != op.Status || nil == op.ExitCode || 0 != *op.ExitCode {
		t.Fatalf("close snapshot: %+v", op)
	}
	if err := s.writeInput("x"); nil == err {
		t.Fatal("closed shell accepted input")
	}
}

func TestShellChoices(t *testing.T) {
	for _, shell := range []string{"sh", "bash", "ash", "dash"} {
		t.Run(shell, func(t *testing.T) {
			a, fake := shellFixture(t)
			if "sh" != shell {
				path := filepath.Join(fake, shell)
				write(t, path, "#!/bin/sh\nexec /bin/sh \"$@\"\n")
				if err := os.Chmod(path, 0700); nil != err {
					t.Fatal(err)
				}
			}
			s := createdShell(t, a, fmt.Sprintf(`{"container":"demo-web-1","shell":%q}`, shell))
			if shell != s.shell {
				t.Fatalf("shell: %q", s.shell)
			}
			argv, err := os.ReadFile(filepath.Join(fake, "argv"))
			if nil != err || !strings.Contains(string(argv), "<docker-node-shell><"+shell+">") {
				t.Fatalf("selected shell not passed literally: %s (%v)", argv, err)
			}
			shellInput(t, a, s, "printf 'shell-choice-ok\\n'\n")
			eventually(t, func() bool { return strings.Contains(s.op.snapshot().Output, "shell-choice-ok\r\n") })
			if w := request(a, "DELETE", "/shells/"+s.op.ID, ""); 200 != w.Code {
				t.Fatalf("close: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestShellValidation(t *testing.T) {
	a, fake := shellFixture(t)
	for _, body := range []string{
		`null`, `{}`, `{"container":"--help"}`, `{"container":"a;id"}`, `{"container":"a/b"}`,
		`{"container":"demo-web-1","shell":"zsh"}`, `{"container":"demo-web-1","shell":"sh -c id"}`,
		`{"container":"demo-web-1","cols":0}`, `{"container":"demo-web-1","rows":501}`,
		`{"container":"demo-web-1","shell":null}`, `{"container":"demo-web-1","cols":null}`,
		`{"container":"demo-web-1","extra":true}`, `{"container":"demo-web-1"}{}`,
	} {
		if w := request(a, "POST", "/stacks/demo/shells", body); 400 != w.Code {
			t.Fatalf("accepted %s: %d %s", body, w.Code, w.Body)
		}
	}
	write(t, filepath.Join(fake, "inspect"), shellInspect("other", true))
	if w := request(a, "POST", "/stacks/demo/shells", `{"container":"demo-web-1"}`); 403 != w.Code {
		t.Fatalf("ownership: %d %s", w.Code, w.Body)
	}
	write(t, filepath.Join(fake, "inspect"), shellInspect("demo", false))
	if w := request(a, "POST", "/stacks/demo/shells", `{"container":"demo-web-1"}`); 409 != w.Code {
		t.Fatalf("stopped: %d %s", w.Code, w.Body)
	}
	if w := request(a, "POST", "/stacks/missing/shells", `{"container":"demo-web-1"}`); 404 != w.Code {
		t.Fatalf("unmanaged stack: %d", w.Code)
	}
	write(t, filepath.Join(fake, "inspect"), shellInspect("demo", true))
	s := createdShell(t, a, `{"container":"demo-web-1","shell":"bash","cols":80,"rows":24}`)
	if err := s.writeInput(strings.Repeat("x", (64<<10)+1)); nil == err {
		t.Fatal("oversized input accepted")
	}
	for _, size := range [][2]int{{0, 30}, {120, 501}} {
		if err := s.resize(size[0], size[1]); nil == err {
			t.Fatalf("invalid size accepted: %v", size)
		}
	}
	shellInput(t, a, s, strings.Repeat("x", 64<<10))
	for _, path := range []string{"/shells/missing/events", "/operations/" + s.op.ID} {
		if w := request(a, "GET", path, ""); 404 != w.Code {
			t.Fatalf("missing: %s %d", path, w.Code)
		}
	}
}

func TestShellAuth(t *testing.T) {
	a, fake := shellFixture(t)
	for _, route := range []struct{ method, path string }{
		{"POST", "/stacks/demo/shells"}, {"GET", "/shells/id/events"},
		{"POST", "/shells/id/input"}, {"POST", "/shells/id/resize"}, {"DELETE", "/shells/id"},
		{"GET", "/operations"},
	} {
		r := httptest.NewRequest(route.method, route.path, strings.NewReader(`{"container":"web"}`))
		r.RemoteAddr = "127.0.0.1:42"
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if 401 != w.Code {
			t.Fatalf("unauthenticated %s: %d", route.path, w.Code)
		}
		r.Header.Set("Authorization", "Bearer test-secret")
		r.RemoteAddr = "192.0.2.1:42"
		w = httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if 403 != w.Code {
			t.Fatalf("untrusted peer %s: %d", route.path, w.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatal("unauthorized request invoked docker")
	}
}

func TestShellReconnect(t *testing.T) {
	a, _ := shellFixture(t)
	s := createdShell(t, a, `{"container":"web"}`)
	server := httptest.NewTLSServer(a)
	defer server.Close()
	for i := 0; 2 > i; i++ {
		conn := openChannel(t, server)
		channelSend(t, conn, protocol.ChannelMessage{Type: "shell.attach", Shell: s.op.ID})
		message := channelUntil(t, conn, "shell.event", "operation.snapshot")
		var snapshot operationSnapshot
		if err := json.Unmarshal(message.Payload, &snapshot); nil != err || s.op.ID != snapshot.ID || "shell" != snapshot.Action {
			t.Fatalf("invalid snapshot: %s %v", message.Payload, err)
		}
		if 1 == i && !strings.Contains(snapshot.Output, "reconnect-output") {
			t.Fatal("reconnect lost retained output")
		}
		_ = conn.CloseNow()
		shellInput(t, a, s, "printf 'reconnect-output\\n'\n")
		eventually(t, func() bool { return strings.Contains(s.op.snapshot().Output, "reconnect-output") })
		if "running" != s.op.snapshot().Status {
			t.Fatal("channel disconnect terminated shell")
		}
	}
	request(a, "DELETE", "/shells/"+s.op.ID, "")
	conn := openChannel(t, server)
	channelSend(t, conn, protocol.ChannelMessage{Type: "shell.attach", Shell: s.op.ID})
	channelUntil(t, conn, "shell.event", "operation.snapshot")
	channelUntil(t, conn, "shell.event", "operation.done")
}

func TestShellExpiry(t *testing.T) {
	a, fake := shellFixture(t)
	s := createdShell(t, a, `{"container":"web"}`)
	s.mu.Lock()
	s.lastActivity = time.Now().Add(-shellIdle - time.Second)
	s.mu.Unlock()
	eventually(t, func() bool {
		select {
		case <-s.done:
			return true
		default:
			return false
		}
	})
	if data, _ := os.ReadFile(filepath.Join(fake, "cleanups")); 0 == len(data) {
		t.Fatal("idle expiry skipped daemon cleanup")
	}
	if "failed" != s.op.snapshot().Status {
		t.Fatal("idle expiry did not mark the shell as failed")
	}
}

func TestShellShutdown(t *testing.T) {
	a, fake := shellFixture(t)
	ctx, cancel := context.WithCancel(a.ctx)
	a.ctx = ctx
	s := createdShell(t, a, `{"container":"web"}`)
	cancel()
	select {
	case <-s.done:
	case <-time.After(6 * time.Second):
		t.Fatal("shutdown blocked")
	}
	if data, _ := os.ReadFile(filepath.Join(fake, "cleanups")); 0 == len(data) || nil != s.cleanupErr {
		t.Fatal("canceled agent context prevented cleanup")
	}
	if w := request(a, "POST", "/stacks/demo/shells", `{"container":"web"}`); 409 != w.Code {
		t.Fatalf("shell accepted during shutdown: %d", w.Code)
	}
}

func TestShellCleanupTimeout(t *testing.T) {
	a, fake := shellFixture(t)
	ctx, cancel := context.WithCancel(a.ctx)
	a.ctx = ctx
	s := createdShell(t, a, `{"container":"web"}`)
	write(t, filepath.Join(fake, "cleanup-block"), "")
	cancel()
	select {
	case <-s.done:
	case <-time.After(6 * time.Second):
		t.Fatal("cleanup timeout did not bound shutdown")
	}
	if nil == s.cleanupErr || "failed" != s.op.snapshot().Status {
		t.Fatal("cleanup timeout hidden")
	}
}

func TestShellNaturalExit(t *testing.T) {
	logs := captureAutomaticLogs(t)
	a, _ := shellFixture(t)
	s := createdShell(t, a, `{"container":"web"}`)
	shellInput(t, a, s, "printf 'final-output\\n'; exit 7\n")
	eventually(t, func() bool { return "running" != s.op.snapshot().Status })
	if op := s.op.snapshot(); "failed" != op.Status || nil == op.ExitCode || 7 != *op.ExitCode ||
		!strings.Contains(op.Output, "final-output") {
		t.Fatalf("shell exit lost: %+v", op)
	}
	<-s.done
	var stopped bool
	for line := range strings.SplitSeq(strings.TrimSpace(logs.text()), "\n") {
		var record struct {
			Level, Shell, Stack, Container, Reason string
			Message                                string `json:"msg"`
			ExitCode                               int    `json:"exit_code"`
		}
		if err := json.Unmarshal([]byte(line), &record); nil != err {
			t.Fatal(err)
		}
		if "ERROR" != record.Level || s.op.ID != record.Shell {
			continue
		}
		stopped = true
		if s.op.Stack != record.Stack || s.container != record.Container || 7 != record.ExitCode {
			t.Fatalf("shell exit fields: %+v", record)
		}
		for _, detail := range []string{s.op.ID, s.op.Stack, s.container, strconv.Itoa(record.ExitCode), record.Reason} {
			if !strings.Contains(record.Message, detail) {
				t.Fatalf("shell exit message hides %q: %s", detail, record.Message)
			}
		}
	}
	if !stopped || strings.Contains(logs.text(), "final-output") {
		t.Fatal("shell exit log missing or includes terminal contents", logs.text())
	}
}

func TestShellInputDeadline(t *testing.T) {
	master, slave, err := shellTerminal()
	if nil != err {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	if err := pty.Setsize(slave, &pty.Winsize{Cols: 90, Rows: 30}); nil != err {
		t.Fatal(err)
	}
	cmd := exec.Command("stty", "raw", "-echo")
	cmd.Stdin = slave
	if err := cmd.Run(); nil != err {
		t.Fatal(err)
	}
	if err := master.SetWriteDeadline(time.Now().Add(50 * time.Millisecond)); nil != err {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := master.Write([]byte(strings.Repeat("x", 1<<20))); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("input deadline: %v", err)
		}
	case <-time.After(time.Second):
		_ = slave.Close()
		_ = <-done
		t.Fatal("blocked shell input ignored write deadline")
	}
}

func TestShellOutputTail(t *testing.T) {
	op := &operation{ID: "shell", Action: "shell", Status: "running", retainTail: true}
	s := newSubscriber()
	op.subscribers = map[*eventSubscriber]struct{}{s: {}}
	_, _ = op.Write([]byte("old-prefix" + strings.Repeat("€", (1<<20)/3)))
	<-s.events
	_, _ = op.Write([]byte("new-output"))
	select {
	case e := <-s.events:
		if "operation.output" != e.name || !strings.Contains(string(e.data), "new-output") {
			t.Fatalf("live output stopped at cap: %+v", e)
		}
	default:
		t.Fatal("live output stopped at cap")
	}
	output := op.snapshot().Output
	if 1<<20 < len(output) || !utf8.ValidString(output) || strings.HasPrefix(output, "old-prefix") ||
		!strings.HasSuffix(output, "new-output") {
		t.Fatal("shell snapshot is not bounded UTF-8 suffix")
	}
}

func TestShellAttachFailure(t *testing.T) {
	a, fake := shellFixture(t)
	write(t, filepath.Join(fake, "attach-fail"), "")
	if w := request(a, "POST", "/stacks/demo/shells", `{"container":"web"}`); 502 != w.Code {
		t.Fatalf("attach failure: %d %s", w.Code, w.Body)
	}
	pidText, _ := os.ReadFile(filepath.Join(fake, "detached-pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if 1 >= pid {
		t.Fatal("mock did not start detached exec")
	}
	eventually(t, func() bool { return processGone(pid) })
	if data, _ := os.ReadFile(filepath.Join(fake, "cleanups")); 0 == len(data) {
		t.Fatal("CLI failure skipped daemon cleanup")
	}
}

func processGone(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return os.IsNotExist(err) || strings.Contains(string(data), ") Z")
}

func TestShellCleanupRetry(t *testing.T) {
	a, fake := shellFixture(t)
	s := createdShell(t, a, `{"container":"web"}`)
	write(t, filepath.Join(fake, "cleanup-fail"), "")
	if w := request(a, "DELETE", "/shells/"+s.op.ID, ""); 502 != w.Code {
		t.Fatalf("cleanup failure hidden: %d %s", w.Code, w.Body)
	}
	_ = os.Remove(filepath.Join(fake, "cleanup-fail"))
	if w := request(a, "DELETE", "/shells/"+s.op.ID, ""); 200 != w.Code {
		t.Fatalf("cleanup retry: %d %s", w.Code, w.Body)
	}
}

func TestShellContainerGone(t *testing.T) {
	a, fake := shellFixture(t)
	s := createdShell(t, a, `{"container":"web"}`)
	write(t, filepath.Join(fake, "cleanup-fail"), "")
	write(t, filepath.Join(fake, "container-gone"), "")
	if w := request(a, "DELETE", "/shells/"+s.op.ID, ""); 200 != w.Code {
		t.Fatalf("stopped container cleanup: %d %s", w.Code, w.Body)
	}
}

func TestShellCleanupMarker(t *testing.T) {
	token := "docker-node-test-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	processes := []*exec.Cmd{}
	children := []int{}
	for i, marker := range []string{
		"DOCKER_NODE_SHELL_ID=" + token,
		"DOCKER_NODE_SHELL_ID=" + token + "-other",
		"OTHER=\nDOCKER_NODE_SHELL_ID=" + token + "\n",
	} {
		pidFile := filepath.Join(t.TempDir(), strconv.Itoa(i))
		cmd := exec.Command("sh", "-c", `sleep 300 & echo $! > "$1"; wait`, "marker-test", pidFile)
		cmd.Env = append(os.Environ(), marker)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); nil != err {
			t.Fatal(err)
		}
		processes = append(processes, cmd)
		t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
		eventually(t, func() bool { data, _ := os.ReadFile(pidFile); return 0 < len(data) })
		pidText, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(pidText)))
		children = append(children, pid)
	}
	a, _ := shellFixture(t)
	if err := a.engine.CleanupShell(context.Background(), "test-container", token); nil != err {
		t.Fatalf("cleanup: %v", err)
	}
	eventually(t, func() bool { return processGone(processes[0].Process.Pid) })
	eventually(t, func() bool { return processGone(children[0]) })
	for _, cmd := range processes[1:] {
		if processGone(cmd.Process.Pid) {
			t.Fatal("cleanup killed false marker match")
		}
	}
}

func TestShellLimit(t *testing.T) {
	a, _ := shellFixture(t)
	for i := 0; 16 > i; i++ {
		a.shells[strconv.Itoa(i)] = &shellSession{done: make(chan struct{})}
	}
	if w := request(a, "POST", "/stacks/demo/shells", `{"container":"web"}`); 409 != w.Code {
		t.Fatalf("unbounded sessions: %d", w.Code)
	}
	close(a.shells["0"].done)
	s := createdShell(t, a, `{"container":"web"}`)
	if nil == s || 16 != len(a.shells) {
		t.Fatal("completed session not evicted")
	}
}

func TestOperationSummaries(t *testing.T) {
	a, _ := fixture(t)
	seed(t, a, "demo", "compose.yaml")
	completed(t, a, request(a, "POST", "/stacks/demo/actions", `{"action":"pull"}`))
	if w := request(a, "GET", "/relays", ""); "{\"relays\":[]}\n" != w.Body.String() {
		t.Fatalf("ordinary action became a relay: %s", w.Body)
	}
}
