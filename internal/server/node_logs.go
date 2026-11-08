package server

import (
	"bytes"
	"encoding/json"
	"log/slog"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func (s *Server) nodeLogs(channel *nodeChannel, payload json.RawMessage) error {
	var batch protocol.NodeLogs
	if err := json.Unmarshal(payload, &batch); nil != err {
		return err
	}
	logger := slog.Default()
	for _, entry := range batch.Entries {
		if !logger.Enabled(channel.ctx, entry.Level) {
			continue
		}
		record := slog.NewRecord(entry.Time, entry.Level, entry.Message, 0)
		for key, raw := range entry.Attrs {
			switch key {
			case "node_id", "node_name", "_from_node", "_node_source":
				continue
			}
			// interface{} is the JSON boundary: attributes may contain any native
			// JSON value. UseNumber retains integer diagnostic fields exactly.
			var value interface{}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&value); nil != err {
				return err
			}
			record.AddAttrs(slog.Any(key, value))
		}
		if nil != entry.Source {
			record.AddAttrs(slog.Any("_node_source", entry.Source))
		}
		record.AddAttrs(slog.String("node_id", channel.id), slog.String("node_name", channel.node.Name), slog.Bool("_from_node", true))
		_ = logger.Handler().Handle(channel.ctx, record)
	}
	return nil
}
