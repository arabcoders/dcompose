package podman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/arabcoders/dcompose/internal/engine"
)

var _ engine.DetachedOperations = (*Podman)(nil)

const selfContainerFormat = `{"ID":{{json .ID}},"Image":{{json .Image}},"Project":{{json (index .Config.Labels "com.docker.compose.project")}},"Mounts":{{json .Mounts}},"Tmpfs":{{json .HostConfig.Tmpfs}}}`

func selfContainerError(err error, stderr []byte) error {
	return detachedError("Could not identify the node container.", err, stderr)
}

// SelfContainer detects own-stack actions for containerized socket-backed nodes.
// Native Podman keeps its existing host action path.
func (p *Podman) SelfContainer(ctx context.Context) (*engine.SelfContainer, error) {
	if "" == p.host {
		return nil, nil
	}
	stdout, stderr, err := p.run(ctx, p.command("", "ps", "--quiet", "--no-trunc", "--filter", "label=DCOMPOSE_AGENT=true"))
	if nil != err {
		return nil, selfContainerError(err, stderr)
	}
	ids := strings.Fields(string(stdout))
	if 0 == len(ids) {
		return nil, nil
	}
	if 1 != len(ids) {
		return nil, selfContainerError(errors.New("more than one running container has the DCOMPOSE_AGENT=true label on this Podman host"), nil)
	}
	stdout, stderr, err = p.run(ctx, p.command("", "inspect", "--type", "container", "--format", selfContainerFormat, "--", ids[0]))
	if nil != err {
		return nil, selfContainerError(err, stderr)
	}
	var data struct {
		engine.SelfContainer
		Tmpfs map[string]string
	}
	if err := json.Unmarshal(stdout, &data); nil != err {
		return nil, selfContainerError(err, nil)
	}
	self := data.SelfContainer
	if !containerID.MatchString(self.ID) || ids[0] != self.ID || !containerID.MatchString(strings.TrimPrefix(self.Image, "sha256:")) || "" == self.Project {
		return nil, selfContainerError(errors.New("container inspect did not return the expected full container ID, immutable image ID and Compose project"), nil)
	}
	// Podman omits tmpfs from Mounts. Keep it for shared-path validation.
	for destination := range data.Tmpfs {
		self.Mounts = append(self.Mounts, engine.ContainerMount{Type: "tmpfs", Destination: destination, RW: true})
	}
	return &self, nil
}

func (p *Podman) StartDetached(ctx context.Context, self engine.SelfContainer, name, requestFile string, stack engine.Stack) error {
	if err := engine.ValidateDetachedMounts(self, requestFile, stack); nil != err {
		return err
	}
	if !containerID.MatchString(self.ID) || !containerID.MatchString(strings.TrimPrefix(self.Image, "sha256:")) {
		return &engine.Error{Code: 409, Message: "Could not start the action on this node's own stack.", Cause: errors.New("node container requires an exact container ID and immutable image ID")}
	}
	groups, err := os.Getgroups()
	if nil != err {
		return detachedError("Could not start the action on this node's own stack.", err, nil)
	}
	args := []string{"run", "--detach", "--name", name, "--label", "io.dcompose.operation=" + strings.TrimPrefix(name, "dagent-operation-"), "--restart=no", "--pull", "never", "--volumes-from", self.ID, "--security-opt", "label=disable", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
	for _, group := range groups {
		args = append(args, "--group-add", strconv.Itoa(group))
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if "CONTAINER_HOST" != key {
			args = append(args, "--env", key)
		}
	}
	args = append(args, "--env", "CONTAINER_HOST", "--entrypoint", "dagent", self.Image, "--run-operation", requestFile)
	cmd := p.command("", args...)
	cmd.Env = []string{"CONTAINER_HOST=" + p.host}
	_, stderr, err := p.run(ctx, cmd)
	if nil != err {
		return detachedError("Could not start the action on this node's own stack.", err, stderr)
	}
	return nil
}

func detachedError(message string, err error, stderr []byte) error {
	if detail := strings.TrimSpace(string(stderr)); "" != detail {
		err = fmt.Errorf("%w: %s", err, detail)
	}
	return &engine.Error{Code: 502, Message: message, Cause: err}
}

func (p *Podman) DetachedState(ctx context.Context, name string) (engine.DetachedState, error) {
	var state engine.DetachedState
	stdout, stderr, err := p.run(ctx, p.command("", "inspect", "--type", "container", "--format", `{"Running":{{json .State.Running}},"ExitCode":{{json .State.ExitCode}}}`, "--", name))
	if nil != err {
		return state, detachedError("Could not check the action's status.", err, stderr)
	}
	if err := json.Unmarshal(stdout, &state); nil != err {
		return state, detachedError("Could not check the action's status.", err, nil)
	}
	if !state.Running {
		stdout, stderr, err = p.run(ctx, p.command("", "logs", "--", name))
		state.Output = string(stdout) + string(stderr)
		if nil != err {
			return state, detachedError("Could not read the action's output.", err, stderr)
		}
	}
	return state, nil
}

func (p *Podman) RemoveDetached(ctx context.Context, name string) error {
	_, stderr, err := p.run(ctx, p.command("", "rm", "--", name))
	if nil != err {
		return detachedError("Could not remove self-update helper.", err, stderr)
	}
	return nil
}
