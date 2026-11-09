package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/server"
)

func TestLogLevel(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dcompose")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); nil != err {
		t.Fatalf("build: %v: %s", err, output)
	}
	for _, test := range []struct {
		name, configured, environment, override string
		warn                                    bool
	}{
		{name: "toml", configured: "error"},
		{name: "environment", configured: "info", environment: "error"},
		{name: "flag", configured: "error", environment: "error", override: "info", warn: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DCOMPOSE_LOG_LEVEL", test.environment)
			path := filepath.Join(t.TempDir(), "dcompose.toml")
			data := fmt.Sprintf("[global]\nunknown = true\nlog_level = %q\n[auth]\nusername = 'admin'\npassword = 'invalid'\n", test.configured)
			if err := os.WriteFile(path, []byte(data), 0600); nil != err {
				t.Fatal(err)
			}
			args := []string{"--config", path}
			if "" != test.override {
				args = append(args, "--log-level", test.override)
			}
			output, err := exec.Command(binary, args...).CombinedOutput()
			if nil == err || !strings.Contains(string(output), " ERROR ") || !strings.Contains(string(output), "error=") || !strings.Contains(string(output), "config="+path) {
				t.Fatalf("startup error: %v: %s", err, output)
			}
			if test.warn != strings.Contains(string(output), " WARN ") {
				t.Fatalf("warning filter: %s", output)
			}
			for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
				parts := strings.SplitN(line, " ", 3)
				if 3 != len(parts) {
					t.Fatal("missing log message", line)
				}
				if _, err := time.Parse(time.RFC3339Nano, parts[0]); nil != err {
					t.Fatal("invalid log timestamp", line, err)
				}
				message, _, _ := strings.Cut(parts[2], " config=")
				detail := "auth.password"
				if "WARN" == parts[1] {
					detail = "global.unknown"
					message, _, _ = strings.Cut(message, " key=")
				}
				if !strings.Contains(message, path) || !strings.Contains(message, detail) {
					t.Fatal("startup message hides configuration context or cause", line)
				}
			}
		})
	}
}

func TestPasswordConfig(t *testing.T) {
	for _, name := range []string{"discovery", "explicit", "xdg"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("DCOMPOSE_CONFIG_DIR", filepath.Join(dir, "docker"))
			path := filepath.Join(dir, "docker", "dcompose.toml")
			args := []string{"dcompose", "--set-password", "--username", "alice", "--log-level", "error"}
			if "explicit" == name {
				path = filepath.Join(dir, "explicit", "server.toml")
				args = append(args, "--config", path)
			}
			if "xdg" == name {
				t.Setenv("DCOMPOSE_CONFIG_DIR", "")
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
				path = filepath.Join(dir, "xdg", "dcompose", "dcompose.toml")
				if err := server.SetPassword(path, "old", "old-password"); nil != err {
					t.Fatal(err)
				}
			}
			read, write, err := os.Pipe()
			if nil != err {
				t.Fatal(err)
			}
			if _, err := write.WriteString("test-password\r\n"); nil != err {
				t.Fatal(err)
			}
			_ = write.Close()
			defer func() { _ = read.Close() }()
			oldArgs, oldFlags, oldStdin := os.Args, flag.CommandLine, os.Stdin
			oldLogger := slog.Default()
			t.Cleanup(func() {
				os.Args, flag.CommandLine, os.Stdin = oldArgs, oldFlags, oldStdin
				slog.SetDefault(oldLogger)
			})
			os.Args, os.Stdin = args, read
			flag.CommandLine = flag.NewFlagSet("dcompose", flag.ContinueOnError)
			main()
			c, err := server.LoadConfig(path)
			if nil != err || "alice" != c.Auth.Username || "file:secrets/password.hash" != c.Auth.Password {
				t.Fatal("CLI did not persist credentials", err)
			}
		})
	}
}
