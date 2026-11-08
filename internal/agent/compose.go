package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/arabcoders/dcompose/internal/engine"
)

var composeNames = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}
var stackName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)

type service = engine.Service

type stack struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Compose  string    `json:"compose"`
	Env      string    `json:"env"`
	Services []service `json:"services"`
}

func (a *agent) stackPath(name string, create bool) (string, error) {
	if !stackName.MatchString(name) {
		return "", fault(400, "stack name must be 1-128 lowercase letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	dir := filepath.Join(a.config.Global.StacksDir, name)
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) && create {
		return dir, nil
	}
	if os.IsNotExist(err) {
		return "", fault(404, "stack not found")
	}
	if nil != err {
		return "", err
	}
	if !info.IsDir() || 0 != info.Mode()&os.ModeSymlink {
		return "", fault(400, "stack must be a real directory, not a symlink")
	}
	return dir, nil
}

// Reject existing symlinks and other non-regular files before stack file access.
func managedFile(dir, name string) (string, error) {
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return path, nil
	}
	if nil != err {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fault(400, "stack file must be a regular file: "+name)
	}
	return path, nil
}

func composeFile(dir string) (string, error) {
	for _, name := range composeNames {
		path, err := managedFile(dir, name)
		if nil != err {
			return "", err
		}
		if _, err := os.Stat(path); nil == err {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", fault(404, "compose file not found")
}

func readOptional(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(data), err
}

func (a *agent) engineStack(name, dir string) (engine.Stack, error) {
	file, err := composeFile(dir)
	if nil != err {
		return engine.Stack{}, err
	}
	env, err := managedFile(dir, ".env")
	if nil != err {
		return engine.Stack{}, err
	}
	if _, err := os.Stat(env); os.IsNotExist(err) {
		env = "/dev/null"
	} else if nil != err {
		return engine.Stack{}, err
	}
	return engine.Stack{Name: name, Dir: dir, ComposeFile: file, EnvFile: env}, nil
}

func (a *agent) containers(ctx context.Context, name, dir string) ([]service, error) {
	s, err := a.engineStack(name, dir)
	if nil != err {
		return nil, err
	}
	return a.engine.Containers(ctx, s)
}

func stackStatus(services []service) string {
	if 0 == len(services) {
		return "draft"
	}
	running, stopped := 0, 0
	for _, s := range services {
		switch s.State {
		case "running":
			running++
		case "exited", "created", "dead":
			stopped++
		}
	}
	if len(services) == running {
		return "running"
	}
	if len(services) == stopped {
		return "stopped"
	}
	if 0 < running {
		return "partial"
	}
	return "unknown"
}

func tempFile(dir, pattern, content string) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if nil != err {
		return "", err
	}
	name := f.Name()
	_, err = f.WriteString(content)
	if nil == err {
		err = f.Sync()
	}
	closeErr := f.Close()
	if nil == err {
		err = closeErr
	}
	if nil != err {
		_ = os.Remove(name)
	}
	return name, err
}

func (a *agent) save(ctx context.Context, name, compose, env string) error {
	return a.saveFiles(ctx, name, compose, env, true, true)
}

func (a *agent) saveFiles(ctx context.Context, name, compose, env string, validate, overwrite bool) error {
	dir, err := a.stackPath(name, true)
	if nil != err {
		return err
	}
	created := false
	if err := os.Mkdir(dir, 0750); nil == err {
		created = true
	} else if !os.IsExist(err) {
		return err
	} else if !overwrite {
		return fault(409, "destination stack directory already exists.")
	}
	if created {
		defer os.Remove(dir)
	} // Only removes an empty, rejected creation.
	file, err := composeFile(dir)
	var apiErr *apiError
	if errors.As(err, &apiErr) && 404 == apiErr.code {
		file = filepath.Join(dir, composeNames[0])
	} else if nil != err {
		return err
	}
	envPath, err := managedFile(dir, ".env")
	if nil != err {
		return err
	}
	oldEnv, err := os.ReadFile(envPath)
	oldEnvExists := nil == err
	if nil != err && !os.IsNotExist(err) {
		return err
	}
	composeTemp, err := tempFile(dir, ".dagent-compose-*.yaml", compose)
	if nil != err {
		return err
	}
	defer os.Remove(composeTemp)
	envTemp, err := tempFile(dir, ".dagent-env-*", env)
	if nil != err {
		return err
	}
	defer os.Remove(envTemp)
	if validate {
		// Validation resolves interpolation from the staged env, not service env_file
		// entries that may refer to the not-yet-installed .env.
		if err := a.engine.Validate(ctx, engine.Stack{Name: name, Dir: dir, ComposeFile: composeTemp, EnvFile: envTemp}); nil != err {
			return err
		}
	}
	if err := os.Rename(envTemp, envPath); nil != err {
		return err
	}
	if err := os.Rename(composeTemp, file); nil != err {
		if oldEnvExists {
			rollback, restoreErr := tempFile(dir, ".dagent-env-*", string(oldEnv))
			if nil == restoreErr {
				restoreErr = os.Rename(rollback, envPath)
				_ = os.Remove(rollback)
			}
			if nil != restoreErr {
				return fmt.Errorf("save failed: %v. Restoring .env failed: %w", err, restoreErr)
			}
		} else {
			_ = os.Remove(envPath)
		}
		return err
	}
	return nil
}
