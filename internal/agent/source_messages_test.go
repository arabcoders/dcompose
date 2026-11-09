package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChannelSetupMessages(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			logs := captureAutomaticLogs(t)
			a, _ := fixture(t)
			r := httptest.NewRequest("GET", "/channel", nil)
			r.RemoteAddr = "192.0.2.42:4321"
			r.Header.Set("Authorization", "Bearer channel-secret")
			if canceled {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			a.channel(w, r)
			var record struct {
				Level, Peer, Error string
				Message            string `json:"msg"`
			}
			if err := json.Unmarshal([]byte(logs.text()), &record); nil != err {
				t.Fatal(err)
			}
			level := "ERROR"
			if canceled {
				level = "DEBUG"
			}
			if level != record.Level || r.RemoteAddr != record.Peer || "" == record.Error || 400 > w.Code {
				t.Fatalf("connection fields: %+v, HTTP %d", record, w.Code)
			}
			for _, detail := range []string{record.Peer, record.Error} {
				if !strings.Contains(record.Message, detail) {
					t.Fatalf("connection message hides %q: %s", detail, record.Message)
				}
			}
			if strings.Contains(logs.text(), "channel-secret") {
				t.Fatal("connection credentials leaked")
			}
		})
	}
}

func TestEventFailureMessage(t *testing.T) {
	logs := captureAutomaticLogs(t)
	a, fake := fixture(t)
	write(t, filepath.Join(fake, "docker"), "#!/bin/sh\necho 'permission denied for event socket' >&2\nexit 8\n")
	a.listenRuntime(2 * time.Second)
	var record struct {
		Level, Runtime, Error, Stderr string
		Message                       string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(logs.text()), &record); nil != err {
		t.Fatal(err)
	}
	if "WARN" != record.Level || "docker" != record.Runtime || "exit status 8" != record.Error || "permission denied for event socket" != record.Stderr {
		t.Fatalf("event fields: %+v", record)
	}
	for _, detail := range []string{record.Runtime, record.Error, record.Stderr, "2s"} {
		if !strings.Contains(record.Message, detail) {
			t.Fatalf("event message hides %q: %s", detail, record.Message)
		}
	}
}

func TestShellInputMessage(t *testing.T) {
	logs := captureAutomaticLogs(t)
	file, err := os.CreateTemp(t.TempDir(), "shell-terminal")
	if nil != err {
		t.Fatal(err)
	}
	if err := file.Close(); nil != err {
		t.Fatal(err)
	}
	s := &shellSession{op: &operation{ID: "shell-42", Stack: "demo"}, container: "demo-web", terminal: file}
	if err := s.writeInput("private-shell-input"); nil == err {
		t.Fatal("closed terminal accepted input")
	}
	var record struct {
		Level, Shell, Stack, Container, Error string
		Message                               string `json:"msg"`
	}
	if err := json.Unmarshal([]byte(logs.text()), &record); nil != err {
		t.Fatal(err)
	}
	if "ERROR" != record.Level || s.op.ID != record.Shell || s.op.Stack != record.Stack || s.container != record.Container || "" == record.Error {
		t.Fatalf("shell input fields: %+v", record)
	}
	for _, detail := range []string{record.Shell, record.Stack, record.Container, record.Error} {
		if !strings.Contains(record.Message, detail) {
			t.Fatalf("shell input message hides %q: %s", detail, record.Message)
		}
	}
	if strings.Contains(logs.text(), "private-shell-input") {
		t.Fatal("shell input leaked")
	}
}
