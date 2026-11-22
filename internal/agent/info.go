package agent

import (
	"context"
	"net/http"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

// nodeInfoFlight carries one in-flight node sample shared by overlapping /info
// requests. The result and its timestamp come from the original sample and are
// not cached for later requests.
type nodeInfoFlight struct {
	done chan struct{}
	info engine.NodeInfo
	err  error
}

// nodeInfoTimeout matches the server's node request budget for /info.
const nodeInfoTimeout = 15 * time.Second

func (a *agent) nodeInfo(ctx context.Context, w http.ResponseWriter) error {
	a.infoMu.Lock()
	flight := a.infoFlight
	if nil == flight {
		flight = &nodeInfoFlight{done: make(chan struct{})}
		a.infoFlight = flight
		a.infoMu.Unlock()
		// The sample runs on the agent's context, not the first caller's, so
		// one disconnecting browser cannot fail every overlapping request.
		sampleCtx, cancel := context.WithTimeout(a.ctx, nodeInfoTimeout)
		info, err := a.engine.NodeInfo(sampleCtx)
		cancel()
		if nil == err {
			info.CheckedAt = time.Now().UTC()
		}
		a.infoMu.Lock()
		flight.info, flight.err = info, err
		a.infoFlight = nil
		a.infoMu.Unlock()
		close(flight.done)
	} else {
		a.infoMu.Unlock()
		select {
		case <-flight.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if nil != flight.err {
		return flight.err
	}
	respond(w, http.StatusOK, flight.info)
	return nil
}
