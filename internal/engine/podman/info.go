package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/arabcoders/dcompose/internal/engine"
)

type podmanInfo struct {
	Host struct {
		Hostname        string `json:"hostname"`
		Arch            string `json:"arch"`
		OS              string `json:"os"`
		Kernel          string `json:"kernel"`
		CPUs            int    `json:"cpus"`
		MemTotal        int64  `json:"memTotal"`
		EventLogger     string `json:"eventLogger"`
		ServiceIsRemote bool   `json:"serviceIsRemote"`
		Distribution    struct {
			Distribution string `json:"distribution"`
			Version      string `json:"version"`
		} `json:"distribution"`
		Security struct {
			Rootless bool `json:"rootless"`
		} `json:"security"`
		RemoteSocket struct {
			Path string `json:"path"`
		} `json:"remoteSocket"`
	} `json:"host"`
	Store struct {
		ContainerStore struct {
			Number  int `json:"number"`
			Running int `json:"running"`
			Stopped int `json:"stopped"`
			Paused  int `json:"paused"`
		} `json:"containerStore"`
		ImageStore struct {
			Number int `json:"number"`
		} `json:"imageStore"`
		GraphDriverName string `json:"graphDriverName"`
	} `json:"store"`
	Version struct {
		Version string `json:"Version"`
	} `json:"version"`
}

func (p *Podman) info(ctx context.Context) (*podmanInfo, error) {
	if "" != p.host {
		endpoint, err := url.Parse(p.host)
		if nil != err || "unix" != endpoint.Scheme || "" != endpoint.Host || nil != endpoint.User || !strings.HasPrefix(endpoint.Path, "/") || "" != endpoint.RawQuery || "" != endpoint.Fragment {
			return nil, errors.New("CONTAINER_HOST must be an absolute unix:/// socket URL")
		}
	}
	stdout, stderr, err := p.run(ctx, p.command("", "info", "--format", "json"))
	if nil != err {
		return nil, fmt.Errorf("podman info: %w: %s", err, bytes.TrimSpace(stderr))
	}
	var info *podmanInfo
	if err := json.Unmarshal(stdout, &info); nil != err {
		return nil, fmt.Errorf("podman info: invalid JSON response: %w", err)
	}
	if nil == info || "" == info.Host.Hostname || "" == info.Version.Version {
		return nil, errors.New("podman info: response is missing the node name or Podman version")
	}
	return info, nil
}

func nodeInfoError(command string, err error, stderr []byte) error {
	if detail := bytes.TrimSpace(stderr); 0 < len(detail) {
		slog.Error(fmt.Sprintf("Could not read Podman node information with %s: %v: %s", command, err, detail), "runtime", "podman", "command", command, "error", err, "stderr", string(detail))
	} else {
		slog.Error(fmt.Sprintf("Could not read Podman node information with %s: %v", command, err), "runtime", "podman", "command", command, "error", err)
	}
	return &engine.Error{Code: 502, Message: "Could not load node information. Try again."}
}

func (p *Podman) NodeInfo(ctx context.Context) (engine.NodeInfo, error) {
	result := engine.NodeInfo{Containers: []engine.ContainerStats{}}
	info, err := p.info(ctx)
	if nil != err {
		return result, nodeInfoError("podman info", err, nil)
	}
	osName := strings.TrimSpace(info.Host.Distribution.Distribution + " " + info.Host.Distribution.Version)
	if "" == osName {
		osName = info.Host.OS
	}
	result.Runtime = engine.RuntimeInfo{
		Name: info.Host.Hostname, Version: info.Version.Version, OperatingSystem: osName,
		Architecture: info.Host.Arch, KernelVersion: info.Host.Kernel, CPUs: info.Host.CPUs, MemoryBytes: info.Host.MemTotal,
		Containers: info.Store.ContainerStore.Number, ContainersRunning: info.Store.ContainerStore.Running,
		ContainersStopped: info.Store.ContainerStore.Stopped, ContainersPaused: info.Store.ContainerStore.Paused,
		Images: info.Store.ImageStore.Number, StorageDriver: info.Store.GraphDriverName,
	}
	if 0 == info.Store.ContainerStore.Running {
		return result, nil
	}

	stdout, stderr, err := p.run(ctx, p.command("", "stats", "--no-stream", "--no-trunc", "--format", "json"))
	if nil != err {
		return result, nodeInfoError("podman stats", err, stderr)
	}
	var stats []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		CPU           string `json:"cpu_percent"`
		Memory        string `json:"mem_usage"`
		MemPercent    string `json:"mem_percent"`
		Network       string `json:"net_io"`
		Block         string `json:"block_io"`
		LegacyNetwork string `json:"netio"`
		LegacyBlock   string `json:"blocki"`
		PIDs          string `json:"pids"`
	}
	if err := json.Unmarshal(stdout, &stats); nil != err {
		return result, nodeInfoError("podman stats", fmt.Errorf("invalid JSON response: %w", err), nil)
	}
	for _, stat := range stats {
		if "" == stat.ID || "" == stat.Name {
			return result, nodeInfoError("podman stats", errors.New("response is missing the container ID or name"), nil)
		}
		network := stat.Network
		if "" == network {
			network = stat.LegacyNetwork
		}
		if "" == network {
			network = "-- / --"
		}

		block := stat.Block
		if "" == block {
			block = stat.LegacyBlock
		}

		result.Containers = append(result.Containers, engine.ContainerStats{
			ID: stat.ID, Name: stat.Name, CPU: stat.CPU, Memory: stat.Memory, MemoryPercent: stat.MemPercent,
			NetworkIO: network, BlockIO: block, PIDs: stat.PIDs,
		})
	}
	return result, nil
}
