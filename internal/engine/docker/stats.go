package docker

import (
	"context"
	"fmt"

	"github.com/arabcoders/dcompose/internal/engine"
)

func (d *Docker) Stats(ctx context.Context, stack engine.Stack) ([]engine.ContainerStats, error) {
	containers, err := d.Containers(ctx, stack)
	if nil != err {
		return nil, err
	}
	ids := []string{}
	for _, container := range containers {
		if "running" != container.State {
			continue
		}
		id := container.ID
		if "" == id {
			id = container.Name
		}
		if "" != id {
			ids = append(ids, id)
		}
	}
	// Without explicit containers, the native command would collect the whole node.
	if 0 == len(ids) {
		return []engine.ContainerStats{}, nil
	}
	args := append([]string{"stats", "--no-stream", "--no-trunc", "--format", "{{json .}}"}, ids...)
	stdout, _, err := d.run(ctx, command("", args...))
	if nil != err {
		return nil, &engine.Error{Code: 502, Message: "Could not load stack statistics.", Cause: fmt.Errorf("docker stats: %w", err)}
	}
	stats, err := parseStats(stdout)
	if nil != err {
		return nil, &engine.Error{Code: 502, Message: "Could not load stack statistics.", Cause: err}
	}
	return stats, nil
}
