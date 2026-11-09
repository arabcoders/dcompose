package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/engine/compose"
)

type Docker struct {
	*compose.CLI
	run engine.Run
}

func New(run engine.Run) *Docker {
	return &Docker{CLI: compose.New(run, nil), run: run}
}

var _ engine.Driver = (*Docker)(nil)

func command(dir string, args ...string) engine.Command {
	return engine.Command{Program: "docker", Args: args, Dir: dir}
}

func (d *Docker) Containers(ctx context.Context, stack engine.Stack) ([]engine.Service, error) {
	services, err := d.CLI.Containers(ctx, stack)
	if nil != err {
		return nil, err
	}
	for i := range services {
		data, _, err := d.run(ctx, command("", "container", "inspect", "--format", compose.ContainerInfoFormat, services[i].Name))
		if nil != err {
			return nil, fmt.Errorf("inspect container metadata: %w", err)
		}
		if err := compose.ApplyContainerInfo(&services[i], data); nil != err {
			return nil, fmt.Errorf("decode container metadata: %w", err)
		}
	}
	return services, nil
}

func (d *Docker) ImageRefs(ctx context.Context, s engine.Stack) (map[string]bool, error) {
	images, err := d.Images(ctx, s)
	refs := make(map[string]bool)
	if nil == err {
		for _, ref := range images {
			refs[d.ImageRef(ref)] = true
		}
	}
	return refs, err
}

func (d *Docker) Probe(ctx context.Context) error {
	_, stderr, err := d.run(ctx, command("", "version", "--format", "{{.Server.Version}}"))
	if detail := strings.TrimSpace(string(stderr)); nil != err && "" != detail {
		return fmt.Errorf("%w: %s", err, detail)
	}
	return err
}

func (d *Docker) Events(since time.Time) engine.Command {
	return command(
		"",
		"events",
		"--since",
		fmt.Sprintf("%d.%09d", since.Unix(), since.Nanosecond()),
		"--format",
		"{{json .}}",
		"--filter",
		"type=container",
		"--filter",
		"label=com.docker.compose.project",
	)
}

func (d *Docker) EventStack(line []byte) string {
	var e struct {
		Type   string `json:"Type"`
		Action string `json:"Action"`
		Actor  struct {
			Attributes map[string]string `json:"Attributes"`
		} `json:"Actor"`
	}
	if nil != json.Unmarshal(line, &e) || "container" != e.Type || strings.HasPrefix(e.Action, "exec_") {
		return ""
	}
	return e.Actor.Attributes["com.docker.compose.project"]
}
