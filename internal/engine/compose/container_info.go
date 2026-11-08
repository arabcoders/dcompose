package compose

import (
	"encoding/json"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

// ContainerInfoFormat selects only scheduling labels and absolute container times.
const ContainerInfoFormat = `{"labels":{{json .Config.Labels}},"created_at":{{json .Created}},"started_at":{{json .State.StartedAt}}}`

// ApplyContainerInfo normalizes Docker and Podman inspection times for the API.
func ApplyContainerInfo(service *engine.Service, data []byte) error {
	var info struct {
		Labels    json.RawMessage `json:"labels"`
		CreatedAt time.Time       `json:"created_at"`
		StartedAt time.Time       `json:"started_at"`
	}
	if err := json.Unmarshal(data, &info); nil != err {
		return err
	}
	if nil != info.Labels {
		if err := ApplyLabels(service, info.Labels); nil != err {
			return err
		}
	}
	service.CreatedAt, service.StartedAt = "", ""
	if !info.CreatedAt.IsZero() {
		service.CreatedAt = info.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if !info.StartedAt.IsZero() {
		service.StartedAt = info.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	return nil
}
