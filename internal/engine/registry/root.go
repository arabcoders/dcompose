package registry

import (
	"context"
	"net/http"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

type rootKey struct{ registry, repository, tag, credential string }

type rootState struct {
	observed *rootObservation
	pending  *rootFlight
}

// Root observations are immutable. An unchanged HEAD shares the verified
// platform mappings but creates a new observation time; hits never renew it.
type rootObservation struct {
	manifest  manifest
	digest    string
	checkedAt time.Time
	platforms *platformCache
}

type platformCache struct {
	images  map[engine.ImagePlatform]engine.RegistryImage
	pending map[engine.ImagePlatform]*imageFlight
}

type rootFlight struct {
	done        chan struct{}
	observation *rootObservation
	err         error
}

type imageFlight struct {
	done   chan struct{}
	result engine.RegistryImage
	err    error
}

func (l *lookup) root(ctx context.Context, maxAge time.Duration) (*rootObservation, error) {
	c := l.client
	key := rootKey{l.ref.registry, l.ref.repository, l.ref.tag, l.login.fingerprint()}
	c.mu.Lock()
	state := c.roots[key]
	if nil == state {
		state = &rootState{}
		c.roots[key] = state
	}
	if 0 < maxAge && nil != state.observed && c.Now().Before(state.observed.checkedAt.Add(maxAge)) {
		observation := state.observed
		c.mu.Unlock()
		return observation, nil
	}
	if pending := state.pending; nil != pending {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending.done:
			return pending.observation, pending.err
		}
	}
	pending := &rootFlight{done: make(chan struct{})}
	state.pending = pending
	previous := state.observed
	c.mu.Unlock()

	observation, err := l.refreshRoot(ctx, previous)
	err = l.sharedError(err)
	c.mu.Lock()
	if nil == err {
		state.observed = observation
	}
	pending.observation, pending.err = observation, err
	state.pending = nil
	close(pending.done)
	c.mu.Unlock()
	return observation, err
}

func (l *lookup) refreshRoot(ctx context.Context, previous *rootObservation) (*rootObservation, error) {
	resp, err := l.request(ctx, http.MethodHead, l.endpoint("manifests", l.ref.tag))
	if nil != err {
		return nil, err
	}
	digest := ""
	if http.StatusOK == resp.StatusCode {
		digest = resp.Header.Get("Docker-Content-Digest")
		_ = resp.Body.Close()
	} else if http.StatusMethodNotAllowed == resp.StatusCode || http.StatusNotImplemented == resp.StatusCode {
		_ = resp.Body.Close()
	} else {
		return nil, responseError(resp)
	}
	if imageDigest.MatchString(l.ref.tag) && "" != digest && l.ref.tag != digest {
		return nil, &manifestError{cause: "registry HEAD digest does not match the requested manifest digest"}
	}
	if imageDigest.MatchString(digest) && nil != previous && previous.digest == digest {
		return &rootObservation{manifest: previous.manifest, digest: digest, checkedAt: l.client.Now(), platforms: previous.platforms}, nil
	}
	root, digest, err := l.fetchManifest(ctx, l.ref.tag)
	if nil != err {
		return nil, err
	}
	platforms := &platformCache{
		images:  make(map[engine.ImagePlatform]engine.RegistryImage),
		pending: make(map[engine.ImagePlatform]*imageFlight),
	}
	if nil != previous && previous.digest == digest {
		platforms = previous.platforms
	}
	return &rootObservation{manifest: root, digest: digest, checkedAt: l.client.Now(), platforms: platforms}, nil
}
