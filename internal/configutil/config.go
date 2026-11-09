package configutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Find checks explicit paths before user and system XDG locations. New
// installations use the user location when available.
func Find(explicit, filename string) (string, error) {
	if "" != explicit {
		return filepath.Abs(explicit)
	}
	if directory := os.Getenv("DCOMPOSE_CONFIG_DIR"); "" != directory {
		return filepath.Abs(filepath.Join(directory, filename))
	}

	var candidates []string
	home := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(home) {
		if userHome, err := os.UserHomeDir(); nil == err && filepath.IsAbs(userHome) {
			home = filepath.Join(userHome, ".config")
		} else {
			home = ""
		}
	}
	if "" != home {
		candidates = append(candidates, filepath.Join(home, "dcompose", filename))
	}

	directories := os.Getenv("XDG_CONFIG_DIRS")
	if "" == directories {
		directories = "/etc/xdg"
	}
	for _, directory := range filepath.SplitList(directories) {
		if filepath.IsAbs(directory) {
			candidates = append(candidates, filepath.Join(directory, "dcompose", filename))
		}
	}
	fallback := filepath.Join("/etc/dcompose", filename)
	candidates = append(candidates, fallback)
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); nil == err {
			return candidate, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("check config file %s: %w", candidate, err)
		}
	}
	return candidates[0], nil
}

// Path resolves relative paths from the directory containing the loaded TOML.
func Path(configPath, value string) string {
	if "" == value || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(filepath.Dir(configPath), value)
}

// Resolve reads file: references without expanding environment or home variables.
func Resolve(value, configPath string) (string, error) {
	if !strings.HasPrefix(value, "file:") {
		return value, nil
	}
	reference := strings.TrimPrefix(value, "file:")
	if "" == reference {
		return "", fmt.Errorf("file reference is empty")
	}
	data, err := os.ReadFile(Path(configPath, reference))
	if nil != err {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	result := string(data)
	if trimmed, ok := strings.CutSuffix(result, "\r\n"); ok {
		result = trimmed
	} else if strings.HasSuffix(result, "\n") || strings.HasSuffix(result, "\r") {
		result = result[:len(result)-1]
	}
	if "" == result {
		return "", fmt.Errorf("secret file is empty")
	}
	return result, nil
}

// Write atomically replaces a file with permissions restricted to its owner.
func Write(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); nil != err {
		return err
	}
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if nil != err {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	if _, err := file.Write(data); nil != err {
		return err
	}
	if err := file.Sync(); nil != err {
		return err
	}
	if err := file.Close(); nil != err {
		return err
	}
	return os.Rename(file.Name(), path)
}
