package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cliMessage(t *testing.T, output []byte) string {
	t.Helper()
	line, _, _ := strings.Cut(string(output), "\n")
	parts := strings.SplitN(line, " ", 3)
	if 3 != len(parts) {
		t.Fatalf("missing log message: %s", output)
	}
	_, err := time.Parse(time.RFC3339Nano, parts[0])
	if nil != err {
		t.Fatal(err)
	}
	message := parts[2]
	for _, field := range []string{" config=", " request="} {
		message, _, _ = strings.Cut(message, field)
	}
	return message
}

func TestCLI(t *testing.T) {
	t.Setenv("DCOMPOSE_LOG_LEVEL", "")
	binary := filepath.Join(t.TempDir(), "dagent")
	build := exec.Command("go", "build", "-ldflags", "-X main.version=cli-version", "-o", binary, ".")
	if output, err := build.CombinedOutput(); nil != err {
		t.Fatalf("build: %v: %s", err, output)
	}
	for _, name := range []string{"explicit", "docker", "xdg_home", "xdg_dirs"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			xdgHome := filepath.Join(root, "home")
			xdgDirs := []string{filepath.Join(root, "first"), filepath.Join(root, "second")}
			docker := filepath.Join(root, "docker")
			t.Setenv("DCOMPOSE_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", xdgHome)
			t.Setenv("XDG_CONFIG_DIRS", strings.Join(xdgDirs, string(os.PathListSeparator)))
			files := map[string]string{
				filepath.Join(xdgHome, "dcompose", "agent.toml"):    "xdg_home",
				filepath.Join(xdgDirs[1], "dcompose", "agent.toml"): "xdg_dirs",
				filepath.Join(docker, "agent.toml"):                 "docker",
				filepath.Join(root, "explicit.toml"):                "explicit",
			}
			for path, marker := range files {
				if "xdg_dirs" == name && "xdg_home" == marker {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); nil != err {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, fmt.Appendf(nil, "[global]\nengine = %q\n", "fixture-"+marker), 0600); nil != err {
					t.Fatal(err)
				}
			}
			args := []string{}
			if "docker" == name || "explicit" == name {
				t.Setenv("DCOMPOSE_CONFIG_DIR", docker)
			}
			if "explicit" == name {
				args = append(args, "--config", filepath.Join(root, "explicit.toml"))
			}
			cmd := exec.Command(binary, args...)
			cmd.Dir = t.TempDir()
			output, err := cmd.CombinedOutput()
			if nil == err || !strings.Contains(string(output), " ERROR ") || !strings.Contains(string(output), "fixture-"+name) {
				t.Fatalf("config discovery: %v: %s", err, output)
			}
			message := cliMessage(t, output)
			for _, detail := range []string{"agent.toml", "fixture-" + name} {
				if "explicit" == name && "agent.toml" == detail {
					detail = "explicit.toml"
				}
				if !strings.Contains(message, detail) {
					t.Fatalf("startup message hides %q: %s", detail, message)
				}
			}
		})
	}
	t.Run("log-level", func(t *testing.T) {
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
				path := filepath.Join(t.TempDir(), "agent.toml")
				data := fmt.Sprintf("[global]\nengine = 'unsupported'\nunknown = true\nlog_level = %q\n", test.configured)
				if err := os.WriteFile(path, []byte(data), 0600); nil != err {
					t.Fatal(err)
				}
				args := []string{"--config", path}
				if "" != test.override {
					args = append(args, "--log-level", test.override)
				}
				cmd := exec.Command(binary, args...)
				output, err := cmd.CombinedOutput()
				if nil == err || !strings.Contains(string(output), " ERROR ") || !strings.Contains(string(output), "error=") || !strings.Contains(string(output), "config="+path) {
					t.Fatalf("startup error: %v: %s", err, output)
				}
				if test.warn != strings.Contains(string(output), " WARN ") {
					t.Fatalf("warning filter: %s", output)
				}
			})
		}
	})
	t.Run("helpers", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("DCOMPOSE_CONFIG_DIR", filepath.Join(root, "missing"))
		missing := filepath.Join(root, "missing.toml")
		cmd := exec.Command(binary, "--config", missing, "--version")
		if output, err := cmd.CombinedOutput(); nil != err || "dagent cli-version\n" != string(output) {
			t.Fatalf("version loaded configuration: %v: %s", err, output)
		}
		requestFile := filepath.Join(root, "missing", "request.json")
		cmd = exec.Command(binary, "--config", missing, "--run-operation", requestFile)
		output, err := cmd.CombinedOutput()
		if nil == err || !strings.Contains(string(output), " ERROR ") || !strings.Contains(string(output), "request="+requestFile) || strings.Contains(string(output), "config=") {
			t.Fatalf("operation helper loaded node configuration: %v: %s", err, output)
		}
		message := cliMessage(t, output)
		for _, detail := range []string{requestFile, "no such file or directory"} {
			if !strings.Contains(message, detail) {
				t.Fatalf("helper message hides %q: %s", detail, message)
			}
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("operation helper created node configuration", err)
		}
		token := filepath.Join(root, "generated", "token")
		certs := filepath.Join(root, "certs")
		cmd = exec.Command(binary, "--config", missing, "--log-level", "error", "--generate-token", token, "--generate-cert", certs)
		output, err = cmd.CombinedOutput()
		if nil != err || !strings.Contains(string(output), token) || !strings.Contains(string(output), filepath.Join(certs, "node.crt")) || !strings.Contains(string(output), filepath.Join(certs, "node.key")) {
			t.Fatalf("credential generation failed or filtered its output: %v: %s", err, output)
		}
		for _, path := range []string{token, filepath.Join(certs, "node.key"), filepath.Join(certs, "node.crt")} {
			if info, err := os.Stat(path); nil != err || 0 == info.Size() {
				t.Fatalf("credential file missing: %s: %v", path, err)
			}
		}
		pair, err := tls.LoadX509KeyPair(filepath.Join(certs, "node.crt"), filepath.Join(certs, "node.key"))
		if nil != err {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(pair.Certificate[0])
		if nil != err {
			t.Fatal(err)
		}
		hash := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
		if !strings.Contains(string(output), base64.StdEncoding.EncodeToString(hash[:])) {
			t.Fatal("missing generated public-key fingerprint", string(output))
		}
	})
}
