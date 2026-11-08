package protocol

import "encoding/json"

// ChannelMessage multiplexes live traffic over agent /channel and browser /api/events.
type ChannelMessage struct {
	Type     string          `json:"type"`
	Node     string          `json:"node,omitempty"`
	Shell    string          `json:"shell,omitempty"`
	Stream   string          `json:"stream,omitempty"`
	Resource string          `json:"resource,omitempty"`
	Event    string          `json:"event,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Data     string          `json:"data,omitempty"`
	Cols     int             `json:"cols,omitempty"`
	Rows     int             `json:"rows,omitempty"`
	Error    string          `json:"error,omitempty"`
	Status   int             `json:"status,omitempty"`
}
