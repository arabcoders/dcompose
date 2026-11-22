package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"syscall"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

// statsSampler fans one on-demand sampling loop out to every attached stream,
// so concurrent viewers of a stack share a single engine query per interval.
// The loop stops when sampling fails or the last stream detaches.
type statsSampler struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	// err is the terminal sampling failure, set before done closes.
	err error

	// subs is guarded by agent.mu.
	subs map[*eventSubscriber]struct{}
}

func (a *agent) subscribeStats(name string) (*statsSampler, *eventSubscriber) {
	sub := newSubscriber()
	a.mu.Lock()
	sampler, ok := a.samplers[name]
	if !ok {
		ctx, cancel := context.WithCancel(a.ctx)
		sampler = &statsSampler{
			ctx: ctx, cancel: cancel, done: make(chan struct{}), subs: make(map[*eventSubscriber]struct{}),
		}
		a.samplers[name] = sampler
		a.work.Add(1)
		go a.sampleStats(name, sampler)
	}
	sampler.subs[sub] = struct{}{}
	a.mu.Unlock()
	return sampler, sub
}

func (a *agent) unsubscribeStats(name string, sampler *statsSampler, sub *eventSubscriber) {
	a.mu.Lock()
	delete(sampler.subs, sub)
	if 0 == len(sampler.subs) && sampler == a.samplers[name] {
		delete(a.samplers, name)
		sampler.cancel()
	}
	a.mu.Unlock()
}

// watchStats forwards shared samples to one stream until it detaches, its
// subscriber stalls, or sampling fails. Releasing the subscriber share stops
// the sampler for the last stream, including a stalled one.
func (a *agent) watchStats(
	ctx context.Context,
	name string,
	sampler *statsSampler,
	sub *eventSubscriber,
	send func(streamEvent) bool,
) error {
	defer a.unsubscribeStats(name, sampler, sub)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.done:
			// The stream stopped reading. Surface a retryable failure.
			return fault(502, "stack statistics stream stalled")
		case e := <-sub.events:
			if !send(e) {
				return nil
			}
		case <-sampler.done:
			// Queued samples stay ahead of the terminal failure.
			for {
				select {
				case e := <-sub.events:
					if !send(e) {
						return nil
					}
				default:
					return sampler.err
				}
			}
		}
	}
}

func (a *agent) sampleStats(name string, sampler *statsSampler) {
	defer a.work.Done()
	defer sampler.cancel()
	defer close(sampler.done)
	defer a.releaseStatsSampler(name, sampler)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if nil != sampler.ctx.Err() {
			return
		}
		dir, err := a.stackPath(name, false)
		var stack engine.Stack
		if nil == err {
			stack, err = a.engineStack(name, dir)
		}
		var containers []engine.ContainerStats
		if nil == err {
			sampleCtx, cancel := context.WithTimeout(sampler.ctx, 10*time.Second)
			containers, err = a.engine.Stats(sampleCtx, stack)
			cancel()
		}
		if nil != sampler.ctx.Err() {
			return
		}
		switch {
		case errors.Is(err, engine.ErrStatsContainerGone):
			// The next sample will discover any replacement containers.
		case nil != err:
			code := 502
			if apiErr, ok := errors.AsType[*apiError](err); ok {
				code = apiErr.code
			}
			slog.Error(
				fmt.Sprintf(
					"Could not read %s resource statistics for stack %q on node %q: %s",
					a.config.Global.Engine,
					name,
					a.config.Global.NodeID,
					statsFailureCause(err),
				),
			)
			sampler.err = fault(code, "stack statistics unavailable")
			return
		default:
			if nil == containers {
				containers = []engine.ContainerStats{}
			}
			e := event("stack.stats", struct {
				Containers []engine.ContainerStats `json:"containers"`
			}{containers})
			a.mu.Lock()
			for sub := range sampler.subs {
				sub.send(e)
				select {
				case <-sub.done:
					delete(sampler.subs, sub)
				default:
				}
			}
			a.mu.Unlock()
		}
		select {
		case <-sampler.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *agent) releaseStatsSampler(name string, sampler *statsSampler) {
	a.mu.Lock()
	if sampler == a.samplers[name] {
		delete(a.samplers, name)
	}
	a.mu.Unlock()
}

// Compose stderr can contain interpolated secrets. Log typed system/parse causes
// rather than raw command output or unclassified runtime errors.
func statsFailureCause(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded.Error()
	}
	if cause, ok := errors.AsType[*exec.ExitError](err); ok {
		return cause.Error()
	}
	if cause, ok := errors.AsType[syscall.Errno](err); ok {
		return cause.Error()
	}
	if cause, ok := errors.AsType[*json.SyntaxError](err); ok {
		return cause.Error()
	}
	if cause, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return cause.Error()
	}
	if cause, ok := errors.AsType[*engine.Error](err); ok {
		return cause.Message
	}
	if cause, ok := errors.AsType[*apiError](err); ok {
		return cause.Error()
	}
	return "container engine request failed"
}
