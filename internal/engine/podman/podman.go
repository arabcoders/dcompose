package podman

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/engine/compose"
)

type Podman struct {
	*compose.CLI
	run  engine.Run
	host string
}

func New(run engine.Run) *Podman {
	host := os.Getenv("CONTAINER_HOST")
	endpoint := host
	if "" == endpoint {
		endpoint = "unix://" + localSocket(0 != os.Geteuid())
	}
	return &Podman{CLI: compose.New(run, composeEnv(endpoint)), run: run, host: host}
}

var _ engine.Driver = (*Podman)(nil)

func (p *Podman) Containers(ctx context.Context, stack engine.Stack) ([]engine.Service, error) {
	services, err := p.CLI.Containers(ctx, stack)
	if nil != err {
		return nil, err
	}
	for i := range services {
		data, _, err := p.run(ctx, p.command("", "container", "inspect", "--format", compose.ContainerInfoFormat, services[i].Name))
		if nil != err {
			return nil, fmt.Errorf("inspect container metadata: %w", err)
		}
		if err := compose.ApplyContainerInfo(&services[i], data); nil != err {
			return nil, fmt.Errorf("decode container metadata: %w", err)
		}
	}
	return services, nil
}

func (p *Podman) command(dir string, args ...string) engine.Command {
	if "" != p.host {
		args = append([]string{"--remote", "--url", p.host}, args...)
	}
	return engine.Command{Program: "podman", Args: args, Dir: dir}
}

func localSocket(rootless bool) string {
	if !rootless {
		return "/run/podman/podman.sock"
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if "" == dir {
		dir = filepath.Join("/run/user", strconv.Itoa(os.Geteuid()))
	}
	return filepath.Join(dir, "podman/podman.sock")
}

func composeEnv(endpoint string) []string {
	// Docker contexts take precedence over DOCKER_HOST. Clear all Docker
	// transport overrides so Compose and the native CLI use the same engine.
	return []string{"DOCKER_HOST=" + endpoint, "DOCKER_CONTEXT=", "DOCKER_TLS_VERIFY=", "DOCKER_CERT_PATH="}
}

func (p *Podman) Check(ctx context.Context) error {
	info, err := p.info(ctx)
	if nil != err {
		return err
	}
	if "linux" != info.Host.OS || (info.Host.ServiceIsRemote && "" == p.host) {
		return fmt.Errorf("Podman requires a local Linux engine or an explicit CONTAINER_HOST Unix socket URL")
	}
	if "" == info.Host.EventLogger || "none" == info.Host.EventLogger {
		return fmt.Errorf("Podman events must be enabled using the file or journald events logger")
	}
	endpoint := p.host
	if "" == endpoint {
		socket := info.Host.RemoteSocket.Path
		if "" == socket {
			socket = localSocket(info.Host.Security.Rootless)
		}
		if !filepath.IsAbs(socket) {
			return fmt.Errorf("Podman must report a local absolute Unix socket path")
		}
		endpoint = "unix://" + socket
	}
	p.CLI = compose.New(p.run, composeEnv(endpoint))
	return p.CLI.Check(ctx)
}

func (p *Podman) Probe(ctx context.Context) error {
	_, err := p.info(ctx)
	return err
}

func (p *Podman) ImageRefs(ctx context.Context, s engine.Stack) (map[string]bool, error) {
	images, err := p.CLI.Images(ctx, s)
	refs := make(map[string]bool)
	if nil == err {
		for _, ref := range images {
			refs[p.ImageRef(ref)] = true
		}
	}
	return refs, err
}

func (p *Podman) Events(since time.Time) engine.Command {
	// Podman 4.3 drops all events for label-presence filters. EventStack
	// filters Compose ownership after decoding the native event labels.
	return p.command("", "events", "--since", since.UTC().Format(time.RFC3339Nano), "--format", "json", "--filter", "type=container")
}

func (p *Podman) EventStack(line []byte) string {
	var event struct {
		Type       string            `json:"Type"`
		Status     string            `json:"Status"`
		Attributes map[string]string `json:"Attributes"`
	}
	if nil != json.Unmarshal(line, &event) || "container" != event.Type || "exec" == event.Status ||
		strings.HasPrefix(event.Status, "exec_") {
		return ""
	}
	return event.Attributes["com.docker.compose.project"]
}
