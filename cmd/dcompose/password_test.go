package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestPasswordInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "test-password", "test-password"},
		{"lf", "test-password\n", "test-password"},
		{"crlf", "test-password\r\n", "test-password"},
		{"spaces", " test-password \n", " test-password "},
		{"empty", "\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, output, err := os.Pipe()
			if nil != err {
				t.Fatal(err)
			}
			defer input.Close()
			if _, err := output.WriteString(tc.input); nil != err {
				t.Fatal(err)
			}
			_ = output.Close()
			var prompts bytes.Buffer
			got, err := readPassword(input, &prompts, true)
			if "" == tc.want {
				if nil == err || "" != got {
					t.Fatal("expected empty password error", err)
				}
			} else if nil != err || tc.want != got {
				t.Fatalf("got %q, %v", got, err)
			}
			if 0 != prompts.Len() {
				t.Fatal("piped input must not prompt")
			}
		})
	}
}

func TestPasswordPrompt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		confirm bool
		repeat  string
		wantErr bool
	}{
		{"match", true, "test-password", false},
		{"mismatch", true, "different-password", true},
		{"hash", false, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			master, slave, err := pty.Open()
			if nil != err {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			prompts, output, err := os.Pipe()
			if nil != err {
				t.Fatal(err)
			}
			defer prompts.Close()
			defer output.Close()

			driven := make(chan error, 1)
			go func() {
				for i := 0; 2 > i; i++ {
					if 1 == i && !tc.confirm {
						break
					}
					for {
						var prompt [256]byte
						n, err := prompts.Read(prompt[:])
						if nil != err {
							driven <- err
							return
						}
						if "" != strings.TrimSpace(string(prompt[:n])) {
							break
						}
					}
					password := "test-password"
					if 1 == i {
						password = tc.repeat
					}
					if _, err := fmt.Fprintln(master, password); nil != err {
						driven <- err
						return
					}
				}
				driven <- nil
			}()

			done := make(chan error, 1)
			go func() {
				password, err := readPassword(slave, output, tc.confirm)
				if tc.wantErr {
					if nil == err || "" != password {
						done <- fmt.Errorf("mismatch returned %q, %v", password, err)
						return
					}
				} else if nil != err || "test-password" != password {
					done <- fmt.Errorf("got %q, %v", password, err)
					return
				}
				done <- nil
			}()
			select {
			case err := <-done:
				if nil != err {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("password prompt timed out")
			}
			if err := <-driven; nil != err {
				t.Fatal(err)
			}
		})
	}
}
