package podman

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

var imageDigest = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var imageIDReference = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

// ImageChanged compares the container's image with its local reference after pull.
func (p *Podman) ImageChanged(ctx context.Context, s engine.Service) (bool, error) {
	data, stderr, err := p.run(ctx, p.command("", "container", "inspect", "--format", `{"id":{{json .Image}},"ref":{{json .ImageName}}}`, s.Name))
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
	data, stderr, err = p.run(ctx, p.command("", "image", "inspect", "--format", "{{.Id}}", container.Ref))
	if nil != err {
		return false, fmt.Errorf("inspect pulled image %s: %w: %s", container.Ref, err, stderr)
	}
	id := strings.TrimSpace(string(data))
	if "" == id {
		return false, fmt.Errorf("image %s is missing its ID", container.Ref)
	}
	return strings.TrimPrefix(container.ID, "sha256:") != strings.TrimPrefix(id, "sha256:"), nil
}

func (p *Podman) InspectImage(ctx context.Context, s engine.Service) engine.ImageTarget {
	result := engine.ImageTarget{Status: "error", Reason: "inspect_error"}
	if "" == s.Name || strings.HasPrefix(s.Name, "-") {
		slog.Warn(fmt.Sprintf("Could not check Podman image %q for container %q: container name is empty or starts with a dash.", s.Image, s.Name), "runtime", "podman", "container", s.Name, "image", s.Image, "error", "container name is empty or starts with a dash")
		return result
	}
	data, stderr, err := p.run(ctx, p.command("", "container", "inspect", "--format", `{"id":{{json .Image}},"ref":{{json .ImageName}}}`, s.Name))
	if nil != err {
		level := slog.LevelWarn
		if errors.Is(err, context.Canceled) || context.Canceled == ctx.Err() {
			level = slog.LevelDebug
		}
		slog.Log(ctx, level, fmt.Sprintf("Could not inspect Podman container %q for the update check of image %q: %v. %s", s.Name, s.Image, err, strings.TrimSpace(string(stderr))), "runtime", "podman", "command", "podman container inspect", "container", s.Name, "image", s.Image, "error", err, "stderr", string(stderr))
		return result
	}
	var container struct {
		ID  string `json:"id"`
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &container); nil != err {
		slog.Warn(fmt.Sprintf("Could not parse the Podman image inspection for container %q using image %q: %v", s.Name, s.Image, err), "runtime", "podman", "container", s.Name, "image", s.Image, "error", err)
		return result
	}
	result.LocalID = container.ID
	if !strings.Contains(container.ID, ":") {
		result.LocalID = "sha256:" + container.ID
	}
	if !imageDigest.MatchString(result.LocalID) {
		slog.Warn(fmt.Sprintf("Could not read the Podman image ID for container %q using image %q: container inspection is missing a valid image ID.", s.Name, s.Image), "runtime", "podman", "container", s.Name, "image", s.Image, "error", "container inspection is missing a valid image ID")
		return result
	}
	if strings.Contains(container.Ref, "@") {
		result.Status, result.Reason = "unsupported", "pinned_digest"
		return result
	}
	if "" == container.Ref || strings.HasPrefix(container.Ref, "sha256:") || imageIDReference.MatchString(container.Ref) || strings.HasPrefix(container.Ref, "-") {
		result.Status, result.Reason = "unsupported", "local_image"
		return result
	}
	// Podman 4.x does not expose Variant/OsVersion. Decode optional fields from
	// newer versions without templates that fail on older structs. Never log or
	// return the image's Config (which can contain environment variables).
	data, stderr, err = p.run(ctx, p.command("", "image", "inspect", container.ID))
	if nil != err {
		level := slog.LevelWarn
		if errors.Is(err, context.Canceled) || context.Canceled == ctx.Err() {
			level = slog.LevelDebug
		}
		slog.Log(ctx, level, fmt.Sprintf("Could not inspect local Podman image %q (%s) for the update check of container %q: %v. %s", container.Ref, container.ID, s.Name, err, strings.TrimSpace(string(stderr))), "runtime", "podman", "command", "podman image inspect", "container", s.Name, "image", container.Ref, "image_id", container.ID, "error", err, "stderr", string(stderr))
		return result
	}
	var images []struct {
		engine.ImagePlatform
		Tags    []string `json:"RepoTags"`
		Digests []string `json:"RepoDigests"`
	}
	if err := json.Unmarshal(data, &images); nil != err {
		slog.Warn(fmt.Sprintf("Could not parse local Podman image %q (%s) for container %q: %v", container.Ref, container.ID, s.Name, err), "runtime", "podman", "container", s.Name, "image", container.Ref, "image_id", container.ID, "error", err)
		return result
	}
	if 1 != len(images) {
		slog.Warn(fmt.Sprintf("Could not read local Podman image %q (%s) for container %q: inspection returned %d images instead of exactly one image.", container.Ref, container.ID, s.Name, len(images)), "runtime", "podman", "container", s.Name, "image", container.Ref, "image_id", container.ID, "error", "image inspection must contain exactly one image", "image_count", len(images))
		return result
	}
	local := images[0]
	if 0 == len(local.Digests) {
		result.Status, result.Reason = "unsupported", "local_image"
		return result
	}
	// Without a reported variant, ARM32 cannot distinguish v5/v6/v7.
	// ARM64's empty variant uses the same v8 baseline as the registry matcher.
	if "" == local.OS || "" == local.Architecture || ("arm" == local.Architecture && "" == local.Variant) || ("windows" == local.OS && "" == local.OSVersion) {
		result.Status, result.Reason = "unsupported", "platform_unknown"
		return result
	}
	ref := p.ImageRef(container.Ref)
	if !qualifiedImage(ref) {
		// Short-name search registries and aliases are user-configured. Resolve
		// against this image's actual names, never implicitly against Docker Hub.
		resolved := make(map[string]bool)
		for _, tag := range local.Tags {
			tag = p.ImageRef(tag)
			if qualifiedImage(tag) && strings.HasSuffix(tag, "/"+ref) {
				resolved[tag] = true
			}
		}
		if 1 != len(resolved) {
			result.Status, result.Reason = "unsupported", "local_image"
			return result
		}
		for tag := range resolved {
			ref = tag
		}
	}
	result.Key = engine.ImageKey{Ref: ref, ImagePlatform: local.ImagePlatform}
	result.Status, result.Reason = "", ""
	return result
}

func (p *Podman) ImageRef(ref string) string {
	ref = strings.TrimPrefix(ref, "docker://")
	if strings.HasPrefix(ref, "index.docker.io/") {
		ref = "docker.io/" + strings.TrimPrefix(ref, "index.docker.io/")
	}
	if strings.HasPrefix(ref, "docker.io/") && 1 == strings.Count(ref, "/") {
		ref = "docker.io/library/" + strings.TrimPrefix(ref, "docker.io/")
	}
	if "" != ref && !strings.Contains(ref, "@") && !strings.Contains(filepath.Base(ref), ":") {
		ref += ":latest"
	}
	return ref
}

func qualifiedImage(ref string) bool {
	first, _, slash := strings.Cut(ref, "/")
	return slash && (strings.ContainsAny(first, ".:") || "localhost" == first)
}
