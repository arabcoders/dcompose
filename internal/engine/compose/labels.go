package compose

import (
	"encoding/json"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

// ApplyLabels decodes only scheduling metadata, never arbitrary container config.
func ApplyLabels(service *engine.Service, data []byte) error {
	var labels struct {
		CheckInterval string `json:"io.dcompose.check-interval"`
		AutoUpdate    string `json:"io.dcompose.auto-update"`
	}
	if err := json.Unmarshal(data, &labels); nil != err {
		return err
	}
	if interval, err := time.ParseDuration(labels.CheckInterval); nil == err && time.Minute <= interval {
		service.CheckInterval = labels.CheckInterval
	}
	service.AutoUpdate = "true" == labels.AutoUpdate
	return nil
}
