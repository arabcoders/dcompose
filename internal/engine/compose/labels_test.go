package compose

import (
	"testing"

	"github.com/arabcoders/dcompose/internal/engine"
)

func TestSchedulingLabels(t *testing.T) {
	for _, test := range []struct {
		data, interval string
		auto           bool
	}{
		{`{"io.dcompose.check-interval":"30m","io.dcompose.auto-update":"true","private":"ignored"}`, "30m", true},
		{`{"io.dcompose.check-interval":"1m","io.dcompose.auto-update":"false"}`, "1m", false},
		{`{"io.dcompose.check-interval":"59s","io.dcompose.auto-update":"TRUE"}`, "", false},
		{`{"io.dcompose.check-interval":"invalid"}`, "", false},
		{`null`, "", false},
	} {
		var service engine.Service
		if err := ApplyLabels(
			&service,
			[]byte(test.data),
		); nil != err || test.interval != service.CheckInterval ||
			test.auto != service.AutoUpdate {
			t.Fatalf("labels: %+v %v", service, err)
		}
	}
}
