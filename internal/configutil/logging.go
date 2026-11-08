package configutil

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/BurntSushi/toml"
)

// LogLevel reads only the logging setting so startup diagnostics use the selected
// level before the full configuration is loaded. An empty path skips TOML.
func LogLevel(path, override string) (slog.Level, error) {
	value, source := override, "--log-level"
	if "" == value {
		value, source = os.Getenv("DCOMPOSE_LOG_LEVEL"), "DCOMPOSE_LOG_LEVEL"
	}
	if "" == value && "" != path {
		var config struct {
			Global struct {
				LogLevel string `toml:"log_level"`
			} `toml:"global"`
		}
		if _, err := toml.DecodeFile(path, &config); nil != err && !os.IsNotExist(err) {
			return slog.LevelInfo, fmt.Errorf("read logging configuration: %w", err)
		}
		value, source = config.Global.LogLevel, "global.log_level"
	}
	if "" == value {
		return slog.LevelInfo, nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(value)); nil != err {
		return slog.LevelInfo, fmt.Errorf("%s: %w", source, err)
	}
	return level, nil
}
