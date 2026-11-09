package compose

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
)

func TestContainerInfo(t *testing.T) {
	for _, test := range []struct {
		name, data, created, started string
	}{
		{"running", `{"labels":{"io.dcompose.check-interval":"30m","io.dcompose.auto-update":"true"},"created_at":"2026-11-03T02:39:58.237+02:00","started_at":"2026-11-03T02:39:58.706+02:00"}`, "2026-11-03T00:39:58.237Z", "2026-11-03T00:39:58.706Z"},
		{"restarted", `{"created_at":"2026-11-03T00:39:58Z","started_at":"2026-11-03T03:45:00.123456789Z"}`, "2026-11-03T00:39:58Z", "2026-11-03T03:45:00.123456789Z"},
		{"never_started", `{"created_at":"2026-11-03T00:39:58Z","started_at":"0001-01-01T00:00:00Z","labels":null}`, "2026-11-03T00:39:58Z", ""},
		{"missing", `{}`, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := engine.Service{CreatedAt: "stale Compose date", StartedAt: "old start"}
			if err := ApplyContainerInfo(&service, []byte(test.data)); nil != err {
				t.Fatal(err)
			}
			if test.created != service.CreatedAt || test.started != service.StartedAt {
				t.Fatalf("times: %+v", service)
			}
			if "running" == test.name && ("30m" != service.CheckInterval || !service.AutoUpdate) {
				t.Fatalf("labels: %+v", service)
			}
			data, err := json.Marshal(service)
			if nil != err {
				t.Fatal(err)
			}
			if "" == test.started && strings.Contains(string(data), `"started_at"`) {
				t.Fatalf("unset start escaped: %s", data)
			}
		})
	}
}

func TestContainerInfoErrors(t *testing.T) {
	for _, data := range []string{`{`, `{"created_at":"invalid"}`, `{"started_at":"invalid"}`, `{"labels":{"io.dcompose.auto-update":true}}`} {
		if err := ApplyContainerInfo(&engine.Service{}, []byte(data)); nil == err {
			t.Fatalf("accepted invalid metadata: %s", data)
		}
	}
}

func TestContainerInfoFormat(t *testing.T) {
	created := time.Date(2026, 11, 3, 2, 39, 58, 237000000, time.FixedZone("node", 7200))
	started := created.Add(469 * time.Millisecond)
	for _, native := range []bool{false, true} {
		var container struct {
			Created any
			State   struct{ StartedAt any }
			Config  struct{ Labels map[string]string }
		}
		if native {
			container.Created, container.State.StartedAt = created, started
		} else {
			container.Created, container.State.StartedAt = created.Format(time.RFC3339Nano), started.Format(time.RFC3339Nano)
		}
		container.Config.Labels = map[string]string{"io.dcompose.auto-update": "true", "private": "private-label-value"}
		format := template.Must(template.New("inspect").Funcs(template.FuncMap{"json": func(value any) (string, error) {
			data, err := json.Marshal(value)
			return string(data), err
		}}).Parse(ContainerInfoFormat))
		var output bytes.Buffer
		if err := format.Execute(&output, container); nil != err {
			t.Fatal(err)
		}
		var service engine.Service
		if err := ApplyContainerInfo(&service, output.Bytes()); nil != err {
			t.Fatal(err)
		}
		if "2026-11-03T00:39:58.237Z" != service.CreatedAt || "2026-11-03T00:39:58.706Z" != service.StartedAt || !service.AutoUpdate {
			t.Fatalf("native %t: %+v", native, service)
		}
		encoded, err := json.Marshal(service)
		if nil != err || strings.Contains(string(encoded), "private-label-value") {
			t.Fatalf("arbitrary labels escaped: %s %v", encoded, err)
		}
	}
}
