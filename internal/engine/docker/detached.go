package docker

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

var _ engine.DetachedOperations = (*Docker)(nil)

// Select only the fields needed here, never the container configuration or env.
const selfContainerFormat = `{"ID":{{json .Id}},"Image":{{json .Image}},"Project":{{json (index .Config.Labels "com.docker.compose.project")}},"Mounts":[{{range $i, $mount := .Mounts}}{{if $i}},{{end}}{"Type":{{json $mount.Type}},"Source":{{json $mount.Source}},"Destination":{{json $mount.Destination}},"RW":{{json $mount.RW}}}{{end}}]}`

func detachedError(message string, err error, stderr []byte) error {
	if detail := strings.TrimSpace(string(stderr)); "" != detail {
		err = fmt.Errorf("%w: %s", err, detail)
	}
	return &engine.Error{Code: 502, Message: message, Cause: err}
}

func (d *Docker) SelfContainer(ctx context.Context) (*engine.SelfContainer, error) {
	stdout, stderr, err := d.run(ctx, command("", "ps", "--quiet", "--no-trunc", "--filter", "label=DCOMPOSE_AGENT=true"))
	if nil != err {
		return nil, detachedError("Could not discover the node container.", err, stderr)
	}
	ids := strings.Fields(string(stdout))
	if 0 == len(ids) {
		return nil, nil
	}
	if 1 != len(ids) {
		return nil, detachedError("Could not identify the node container.", errors.New("more than one running container has the DCOMPOSE_AGENT=true label on this Docker host"), nil)
	}
	stdout, stderr, err = d.run(ctx, command("", "inspect", "--type", "container", "--format", selfContainerFormat, "--", ids[0]))
	if nil != err {
		return nil, detachedError("Could not identify the node container.", err, stderr)
	}
	var self engine.SelfContainer
	if err := json.Unmarshal(stdout, &self); nil != err {
		return nil, detachedError("Could not identify the node container.", err, nil)
	}
	if !containerID.MatchString(self.ID) || ids[0] != self.ID || !immutableImage(self.Image) {
		return nil, detachedError("Could not identify the node container.", errors.New("container inspect did not return a full container ID and immutable image ID"), nil)
	}
	return &self, nil
}

func immutableImage(image string) bool {
	return strings.HasPrefix(image, "sha256:") && containerID.MatchString(strings.TrimPrefix(image, "sha256:"))
}

func (d *Docker) StartDetached(ctx context.Context, self engine.SelfContainer, name, requestFile string, stack engine.Stack) error {
	if err := engine.ValidateDetachedMounts(self, requestFile, stack); nil != err {
		return err
	}
	if !containerID.MatchString(self.ID) || !immutableImage(self.Image) {
		return &engine.Error{Code: 409, Message: "Could not start the action on this node's own stack.", Cause: errors.New("node container requires an exact container ID and immutable image ID")}
	}
	groups, err := os.Getgroups()
	if nil != err {
		return detachedError("Could not start the action on this node's own stack.", err, nil)
	}
	args := []string{"run", "--detach", "--name", name, "--label", "io.dcompose.operation=" + strings.TrimPrefix(name, "dagent-operation-"), "--restart=no", "--pull", "never", "--volumes-from", self.ID, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
	for _, group := range groups {
		args = append(args, "--group-add", strconv.Itoa(group))
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		args = append(args, "--env", key)
	}
	args = append(args, "--entrypoint", "dagent", self.Image, "--run-operation", requestFile)
	_, stderr, err := d.run(ctx, command("", args...))
	if nil != err {
		return detachedError("Could not start the action on this node's own stack.", err, stderr)
	}
	return nil
}

func (d *Docker) DetachedState(ctx context.Context, name string) (engine.DetachedState, error) {
	var state engine.DetachedState
	stdout, stderr, err := d.run(ctx, command("", "inspect", "--type", "container", "--format", `{"Running":{{json .State.Running}},"ExitCode":{{json .State.ExitCode}}}`, "--", name))
	if nil != err {
		return state, detachedError("Could not check the action's status.", err, stderr)
	}
	if err := json.Unmarshal(stdout, &state); nil != err {
		return state, detachedError("Could not check the action's status.", err, nil)
	}
	if !state.Running {
		stdout, stderr, err = d.run(ctx, command("", "logs", "--", name))
		state.Output = string(stdout) + string(stderr)
		if nil != err {
			return state, detachedError("Could not read the action's output.", err, stderr)
		}
	}
	return state, nil
}

func (d *Docker) RemoveDetached(ctx context.Context, name string) error {
	_, stderr, err := d.run(ctx, command("", "rm", "--", name))
	if nil != err {
		return detachedError("Could not remove self-update helper.", err, stderr)
	}
	return nil
}
