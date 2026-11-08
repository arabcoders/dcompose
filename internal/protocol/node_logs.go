package protocol

import (
	"encoding/json"
	"log/slog"
	"time"
)

// NodeLog carries an operational log, separate from runtime and container logs.
// Identity comes from the authenticated channel, not from the record.
type NodeLog struct {
	Time    time.Time                  `json:"time"`
	Level   slog.Level                 `json:"level"`
	Message string                     `json:"msg"`
	Source  *slog.Source               `json:"source,omitempty"`
	Attrs   map[string]json.RawMessage `json:"attrs,omitempty"`
}

// NodeLogs is a best-effort batch of node operational logs.
type NodeLogs struct {
	Entries []NodeLog `json:"entries"`
}
