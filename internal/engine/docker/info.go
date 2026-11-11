package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/arabcoders/dcompose/internal/engine"
)

func nodeInfoError(command string, err error, stderr []byte) error {
	if detail := bytes.TrimSpace(stderr); 0 < len(detail) {
		slog.Error(
			fmt.Sprintf("Could not read Docker node information with %s: %v: %s", command, err, detail),
			"runtime",
			"docker",
			"command",
			command,
			"error",
			err,
			"stderr",
			string(detail),
		)
	} else {
		slog.Error(
			fmt.Sprintf("Could not read Docker node information with %s: %v", command, err),
			"runtime",
			"docker",
			"command",
			command,
			"error",
			err,
		)
	}
	return &engine.Error{Code: 502, Message: "Could not load node information. Try again."}
}

func (d *Docker) NodeInfo(ctx context.Context) (engine.NodeInfo, error) {
	result := engine.NodeInfo{Containers: []engine.ContainerStats{}}
	stdout, stderr, err := d.run(ctx, command("", "info", "--format", "{{json .}}"))
	if nil != err {
		return result, nodeInfoError("docker info", err, stderr)
	}
	var info *struct {
		Name              string `json:"Name"`
		ServerVersion     string `json:"ServerVersion"`
		OperatingSystem   string `json:"OperatingSystem"`
		Architecture      string `json:"Architecture"`
		KernelVersion     string `json:"KernelVersion"`
		NCPU              int    `json:"NCPU"`
		MemTotal          int64  `json:"MemTotal"`
		Containers        int    `json:"Containers"`
		ContainersRunning int    `json:"ContainersRunning"`
		ContainersStopped int    `json:"ContainersStopped"`
		ContainersPaused  int    `json:"ContainersPaused"`
		Images            int    `json:"Images"`
		Driver            string `json:"Driver"`
	}
	if err := json.Unmarshal(stdout, &info); nil != err {
		return result, nodeInfoError("docker info", fmt.Errorf("invalid JSON response: %w", err), nil)
	}
	if nil == info || "" == info.Name || "" == info.ServerVersion {
		return result, nodeInfoError("docker info", errors.New("response is missing the node name or Docker version"), nil)
	}
	result.Runtime = engine.RuntimeInfo{
		Name: info.Name, Version: info.ServerVersion, OperatingSystem: info.OperatingSystem,
		Architecture: info.Architecture, KernelVersion: info.KernelVersion, CPUs: info.NCPU, MemoryBytes: info.MemTotal,
		Containers: info.Containers, ContainersRunning: info.ContainersRunning, ContainersStopped: info.ContainersStopped,
		ContainersPaused: info.ContainersPaused, Images: info.Images, StorageDriver: info.Driver,
	}
	if 0 == info.ContainersRunning {
		return result, nil
	}

	stdout, stderr, err = d.run(ctx, command("", "stats", "--no-stream", "--no-trunc", "--format", "{{json .}}"))
	if nil != err {
		return result, nodeInfoError("docker stats", err, stderr)
	}
	result.Containers, err = parseStats(stdout)
	if nil != err {
		return result, nodeInfoError("docker stats", err, nil)
	}
	return result, nil
}

func parseStats(stdout []byte) ([]engine.ContainerStats, error) {
	result := []engine.ContainerStats{}
	decoder := json.NewDecoder(bytes.NewReader(stdout))
	for {
		var stats *struct {
			ID       string `json:"ID"`
			Name     string `json:"Name"`
			CPUPerc  string `json:"CPUPerc"`
			MemUsage string `json:"MemUsage"`
			MemPerc  string `json:"MemPerc"`
			NetIO    string `json:"NetIO"`
			BlockIO  string `json:"BlockIO"`
			PIDs     string `json:"PIDs"`
		}
		if err := decoder.Decode(&stats); io.EOF == err {
			break
		} else if nil != err {
			return nil, fmt.Errorf("invalid JSON response: %w", err)
		}
		if nil == stats || "" == stats.ID || "" == stats.Name {
			return nil, errors.New("response is missing the container ID or name")
		}
		result = append(result, engine.ContainerStats{
			ID: stats.ID, Name: stats.Name, CPU: stats.CPUPerc, Memory: stats.MemUsage, MemoryPercent: stats.MemPerc,
			NetworkIO: stats.NetIO, BlockIO: stats.BlockIO, PIDs: stats.PIDs,
		})
	}
	return result, nil
}
