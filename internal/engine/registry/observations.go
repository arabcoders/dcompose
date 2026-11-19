package registry

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

type imageObservation struct {
	Registry   string                `json:"registry"`
	Repository string                `json:"repository"`
	Tag        string                `json:"tag"`
	Credential string                `json:"credential_fingerprint"`
	Digest     string                `json:"digest"`
	CheckedAt  time.Time             `json:"checked_at"`
	Manifest   manifest              `json:"manifest"`
	Platforms  []platformObservation `json:"platforms"`
}

type platformObservation struct {
	Platform engine.ImagePlatform `json:"platform"`
	Digest   string               `json:"digest,omitempty"`
	Status   string               `json:"status,omitempty"`
	Reason   string               `json:"reason,omitempty"`
}

// ImageObservations encodes completed metadata checks, without logins or tokens.
func (c *Client) ImageObservations() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	images := []imageObservation{}
	for key, state := range c.roots {
		root := state.observed
		if nil == root || 0 == len(root.platforms.images) {
			continue
		}
		platforms := make([]platformObservation, 0, len(root.platforms.images))
		for platform, result := range root.platforms.images {
			platforms = append(platforms, platformObservation{platform, result.Digest, result.Status, result.Reason})
		}
		slices.SortFunc(platforms, func(a, b platformObservation) int {
			return cmp.Or(
				cmp.Compare(a.Platform.OS, b.Platform.OS),
				cmp.Compare(a.Platform.Architecture, b.Platform.Architecture),
				cmp.Compare(a.Platform.Variant, b.Platform.Variant),
				cmp.Compare(a.Platform.OSVersion, b.Platform.OSVersion),
			)
		})
		images = append(images, imageObservation{
			key.registry, key.repository, key.tag, key.credential, root.digest, root.checkedAt, root.manifest, platforms,
		})
	}
	slices.SortFunc(images, func(a, b imageObservation) int {
		return cmp.Or(cmp.Compare(a.Registry, b.Registry), cmp.Compare(a.Repository, b.Repository),
			cmp.Compare(a.Tag, b.Tag), cmp.Compare(a.Credential, b.Credential))
	})
	return json.Marshal(images)
}

// RestoreImageObservations restores the original observation times. The caller's
// normal maxAge and credential checks still govern reuse.
func (c *Client) RestoreImageObservations(data []byte) error {
	var images []imageObservation
	if err := json.Unmarshal(data, &images); nil != err {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, image := range images {
		key := rootKey{image.Registry, image.Repository, image.Tag, image.Credential}
		platforms := &platformCache{
			images:  make(map[engine.ImagePlatform]engine.RegistryImage, len(image.Platforms)),
			pending: make(map[engine.ImagePlatform]*imageFlight),
		}
		for _, result := range image.Platforms {
			platforms.images[result.Platform] = engine.RegistryImage{Digest: result.Digest, Status: result.Status, Reason: result.Reason}
		}
		c.roots[key] = &rootState{observed: &rootObservation{
			manifest: image.Manifest, digest: image.Digest, checkedAt: image.CheckedAt, platforms: platforms,
		}}
	}
	return nil
}
