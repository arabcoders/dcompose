package registry

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

type cooldownKey struct{ registry, account string }
type cooldownError struct{ state engine.RegistryCooldown }

func (e *cooldownError) Error() string {
	return "registry metadata checks are paused until " + e.state.Until.Format(time.RFC3339)
}

func (c *Client) ApplyCooldown(incoming engine.RegistryCooldown) {
	incoming.Registry = canonicalHost(incoming.Registry)
	c.mu.Lock()
	defer c.mu.Unlock()
	key := cooldownKey{incoming.Registry, incoming.Account}
	current := c.cooldowns[key]
	if current.Until.After(incoming.Until) {
		incoming.Until = current.Until
	}
	if current.Attempts > incoming.Attempts {
		incoming.Attempts = current.Attempts
	}
	c.cooldowns[key] = incoming
}

func (c *Client) Cooldowns() []engine.RegistryCooldown {
	c.mu.Lock()
	defer c.mu.Unlock()
	states := make([]engine.RegistryCooldown, 0, len(c.cooldowns))
	for _, state := range c.cooldowns {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].Registry == states[j].Registry {
			return states[i].Account < states[j].Account
		}
		return states[i].Registry < states[j].Registry
	})
	return states
}

func (c *Client) activeCooldown(registry, account string) *engine.RegistryCooldown {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.cooldowns[cooldownKey{registry, account}]
	if ok && state.Until.After(c.Now()) {
		return &state
	}
	return nil
}

func (l *lookup) success() {
	l.client.mu.Lock()
	defer l.client.mu.Unlock()
	key := cooldownKey{l.ref.registry, l.login.account()}
	// Do not remove a concurrently received fleet deadline or local 429.
	if state, ok := l.client.cooldowns[key]; ok && !state.Until.After(l.client.Now()) {
		delete(l.client.cooldowns, key)
	}
}

func (l *lookup) rateLimited(ctx context.Context, resp *http.Response) error {
	retry := resp.Header.Get("Retry-After")
	cause := l.safe(responseError(resp).Error())
	c := l.client
	c.mu.Lock()
	key := cooldownKey{l.ref.registry, l.login.account()}
	previous := c.cooldowns[key]
	now := c.Now()
	attempts := previous.Attempts + 1
	until, valid := retryAfter(retry, now)
	if !valid {
		delay := 5 * time.Minute
		for i := 1; i < attempts && delay < time.Hour; i++ {
			delay *= 2
		}
		if time.Hour < delay {
			delay = time.Hour
		}
		until = now.Add(delay)
	}
	if previous.Until.After(until) {
		until = previous.Until
	}
	state := engine.RegistryCooldown{Registry: l.ref.registry, Account: key.account, Until: until, Attempts: attempts}
	c.cooldowns[key] = state
	c.mu.Unlock()
	if until.After(previous.Until) {
		login := "anonymous access"
		if "" != l.login.Username {
			login = fmt.Sprintf("login %q", l.login.Username)
		} else if "" != key.account {
			login = "the configured token login"
		}
		level := slog.LevelWarn
		if nil != ctx.Err() {
			level = slog.LevelDebug
		}
		slog.Log(ctx, level, fmt.Sprintf("Registry %q rate-limited %s while checking image %q: %s. Metadata checks for this registry/login will retry after %s.", l.ref.registry, login, l.key.Ref, cause, until.UTC().Format(time.RFC3339)), "image", l.key.Ref, "registry", l.ref.registry, "error", cause, "retry_at", until)
	}
	return &cooldownError{state: state}
}

func retryAfter(value string, now time.Time) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); nil == err && 0 <= seconds && seconds <= int64((time.Duration(1<<63-1))/time.Second) {
		return now.Add(time.Duration(seconds) * time.Second), true
	}
	if until, err := http.ParseTime(value); nil == err {
		if until.Before(now) {
			until = now
		}
		return until, true
	}
	return time.Time{}, false
}
