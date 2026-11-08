package configutil

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogLevel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		config    string
		env       string
		override  string
		want      slog.Level
		errSource string
	}{
		{name: "missing", want: slog.LevelInfo},
		{name: "omitted", config: "[global]\n", want: slog.LevelInfo},
		{name: "empty", config: "[global]\nlog_level = ''\n", want: slog.LevelInfo},
		{name: "toml", config: "[global]\nlog_level = 'debug'\n", want: slog.LevelDebug},
		{name: "env", config: "[global]\nlog_level = 'debug'\n", env: "warn", want: slog.LevelWarn},
		{name: "cli", config: "[global]\nlog_level = 'debug'\n", env: "warn", override: "error", want: slog.LevelError},
		{name: "invalid_cli", config: "[global]\nlog_level = 'debug'\n", env: "warn", override: "invalid", errSource: "--log-level"},
		{name: "invalid_env", config: "[global]\nlog_level = 'debug'\n", env: "invalid", errSource: "DCOMPOSE_LOG_LEVEL"},
		{name: "invalid_toml", config: "[global]\nlog_level = 'invalid'\n", errSource: "global.log_level"},
		{name: "env_ignores_invalid", config: "[global]\nlog_level = 'invalid'\n", env: "warn", want: slog.LevelWarn},
		{name: "cli_ignores_invalid", config: "[global]\nlog_level = 'invalid'\n", env: "invalid", override: "error", want: slog.LevelError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DCOMPOSE_LOG_LEVEL", tc.env)
			path := filepath.Join(t.TempDir(), "dcompose.toml")
			if "" != tc.config {
				if err := os.WriteFile(path, []byte(tc.config), 0600); nil != err {
					t.Fatal(err)
				}
			}

			got, err := LogLevel(path, tc.override)
			if "" != tc.errSource {
				if nil == err || !strings.Contains(err.Error(), tc.errSource) {
					t.Fatalf("expected error from %s, got %v", tc.errSource, err)
				}
				return
			}
			if nil != err || tc.want != got {
				t.Fatalf("level: %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
