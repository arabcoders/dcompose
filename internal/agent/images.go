package agent

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func (a *agent) inspectImages(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	var input protocol.ImageInspectRequest
	if err := decodeBody(r, &input); nil != err {
		return err
	}
	dir, err := a.stackPath(input.Stack, false)
	if nil != err {
		return err
	}
	if _, err := composeFile(dir); nil != err {
		return err
	}
	services, err := a.containers(ctx, input.Stack, dir)
	if nil != err {
		return err
	}
	images := make([]protocol.ImageInspection, 0, len(services))
	for _, service := range services {
		if "" != input.Container && input.Container != service.Name {
			continue
		}
		images = append(images, protocol.ImageInspection{
			ID: service.ID, Service: service, Target: a.engine.InspectImage(ctx, service),
		})
	}
	if "" != input.Container && 0 == len(images) {
		return fault(http.StatusNotFound, "container not found in stack")
	}
	respond(w, http.StatusOK, protocol.ImageInspectResponse{Images: images})
	return nil
}

func (a *agent) invalidateImages(name string, refs map[string]bool) {
	if nil == refs {
		refs = make(map[string]bool)
	}
	if dir, err := a.stackPath(name, false); nil == err {
		for ref := range a.stackImageRefs(name, dir) {
			refs[ref] = true
		}
	}
	h := a.events
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, service := range h.stacks[name].Services {
		if "" != service.Image {
			refs[a.engine.ImageRef(service.Image)] = true
		}
	}
	normalized := make(map[string]bool, len(refs))
	for ref := range refs {
		if "" != ref {
			normalized[a.engine.ImageRef(ref)] = true
		}
	}
	current := make([]string, 0, len(normalized))
	for ref := range normalized {
		current = append(current, ref)
	}
	sort.Strings(current)
	h.publish(event("images.invalidated", protocol.ImageInvalidated{Stack: name, Refs: current}))
}

func (a *agent) stackImageRefs(name, dir string) map[string]bool {
	refs := make(map[string]bool)
	stack, err := a.engineStack(name, dir)
	if nil != err {
		return refs
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	refs, _ = a.engine.ImageRefs(ctx, stack)
	return refs
}
