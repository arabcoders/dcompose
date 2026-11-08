package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/protocol"
)

type infoEngine struct {
	engine.Driver
	t     *testing.T
	err   error
	calls int
}

func (e *infoEngine) NodeInfo(ctx context.Context) (engine.NodeInfo, error) {
	e.calls++
	deadline, ok := ctx.Deadline()
	if !ok || 30*time.Second < time.Until(deadline) || 29*time.Second > time.Until(deadline) {
		e.t.Fatalf("node info deadline: %v, set: %t", deadline, ok)
	}
	return engine.NodeInfo{
		Runtime:    engine.RuntimeInfo{Name: "home", Version: "27.5.1", CPUs: 8, MemoryBytes: 16777216000},
		Containers: []engine.ContainerStats{{ID: "container", Name: "web", CPU: "2.50%", Memory: "12MiB / 1GiB"}},
	}, e.err
}

func TestNodeInfo(t *testing.T) {
	a, fake := fixture(t)
	driver := &infoEngine{t: t}
	a.engine = driver
	before := time.Now().UTC()
	w := request(a, http.MethodGet, "/info?command=destroy", "")
	var info engine.NodeInfo
	if err := json.Unmarshal(w.Body.Bytes(), &info); nil != err {
		t.Fatal(err)
	}
	if 200 != w.Code || 1 != driver.calls || "home" != info.Runtime.Name || 1 != len(info.Containers) || "2.50%" != info.Containers[0].CPU {
		t.Fatalf("info: %d %s", w.Code, w.Body)
	}
	if info.CheckedAt.Before(before) || info.CheckedAt.After(time.Now()) || time.UTC != info.CheckedAt.Location() {
		t.Fatalf("checked at: %v", info.CheckedAt)
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatalf("core bypassed driver: %v", err)
	}
	if w := request(a, http.MethodPost, "/info", "{}"); 404 != w.Code || 1 != driver.calls {
		t.Fatalf("POST info: %d %s", w.Code, w.Body)
	}
}

func TestNodeInfoError(t *testing.T) {
	a, _ := fixture(t)
	failure := &engine.Error{Code: 502, Message: "Could not load node information. Try again."}
	a.engine = &infoEngine{t: t, err: failure}
	w := request(a, http.MethodGet, "/info", "")
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || failure.Code != w.Code || failure.Message != response.Error {
		t.Fatalf("info error: %d %s", w.Code, w.Body)
	}
}

func TestNodeInfoAuth(t *testing.T) {
	a, _ := fixture(t)
	driver := &infoEngine{t: t}
	a.engine = driver
	for _, test := range []struct {
		token, api string
		status     int
	}{
		{"", "", 401},
		{"wrong", "", 401},
		{"test-secret", "999", 426},
	} {
		r := httptest.NewRequest(http.MethodGet, "/info", nil)
		r.RemoteAddr = "127.0.0.1:42"
		r.Header.Set("Authorization", "Bearer "+test.token)
		if "" != test.api {
			r.Header.Set(protocol.Header, test.api)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if test.status != w.Code || 0 != driver.calls {
			t.Fatalf("info auth: %d %s", w.Code, w.Body)
		}
	}
}
