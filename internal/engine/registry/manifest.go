package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

const (
	ociIndex        = "application/vnd.oci.image.index.v1+json"
	dockerList      = "application/vnd.docker.distribution.manifest.list.v2+json"
	ociManifest     = "application/vnd.oci.image.manifest.v1+json"
	dockerManifest  = "application/vnd.docker.distribution.manifest.v2+json"
	manifestAccept  = ociIndex + ", " + dockerList + ", " + ociManifest + ", " + dockerManifest
	maxMetadataSize = 8 << 20
)

type descriptor struct {
	Digest   string                `json:"digest"`
	Platform *engine.ImagePlatform `json:"platform"`
}

type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Manifests     []descriptor `json:"manifests"`
	Config        descriptor   `json:"config"`
}

func contentDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (l *lookup) inspect(ctx context.Context, maxAge time.Duration) (engine.RegistryImage, error) {
	if err := ctx.Err(); nil != err {
		return manifestFailure(err), err
	}
	root, err := l.root(ctx, maxAge)
	if nil != err {
		return manifestFailure(err), err
	}
	result, err := l.platformImage(ctx, root)
	result.CheckedAt = root.checkedAt
	if nil == err {
		l.success()
	} else {
		result.CheckedAt = l.client.Now()
	}
	return result, err
}

// platformImage joins identical platform reads while different platforms share
// the immutable root observation and fetch only their own selected metadata.
func (l *lookup) platformImage(ctx context.Context, root *rootObservation) (engine.RegistryImage, error) {
	c := l.client
	platform := l.key.ImagePlatform
	c.mu.Lock()
	if result, ok := root.platforms.images[platform]; ok {
		c.mu.Unlock()
		return result, nil
	}
	if pending := root.platforms.pending[platform]; nil != pending {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return manifestFailure(ctx.Err()), ctx.Err()
		case <-pending.done:
			return pending.result, pending.err
		}
	}
	pending := &imageFlight{done: make(chan struct{})}
	root.platforms.pending[platform] = pending
	c.mu.Unlock()

	result, err := l.inspectManifest(ctx, root.manifest)
	err = l.sharedError(err)
	c.mu.Lock()
	if nil == err {
		root.platforms.images[platform] = result
	}
	pending.result, pending.err = result, err
	delete(root.platforms.pending, platform)
	close(pending.done)
	c.mu.Unlock()
	return result, err
}

func (l *lookup) inspectManifest(ctx context.Context, root manifest) (engine.RegistryImage, error) {
	selected := root
	switch root.MediaType {
	case ociIndex, dockerList:
		var matches []descriptor
		for _, candidate := range root.Manifests {
			if nil != candidate.Platform && platformMatches(*candidate.Platform, l.key.ImagePlatform) {
				matches = append(matches, candidate)
			}
		}
		if 1 != len(matches) {
			reason := "platform_missing"
			if 1 < len(matches) {
				reason = "platform_ambiguous"
			}
			return engine.RegistryImage{Status: "unsupported", Reason: reason}, nil
		}
		child := matches[0]
		if !imageDigest.MatchString(child.Digest) {
			return invalidManifest("selected platform descriptor is missing a valid manifest digest")
		}
		var err error
		selected, _, err = l.fetchManifest(ctx, child.Digest)
		if nil != err {
			return manifestFailure(err), err
		}
	case ociManifest, dockerManifest:
		// A single manifest has no platform descriptor. Read only its config,
		// never any layer, to validate the requested platform.
	default:
		return invalidManifest("registry returned an unsupported manifest media type")
	}
	if ociManifest != selected.MediaType && dockerManifest != selected.MediaType {
		return invalidManifest("selected platform is not an OCI or schema2 image manifest")
	}
	if !imageDigest.MatchString(selected.Config.Digest) {
		return invalidManifest("manifest is missing a valid config digest")
	}
	if ociManifest == root.MediaType || dockerManifest == root.MediaType {
		data, _, _, err := l.fetch(ctx, l.endpoint("blobs", selected.Config.Digest), selected.Config.Digest)
		if nil != err {
			return manifestFailure(err), err
		}
		var platform engine.ImagePlatform
		if err := json.Unmarshal(data, &platform); nil != err {
			return invalidManifest(fmt.Sprintf("could not parse image config: %v", err))
		}
		if "" == platform.OS || "" == platform.Architecture {
			return invalidManifest("image config is missing the operating system or architecture")
		}
		if !platformMatches(platform, l.key.ImagePlatform) {
			return engine.RegistryImage{Status: "unsupported", Reason: "platform_missing"}, nil
		}
	}
	return engine.RegistryImage{Digest: selected.Config.Digest}, nil
}

func platformMatches(a, b engine.ImagePlatform) bool {
	variant := func(p engine.ImagePlatform) string {
		if "arm64" == p.Architecture && "v8" == p.Variant {
			return ""
		}
		return p.Variant
	}
	return "" != a.OS && "" != a.Architecture && a.OS == b.OS && a.Architecture == b.Architecture && a.OSVersion == b.OSVersion && variant(a) == variant(b)
}

type manifestError struct{ cause string }

func (e *manifestError) Error() string { return e.cause }
func invalidManifest(cause string) (engine.RegistryImage, error) {
	return engine.RegistryImage{Status: "error", Reason: "manifest_invalid"}, &manifestError{cause: cause}
}
func manifestFailure(err error) engine.RegistryImage {
	if _, ok := errors.AsType[*manifestError](err); ok {
		return engine.RegistryImage{Status: "error", Reason: "manifest_invalid"}
	}
	return engine.RegistryImage{Status: "error", Reason: "registry_error"}
}

func (l *lookup) fetchManifest(ctx context.Context, ref string) (manifest, string, error) {
	expected := ""
	if imageDigest.MatchString(ref) {
		expected = ref
	}
	data, digest, mediaType, err := l.fetch(ctx, l.endpoint("manifests", ref), expected)
	if nil != err {
		return manifest{}, "", err
	}
	var document manifest
	if err := json.Unmarshal(data, &document); nil != err {
		return manifest{}, "", &manifestError{cause: fmt.Sprintf("could not parse registry manifest: %v", err)}
	}
	if 2 != document.SchemaVersion {
		return manifest{}, "", &manifestError{cause: "registry manifest must use schema version 2"}
	}
	if "" == document.MediaType {
		document.MediaType = mediaType
	}
	return document, digest, nil
}

func (l *lookup) fetch(ctx context.Context, endpoint, expected string) ([]byte, string, string, error) {
	resp, err := l.request(ctx, http.MethodGet, endpoint)
	if nil != err {
		return nil, "", "", err
	}
	if http.StatusOK != resp.StatusCode {
		return nil, "", "", responseError(resp)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataSize+1))
	if nil != err {
		return nil, "", "", fmt.Errorf("could not read registry metadata: %w", err)
	}
	if maxMetadataSize < len(data) {
		return nil, "", "", &manifestError{cause: "registry metadata exceeds the 8 MiB read limit"}
	}
	digest := contentDigest(data)
	if "" != expected && expected != digest {
		return nil, "", "", &manifestError{cause: "registry content verification failed for digest " + expected}
	}
	if header := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest")); "" != header && header != digest {
		return nil, "", "", &manifestError{cause: "registry content does not match its Docker-Content-Digest header"}
	}
	mediaType, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	return data, digest, strings.TrimSpace(mediaType), nil
}
