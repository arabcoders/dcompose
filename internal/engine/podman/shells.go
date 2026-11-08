package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/arabcoders/dcompose/internal/engine"
)

// The marker is inherited by shell jobs. Readiness prevents cleanup from
// racing ahead of Podman starting the marked exec.
const shellBootstrap = `if [ ! -r /proc/self/environ ] || ! command -v tr >/dev/null || ! command -v "$1" >/dev/null; then
 echo 'Shell requires Linux /proc, sh, tr and the selected shell' >&2; exit 125
fi
printf '\036%s\037\n' "$DCOMPOSE_SHELL_ID"
exec "$1" -i`

// Attach disconnect does not terminate the container-side exec. Match complete
// NUL-delimited environment entries, excluding forged embedded newline markers.
const shellCleanup = `for pass in 1 2; do
 for proc in /proc/[0-9]*; do
  pid=${proc##*/}
  [ "$pid" -gt 1 ] && [ "$pid" != "$$" ] || continue
  env=$(tr '\n\000' '\001\n' < "$proc/environ" 2>/dev/null) || continue
  case "
$env
" in *"
DCOMPOSE_SHELL_ID=$1
"*) kill -KILL "$pid" 2>/dev/null || :;; esac
 done
done`

var containerReference = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,255}$`)
var containerID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (p *Podman) ResolveContainer(ctx context.Context, s engine.Stack, reference string) (string, error) {
	if !containerReference.MatchString(reference) {
		return "", &engine.Error{Code: 400, Message: "invalid container"}
	}
	output, stderr, err := p.run(ctx, p.command(s.Dir, "inspect", "--type", "container", "--", reference))
	if nil != err {
		if detail := strings.TrimSpace(string(stderr)); "" != detail {
			slog.Error(fmt.Sprintf("Could not inspect Podman container %q in stack %q for a shell: %v: %s", reference, s.Name, err, detail), "runtime", "podman", "command", "podman inspect", "stack", s.Name, "container", reference, "error", err, "stderr", detail)
		} else {
			slog.Error(fmt.Sprintf("Could not inspect Podman container %q in stack %q for a shell: %v", reference, s.Name, err), "runtime", "podman", "command", "podman inspect", "stack", s.Name, "container", reference, "error", err)
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

func (p *Podman) Shell(container, shell, id string) engine.Command {
	return p.command("", "exec", "-it", "--detach-keys=", "--env", "DCOMPOSE_SHELL_ID="+id,
		"--env", "TERM=xterm-256color", container, "sh", "-c", shellBootstrap, "dcompose-shell", shell)
}

func (p *Podman) CleanupShell(ctx context.Context, container, id string) error {
	// Do not retain/log environment, terminal contents, or cleanup diagnostics.
	_, _, err := p.run(ctx, p.command("", "exec", "--user", "0", container, "sh", "-c", shellCleanup, "dcompose-cleanup", id))
	if nil == err {
		return nil
	}
	// A stopped/removed container cannot retain exec processes. Distinguish it
	// from an unavailable engine without parsing human error messages.
	running, _, probeErr := p.run(ctx, p.command("", "ps", "--quiet", "--no-trunc", "--filter", "id="+container))
	if nil == probeErr && "" == strings.TrimSpace(string(running)) {
		return nil
	}
	return err
}
