package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/arabcoders/dcompose/internal/engine"
)

// The marker is inherited by shell jobs. Readiness makes close-after-create
// safe: cleanup cannot race ahead of the daemon starting the marked exec.
const shellBootstrap = `if [ ! -r /proc/self/environ ] || ! command -v tr >/dev/null || ! command -v "$1" >/dev/null; then
 echo 'Shell requires Linux /proc, sh, tr and the selected shell' >&2; exit 125
fi
printf '\036%s\037\n' "$DOCKER_NODE_SHELL_ID"
exec "$1" -i`

// Attach disconnect does not terminate the daemon-side exec. Match complete
// NUL-delimited environment entries, excluding forged embedded newline markers.
const shellCleanup = `for pass in 1 2; do
 for proc in /proc/[0-9]*; do
  pid=${proc##*/}
  [ "$pid" -gt 1 ] && [ "$pid" != "$$" ] || continue
  env=$(tr '\n\000' '\001\n' < "$proc/environ" 2>/dev/null) || continue
  case "
$env
" in *"
DOCKER_NODE_SHELL_ID=$1
"*) kill -KILL "$pid" 2>/dev/null || :;; esac
 done
done`

var containerReference = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,255}$`)
var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (d *Docker) ResolveContainer(ctx context.Context, s engine.Stack, reference string) (string, error) {
	if !containerReference.MatchString(reference) {
		return "", &engine.Error{Code: 400, Message: "invalid container"}
	}
	output, stderr, err := d.run(ctx, command(s.Dir, "inspect", "--type", "container", "--", reference))
	if nil != err {
		if detail := strings.TrimSpace(string(stderr)); "" != detail {
			slog.Error(
				fmt.Sprintf("Could not inspect Docker container %q in stack %q for a shell: %v: %s", reference, s.Name, err, detail),
				"runtime",
				"docker",
				"command",
				"docker inspect",
				"stack",
				s.Name,
				"container",
				reference,
				"error",
				err,
				"stderr",
				detail,
			)
		} else {
			slog.Error(
				fmt.Sprintf("Could not inspect Docker container %q in stack %q for a shell: %v", reference, s.Name, err),
				"runtime",
				"docker",
				"command",
				"docker inspect",
				"stack",
				s.Name,
				"container",
				reference,
				"error",
				err,
			)
		}
		return "", &engine.Error{Code: 404, Message: "container not found"}
	}
	var containers []struct {
		ID    string `json:"Id"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(output, &containers); nil != err || 1 != len(containers) || !containerID.MatchString(containers[0].ID) {
		return "", &engine.Error{Code: 502, Message: "Could not read container information. Try again."}
	}
	c := containers[0]
	if s.Name != c.Config.Labels["com.docker.compose.project"] {
		return "", &engine.Error{Code: 403, Message: "container does not belong to this stack"}
	}
	if !c.State.Running {
		return "", &engine.Error{Code: 409, Message: "container is not running"}
	}
	return c.ID, nil
}

func (d *Docker) Shell(container, shell, id string) engine.Command {
	return command("", "exec", "-it", "--detach-keys=", "--env", "DOCKER_NODE_SHELL_ID="+id,
		"--env", "TERM=xterm-256color", container, "sh", "-c", shellBootstrap, "docker-node-shell", shell)
}

func (d *Docker) CleanupShell(ctx context.Context, container, id string) error {
	// Do not retain/log environment, terminal contents, or cleanup diagnostics.
	_, _, err := d.run(ctx, command("", "exec", "--user", "0", container, "sh", "-c", shellCleanup, "docker-node-cleanup", id))
	if nil == err {
		return nil
	}
	// A stopped/removed container cannot retain exec processes. Distinguish it
	// from an unavailable daemon without parsing human error messages.
	running, _, probeErr := d.run(ctx, command("", "ps", "--quiet", "--no-trunc", "--filter", "id="+container))
	if nil == probeErr && "" == strings.TrimSpace(string(running)) {
		return nil
	}
	return err
}
