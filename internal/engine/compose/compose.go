// Package compose runs Docker Compose against the runtime selected by a driver.
package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/arabcoders/dcompose/internal/engine"
)

type CLI struct {
	run engine.Run
	env []string
}

func New(run engine.Run, env []string) *CLI {
	return &CLI{run: run, env: env}
}

func (c *CLI) command(s engine.Stack, args ...string) engine.Command {
	base := []string{"compose", "--project-name", s.Name, "--project-directory", s.Dir, "--env-file", s.EnvFile, "--file", s.ComposeFile}
	return engine.Command{Program: "docker", Args: append(base, args...), Dir: s.Dir, Env: c.env}
}

func (c *CLI) Check(ctx context.Context) error {
	if stdout, stderr, err := c.run(ctx, engine.Command{Program: "docker", Args: []string{"compose", "version"}, Env: c.env}); nil != err {
		return fmt.Errorf("Docker Compose v2 is required: %w: %s%s", err, stdout, stderr)
	}
	stdout, stderr, err := c.run(ctx, engine.Command{Program: "docker", Args: []string{"compose", "config", "--help"}, Env: c.env})
	if nil != err || !bytes.Contains(append(stdout, stderr...), []byte("--no-env-resolution")) {
		return fmt.Errorf("Docker Compose 2.35+ with config --no-env-resolution support is required")
	}
	return nil
}

func (c *CLI) Validate(ctx context.Context, s engine.Stack) error {
	// Do not resolve service env_file entries before staged files are installed.
	stdout, stderr, err := c.run(ctx, c.command(s, "config", "--quiet", "--no-env-resolution", "--services"))
	if nil != err {
		return &engine.Error{Code: 400, Message: "Compose validation failed. Check the Compose and .env files.", Cause: fmt.Errorf("compose validation failed: %w: %s%s", err, stdout, stderr)}
	}
	return nil
}

func (c *CLI) Services(ctx context.Context, s engine.Stack) ([]string, error) {
	stdout, stderr, err := c.run(ctx, c.command(s, "config", "--services"))
	if nil != err {
		return nil, &engine.Error{Code: 502, Message: "Could not read Compose services. Try again.", Cause: fmt.Errorf("compose command failed: %w: %s", err, stderr)}
	}
	return strings.Fields(string(stdout)), nil
}

func (c *CLI) Images(ctx context.Context, s engine.Stack) ([]string, error) {
	stdout, _, err := c.run(ctx, c.command(s, "config", "--images", "--no-env-resolution"))
	return strings.Fields(string(stdout)), err
}

func (c *CLI) Action(s engine.Stack, action, service string) engine.Command {
	args := []string{action}
	switch action {
	case "start", "deploy":
		args = []string{"up", "-d"}
	case "update":
		args = []string{"up", "-d", "--no-deps"}
	}
	if "" != service {
		args = append(args, "--", service)
	}
	cmd := c.command(s, args...)
	// The terminal handles TTY progress redraws for action output.
	cmd.Env = append(append([]string{}, c.env...), "COMPOSE_ANSI=always", "COMPOSE_PROGRESS=tty")
	return cmd
}

func (c *CLI) Logs(s engine.Stack) engine.Command {
	return c.command(s, "logs", "--follow", "--tail", "100", "--no-color")
}

type container struct {
	ID         string `json:"ID"`
	Name       string `json:"Name"`
	Service    string `json:"Service"`
	Image      string `json:"Image"`
	State      string `json:"State"`
	Health     string `json:"Health"`
	ExitCode   *int   `json:"ExitCode"`
	Status     string `json:"Status"`
	CreatedAt  string `json:"CreatedAt"`
	RunningFor string `json:"RunningFor"`
	Ports      string `json:"Ports"`
	Publishers []struct {
		URL           string `json:"URL"`
		TargetPort    int    `json:"TargetPort"`
		PublishedPort int    `json:"PublishedPort"`
		Protocol      string `json:"Protocol"`
	} `json:"Publishers"`
}

func (c *CLI) Containers(ctx context.Context, s engine.Stack) ([]engine.Service, error) {
	stdout, stderr, err := c.run(ctx, c.command(s, "ps", "--all", "--format", "json"))
	if nil != err {
		return nil, &engine.Error{Code: 502, Message: "Could not read container information. Try again.", Cause: fmt.Errorf("compose ps: %w: %s", err, stderr)}
	}
	data := bytes.TrimSpace(stdout)
	containers := []container{}
	if 0 < len(data) && '[' == data[0] {
		err = json.Unmarshal(data, &containers)
	} else {
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var item container
			if decodeErr := decoder.Decode(&item); io.EOF == decodeErr {
				break
			} else if nil != decodeErr {
				err = decodeErr
				break
			}
			containers = append(containers, item)
		}
	}
	if nil != err {
		return nil, &engine.Error{Code: 502, Message: "Could not read container information. Try again.", Cause: fmt.Errorf("invalid compose ps output: %w", err)}
	}
	services := make([]engine.Service, 0, len(containers))
	for _, item := range containers {
		bindings := make([]engine.PortBinding, 0, len(item.Publishers))
		for _, port := range item.Publishers {
			bindings = append(bindings, engine.PortBinding{HostIP: port.URL, HostPort: port.PublishedPort, ContainerPort: port.TargetPort, Protocol: port.Protocol})
		}
		ports := item.Ports
		if "" == ports {
			list := []string{}
			for _, publisher := range item.Publishers {
				port := strconv.Itoa(publisher.TargetPort) + "/" + publisher.Protocol
				if 0 != publisher.PublishedPort {
					port = publisher.URL + ":" + strconv.Itoa(publisher.PublishedPort) + "->" + port
				}
				list = append(list, port)
			}
			ports = strings.Join(list, ", ")
		}
		services = append(services, engine.Service{
			ID: item.ID, Name: item.Name, Service: item.Service, Image: item.Image,
			State: item.State, Health: item.Health, ExitCode: item.ExitCode, Status: item.Status,
			CreatedAt: item.CreatedAt, RunningFor: item.RunningFor,
			Ports: ports, PortBindings: bindings,
		})
	}
	return services, nil
}
