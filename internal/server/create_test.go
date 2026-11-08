package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func TestCreateForward(t *testing.T) {
	for _, overwrite := range []string{"", `,"overwrite":false`, `,"overwrite":true`} {
		t.Run(overwrite, func(t *testing.T) {
			var requests atomic.Int32
			up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if http.MethodPost != r.Method || "/stacks/copy" != r.URL.Path || "" != r.URL.RawQuery || "Bearer agent-test-token" != r.Header.Get("Authorization") || strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) || "" != r.Header.Get("Cookie") {
					t.Error("incorrect create forwarding", r.Method, r.URL)
				}
				var input struct {
					Compose   string `json:"compose"`
					Env       string `json:"env"`
					Overwrite *bool  `json:"overwrite"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); nil != err || "# raw\nservices: {}\n" != input.Compose || "KEY='raw'\n" != input.Env || nil == input.Overwrite || (`,"overwrite":true` == overwrite) != *input.Overwrite {
					t.Errorf("forwarded payload: %+v %v", input, err)
				}
				jsonResponse(w, http.StatusOK, map[string]string{"name": "copy"})
			}))
			t.Cleanup(up.Close)
			s := testServer(t, up)
			body := `{"compose":"# raw\nservices: {}\n","env":"KEY='raw'\n"` + overwrite + `}`
			w := perform(t, s, http.MethodPost, "/api/nodes/home/stacks/copy?token=browser-secret", body)
			var result map[string]string
			if 200 != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &result) || 1 != len(result) || "copy" != result["name"] || 1 != requests.Load() {
				t.Fatalf("create: %d %s", w.Code, w.Body)
			}
			w = httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/nodes/home/stacks/copy", strings.NewReader(body)))
			if 401 != w.Code || 1 != requests.Load() {
				t.Fatal("unauthenticated create reached agent", w.Code, requests.Load())
			}
		})
	}
}

func TestCreateError(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				jsonResponse(w, status, map[string]string{"error": "private /srv/stacks/.env SECRET=value"})
			}))
			t.Cleanup(up.Close)
			s := testServer(t, up)
			w := perform(t, s, http.MethodPost, "/api/nodes/home/stacks/copy", `{"compose":"raw","env":"","overwrite":false}`)
			var result struct {
				Error struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if status != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &result) || status != result.Error.Code || "" == result.Error.Message || strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "/srv/") {
				t.Fatalf("unsafe create error: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestCreateBody(t *testing.T) {
	var requests atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		jsonResponse(w, http.StatusOK, map[string]string{"name": "copy"})
	}))
	t.Cleanup(up.Close)
	s := testServer(t, up)
	for _, body := range []string{`{"compose":"raw","env":"","overwrite":"true"}`, `{"compose":"raw","env":"","extra":true}`, `{"compose":"","env":""}`, `{"compose":"raw","env":""}{}`} {
		if w := perform(t, s, http.MethodPost, "/api/nodes/home/stacks/copy", body); 400 != w.Code {
			t.Fatalf("accepted create body %s: %d", body, w.Code)
		}
	}
	if w := perform(t, s, http.MethodPut, "/api/nodes/home/stacks/copy", `{"compose":"raw","env":"","overwrite":true}`); 400 != w.Code {
		t.Fatalf("PUT accepted create-only overwrite: %d", w.Code)
	}
	if 0 != requests.Load() {
		t.Fatal("invalid create reached agent", requests.Load())
	}
}
