package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/arabcoders/dcompose/internal/engine"
	"github.com/arabcoders/dcompose/internal/protocol"
)

type infoEngine struct {
	engine.Driver
	t        *testing.T
	err      error
	calls    int
	deadline time.Time
}

func (e *infoEngine) NodeInfo(ctx context.Context) (engine.NodeInfo, error) {
	e.calls++
	deadline, ok := ctx.Deadline()
	if !ok {
		e.t.Fatalf("node info deadline: %v, set: %t", deadline, ok)
	}
	e.deadline = deadline
	return engine.NodeInfo{
		Runtime:    engine.RuntimeInfo{Name: "home", Version: "27.5.1", CPUs: 8, MemoryBytes: 16777216000},
		Containers: []engine.ContainerStats{{ID: "container", Name: "web", CPU: "2.50%", Memory: "12MiB / 1GiB"}},
	}, e.err
}

// sharedInfoEngine blocks every sample until released, so overlapping requests
// join one in-flight flight.
type sharedInfoEngine struct {
	engine.Driver
	entered chan struct{}
	release chan struct{}
	calls   *atomic.Int32
}

func (e *sharedInfoEngine) NodeInfo(ctx context.Context) (engine.NodeInfo, error) {
	e.calls.Add(1)
	e.entered <- struct{}{}
	select {
	case <-e.release:
		return engine.NodeInfo{Runtime: engine.RuntimeInfo{Name: "home", Version: "27.5.1"}}, nil
	case <-ctx.Done():
		return engine.NodeInfo{}, ctx.Err()
	}
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
	if driver.deadline.Before(before.Add(nodeInfoTimeout)) || driver.deadline.After(time.Now().Add(nodeInfoTimeout)) {
		t.Fatalf("node info deadline: %v", driver.deadline)
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

func TestNodeInfoShared(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		driver := &sharedInfoEngine{entered: make(chan struct{}, 4), release: make(chan struct{}), calls: &calls}
		a := &agent{ctx: context.Background(), engine: driver}
		responses := make(chan engine.NodeInfo, 3)
		request := func() {
			w := httptest.NewRecorder()
			if err := a.nodeInfo(context.Background(), w); nil != err {
				t.Errorf("shared info: %v", err)
			}
			var info engine.NodeInfo
			if nil != json.Unmarshal(w.Body.Bytes(), &info) {
				t.Errorf("shared info body: %s", w.Body)
			}
			responses <- info
		}
		go request()
		go request()
		<-driver.entered
		synctest.Wait()
		close(driver.release)
		first := <-responses
		second := <-responses
		if 1 != calls.Load() {
			t.Fatalf("node info samples: %d", calls.Load())
		}
		if first.CheckedAt.IsZero() || !first.CheckedAt.Equal(second.CheckedAt) {
			t.Fatalf("shared sample timestamps: %v %v", first.CheckedAt, second.CheckedAt)
		}
		// Sequential requests sample again; nothing is cached.
		go request()
		<-driver.entered
		<-responses
		if 2 != calls.Load() {
			t.Fatalf("cached node info samples: %d", calls.Load())
		}
	})
}

func TestNodeInfoLeaderCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		driver := &sharedInfoEngine{entered: make(chan struct{}, 4), release: make(chan struct{}), calls: &calls}
		a := &agent{ctx: context.Background(), engine: driver}
		leader := make(chan struct{})
		responses := make(chan engine.NodeInfo, 1)
		leaderCtx, cancelLeader := context.WithCancel(context.Background())
		go func() {
			defer close(leader)
			_ = a.nodeInfo(leaderCtx, httptest.NewRecorder())
		}()
		<-driver.entered
		cancelLeader()
		// The overlapping waiter still receives the original sample.
		go func() {
			w := httptest.NewRecorder()
			if err := a.nodeInfo(context.Background(), w); nil != err {
				t.Errorf("waiter info: %v", err)
			}
			var info engine.NodeInfo
			if nil != json.Unmarshal(w.Body.Bytes(), &info) {
				t.Errorf("waiter info body: %s", w.Body)
			}
			responses <- info
		}()
		synctest.Wait()
		close(driver.release)
		<-leader
		waiter := <-responses
		if 1 != calls.Load() || "home" != waiter.Runtime.Name || waiter.CheckedAt.IsZero() {
			t.Fatalf("waiter info after leader cancel: %+v, %d calls", waiter, calls.Load())
		}
	})
}
