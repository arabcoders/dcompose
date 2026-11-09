package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/arabcoders/dcompose/internal/engine"
)

var imageIDReference = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

// ImageChanged compares the container's image with its local reference after pull.
func (d *Docker) ImageChanged(ctx context.Context, s engine.Service) (bool, error) {
	data, stderr, err := d.run(
		ctx,
		command("", "container", "inspect", "--format", `{"id":{{json .Image}},"ref":{{json .Config.Image}}}`, s.Name),
	)
	if nil != err {
		return false, fmt.Errorf("inspect container image %s: %w: %s", s.Name, err, stderr)
	}
	var container struct {
		ID  string `json:"id"`
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(data, &container); nil != err {
		return false, fmt.Errorf("parse container image %s: %w", s.Name, err)
	}
	if "" == container.ID || "" == container.Ref {
		return false, fmt.Errorf("container %s is missing its image ID or reference", s.Name)
	}
	data, stderr, err = d.run(ctx, command("", "image", "inspect", "--format", "{{.Id}}", container.Ref))
	if nil != err {
		return false, fmt.Errorf("inspect pulled image %s: %w: %s", container.Ref, err, stderr)
	}
	id := strings.TrimSpace(string(data))
	if "" == id {
		return false, fmt.Errorf("image %s is missing its ID", container.Ref)
	}
	return container.ID != id, nil
}

func (d *Docker) InspectImage(ctx context.Context, s engine.Service) engine.ImageTarget {
	result := engine.ImageTarget{Status: "error", Reason: "inspect_error"}
	if "" == s.Name || strings.HasPrefix(s.Name, "-") {
		slog.Warn(
			fmt.Sprintf(
				"Could not check Docker image %q for container %q: container name is empty or starts with a dash.",
				s.Image,
				s.Name,
			),
			"runtime",
			"docker",
			"container",
			s.Name,
			"image",
			s.Image,
			"error",
			"container name is empty or starts with a dash",
		)
		return result
	}
	// Inspect only the actual container's image ID and reference, never its env.
	data, stderr, err := d.run(
		ctx,
		command("", "container", "inspect", "--format", `{"id":{{json .Image}},"ref":{{json .Config.Image}}}`, s.Name),
	)
	if nil != err {
		level := slog.LevelWarn
		if errors.Is(err, context.Canceled) || context.Canceled == ctx.Err() {
			level = slog.LevelDebug
		}
		slog.Log(
			ctx,
			level,
			fmt.Sprintf(
				"Could not inspect Docker container %q for the update check of image %q: %v. %s",
				s.Name,
				s.Image,
				err,
				strings.TrimSpace(string(stderr)),
			),
			"runtime",
			"docker",
			"command",
			"docker container inspect",
			"container",
			s.Name,
			"image",
			s.Image,
			"error",
			err,
			"stderr",
			string(stderr),
		)
		return result
	}
	var container struct {
		ID  string `json:"id"`
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &container); nil != err {
		slog.Warn(
			fmt.Sprintf("Could not parse the Docker image inspection for container %q using image %q: %v", s.Name, s.Image, err),
			"runtime",
			"docker",
			"container",
			s.Name,
			"image",
			s.Image,
			"error",
			err,
		)
		return result
	}
	if "" == container.ID {
		slog.Warn(
			fmt.Sprintf(
				"Could not read the Docker image ID for container %q using image %q: container inspection is missing the image ID.",
				s.Name,
				s.Image,
			),
			"runtime",
			"docker",
			"container",
			s.Name,
			"image",
			s.Image,
			"error",
			"container inspection is missing the image ID",
		)
		return result
	}
	result.LocalID = container.ID
	if strings.Contains(container.Ref, "@") {
		result.Status, result.Reason = "unsupported", "pinned_digest"
		return result
	}
	if "" == container.Ref || strings.HasPrefix(container.Ref, "sha256:") || imageIDReference.MatchString(container.Ref) ||
		strings.HasPrefix(container.Ref, "-") {
		result.Status, result.Reason = "unsupported", "local_image"
		return result
	}
	data, stderr, err = d.run(
		ctx,
		command(
			"",
			"image",
			"inspect",
			"--format",
			`{"os":{{json .Os}},"architecture":{{json .Architecture}},"variant":{{json .Variant}},"os.version":{{json .OsVersion}},"digests":{{json .RepoDigests}}}`,
			container.ID,
		),
	)
	if nil != err {
		level := slog.LevelWarn
		if errors.Is(err, context.Canceled) || context.Canceled == ctx.Err() {
			level = slog.LevelDebug
		}
		slog.Log(
			ctx,
			level,
			fmt.Sprintf(
				"Could not inspect local Docker image %q (%s) for the update check of container %q: %v. %s",
				container.Ref,
				container.ID,
				s.Name,
				err,
				strings.TrimSpace(string(stderr)),
			),
			"runtime",
			"docker",
			"command",
			"docker image inspect",
			"container",
			s.Name,
			"image",
			container.Ref,
			"image_id",
			container.ID,
			"error",
			err,
			"stderr",
			string(stderr),
		)
		return result
	}
	var local struct {
		engine.ImagePlatform
		Digests []string `json:"digests"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &local); nil != err {
		slog.Warn(
			fmt.Sprintf("Could not parse local Docker image %q (%s) for container %q: %v", container.Ref, container.ID, s.Name, err),
			"runtime",
			"docker",
			"container",
			s.Name,
			"image",
			container.Ref,
			"image_id",
			container.ID,
			"error",
			err,
		)
		return result
	}
	// Failed registry lookup is never evidence that an image is locally built.
	if 0 == len(local.Digests) {
		result.Status, result.Reason = "unsupported", "local_image"
		return result
	}
	if "" == local.OS || "" == local.Architecture {
		result.Status, result.Reason = "unsupported", "platform_unknown"
		return result
	}
	result.Key = engine.ImageKey{Ref: d.ImageRef(container.Ref), ImagePlatform: local.ImagePlatform}
	result.Status, result.Reason = "", ""
	return result
}

func (d *Docker) ImageRef(ref string) string {
	ref = strings.TrimPrefix(ref, "index.docker.io/")
	ref = strings.TrimPrefix(ref, "docker.io/")
	first, _, _ := strings.Cut(ref, "/")
	if !strings.Contains(ref, "/") {
		ref = "docker.io/library/" + ref
	} else if !strings.ContainsAny(first, ".:") && "localhost" != first {
		ref = "docker.io/" + ref
	}
	if !strings.Contains(filepath.Base(ref), ":") {
		ref += ":latest"
	}
	return ref
}
