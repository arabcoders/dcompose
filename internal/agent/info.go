package agent

import (
	"context"
	"net/http"
	"time"
)

func (a *agent) nodeInfo(ctx context.Context, w http.ResponseWriter) error {
	info, err := a.engine.NodeInfo(ctx)
	if nil != err {
		return err
	}
	info.CheckedAt = time.Now().UTC()
	respond(w, http.StatusOK, info)
	return nil
}
