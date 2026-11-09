package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func TestPairNode(t *testing.T) {
	var up *httptest.Server
	up = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).
			Decode(&input); nil != err || "temporary-token" != input.Token || "/pair" != r.URL.Path || http.MethodPost != r.Method ||
			strconv.Itoa(protocol.AgentAPI) != r.Header.Get(protocol.Header) {
			t.Error("incorrect pairing request", err)
		}
		jsonResponse(
			w,
			200,
			protocol.PairingResponse{ID: "node-id", Name: "node-hostname", Token: "permanent-key", Fingerprint: testFingerprint(up)},
		)
	}))
	defer up.Close()
	s := persistedServer(t, nil)
	frames, _ := s.subscribeWorkspace()
	w := perform(
		t,
		s,
		http.MethodPost,
		"/api/settings/nodes/pair",
		fmt.Sprintf(`{"name":"","url":%q,"pairing_token":"temporary-token"}`, strings.TrimPrefix(up.URL, "https://")),
	)
	var paired safeNode
	if 200 != w.Code || nil != json.Unmarshal(w.Body.Bytes(), &paired) || "node-hostname" != paired.Name ||
		"permanent-key" != paired.Token ||
		strings.Contains(w.Body.String(), "temporary-token") {
		t.Fatal(w.Code, w.Body.String())
	}
	c, err := LoadConfig(s.config.path)
	if nil != err {
		t.Fatal(err)
	}
	node := c.Nodes["node-id"]
	if up.URL != node.URL || "node-hostname" != node.Name || "permanent-key" != node.token || testFingerprint(up) != node.Fingerprint ||
		!strings.HasPrefix(node.Token, "file:secrets/") {
		t.Fatal("paired connection was not saved")
	}
	select {
	case frame := <-frames:
		if !strings.Contains(string(frame), "nodes.changed") {
			t.Fatal("missing node change event")
		}
	default:
		t.Fatal("pairing did not publish a node change")
	}
}

func TestPairNodePin(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); nil != err || "temporary-token" != input.Token {
			t.Error("incorrect pairing token", err)
		}
		jsonResponse(
			w,
			http.StatusOK,
			protocol.PairingResponse{ID: "node-id", Name: "Home", Token: "permanent-key", Fingerprint: "response-pin"},
		)
	}))
	defer up.Close()
	for _, test := range []struct {
		name, fingerprint string
		status            int
		calls             int32
	}{
		{"match", " " + testFingerprint(up) + " ", http.StatusOK, 1},
		{"mismatch", base64.StdEncoding.EncodeToString(make([]byte, 32)), http.StatusBadGateway, 0},
		{"invalid", "not-base64", http.StatusBadRequest, 0},
		{"short", base64.StdEncoding.EncodeToString([]byte("short")), http.StatusBadRequest, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls.Store(0)
			s := persistedServer(t, nil)
			body := fmt.Sprintf(`{"url":%q,"pairing_token":"temporary-token","fingerprint":%q}`, up.URL, test.fingerprint)
			w := perform(t, s, http.MethodPost, "/api/settings/nodes/pair", body)
			if test.status != w.Code || test.calls != calls.Load() || strings.Contains(w.Body.String(), "temporary-token") {
				t.Fatal("incorrect pairing result or token reached an unverified peer", w.Code, w.Body.String(), calls.Load())
			}
			if http.StatusOK != w.Code {
				if 0 != len(s.config.Nodes) {
					t.Fatal("rejected pairing saved a node")
				}
				return
			}
			config, err := LoadConfig(s.config.path)
			if nil != err || testFingerprint(up) != config.Nodes["node-id"].Fingerprint {
				t.Fatal("verified fingerprint was not retained", err)
			}
		})
	}
}

func TestPairNodeFailure(t *testing.T) {
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(401)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jsonResponse(w, int(status.Load()), map[string]string{"error": "private sentinel: /srv/agent.toml token=secret"})
	}))
	defer up.Close()
	s := persistedServer(t, nil)
	body := fmt.Sprintf(`{"name":"Home","url":%q,"pairing_token":"wrong"}`, up.URL)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/settings/nodes/pair", strings.NewReader(body)))
	if 401 != w.Code || 0 != calls.Load() {
		t.Fatal("unauthenticated pairing reached the node")
	}
	for _, upstreamStatus := range []int{401, 403, 409, 426, 500} {
		status.Store(int32(upstreamStatus))
		output.Reset()
		w = perform(t, s, http.MethodPost, "/api/settings/nodes/pair", body)
		var response struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(
			w.Body.Bytes(),
			&response,
		); nil != err || 502 != w.Code || 502 != response.Error.Code || "" == response.Error.Message || strings.Contains(w.Body.String(), up.URL) || strings.Contains(w.Body.String(), "private sentinel") ||
			0 != len(s.config.Nodes) {
			t.Fatal(w.Code, w.Body.String())
		}
		var record struct {
			Message   string `json:"msg"`
			Level     string `json:"level"`
			Node      string `json:"node"`
			Operation string `json:"operation"`
			Status    int    `json:"status"`
			Error     string `json:"error"`
		}
		if err := json.Unmarshal(output.Bytes(), &record); nil != err {
			t.Fatal(err, output.String())
		}
		if !strings.Contains(record.Node, "Home") || !strings.Contains(record.Node, up.URL) || "" == record.Operation ||
			upstreamStatus != record.Status ||
			!strings.Contains(record.Error, "private sentinel: /srv/agent.toml token=secret") {
			t.Fatal("missing private pairing failure details", output.String())
		}
		if "ERROR" != record.Level || !strings.Contains(record.Message, record.Node) || !strings.Contains(record.Message, record.Error) {
			t.Fatal("pairing message hides failure context", record.Message)
		}
	}
	for _, address := range []string{"http://node:9077", "https://secret@node:9077", "https://node:9077?secret=value"} {
		w = perform(t, s, http.MethodPost, "/api/settings/nodes/pair", fmt.Sprintf(`{"url":%q,"pairing_token":"token"}`, address))
		if 400 != w.Code {
			t.Fatal("invalid pairing address accepted", w.Code, w.Body.String())
		}
	}
	if 5 != calls.Load() {
		t.Fatal("invalid address performed a pairing request")
	}
	up.Close()
	output.Reset()
	w = perform(t, s, http.MethodPost, "/api/settings/nodes/pair", body)
	if 502 != w.Code || strings.Contains(w.Body.String(), up.URL) || 0 != len(s.config.Nodes) {
		t.Fatal(w.Code, w.Body.String())
	}
	var record struct {
		Message   string `json:"msg"`
		Operation string `json:"operation"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(
		output.Bytes(),
		&record,
	); nil != err || "" == record.Operation || "" == record.Error || !strings.Contains(record.Message, "Home") || !strings.Contains(record.Message, up.URL) || !strings.Contains(record.Message, record.Error) || strings.Contains(w.Body.String(), record.Error) ||
		strings.Contains(output.String(), "wrong") {
		t.Fatal("pairing transport message hides context or leaks token", output.String(), err)
	}
}

func TestPairResponseLog(t *testing.T) {
	output := captureNodeLogs(t, slog.LevelError)
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("!"))
	}))
	defer up.Close()
	s := persistedServer(t, nil)
	w := perform(
		t,
		s,
		http.MethodPost,
		"/api/settings/nodes/pair",
		fmt.Sprintf(`{"name":"Home","url":%q,"pairing_token":"temporary-token"}`, up.URL),
	)
	if http.StatusBadGateway != w.Code {
		t.Fatal("invalid pairing response changed public error", w.Code, w.Body.String())
	}
	select {
	case line := <-output.lines:
		var record struct {
			Message string `json:"msg"`
			Level   string `json:"level"`
			Error   string `json:"error"`
			Status  int    `json:"status"`
		}
		if err := json.Unmarshal(
			line,
			&record,
		); nil != err || "ERROR" != record.Level || http.StatusOK != record.Status ||
			"" == record.Error {
			t.Fatal("missing invalid pairing response diagnostic", string(line), err)
		}
		for _, detail := range []string{"Home", up.URL, record.Error, strconv.Itoa(http.StatusOK)} {
			if !strings.Contains(record.Message, detail) {
				t.Fatal("pairing response message hides context", record.Message)
			}
		}
		if strings.Contains(string(line), "temporary-token") || strings.Contains(w.Body.String(), record.Error) {
			t.Fatal("pairing token or private diagnostics leaked", string(line), w.Body.String())
		}
	default:
		t.Fatal("missing pairing response log")
	}
}

func TestPairNodeRejected(t *testing.T) {
	for _, test := range []struct {
		name, id, token string
		status          int
	}{
		{"empty", "new-node", "", http.StatusBadGateway},
		{"empty_existing", "home", "", http.StatusBadGateway},
		{"collision", "home", "new-node-token", http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			victim := httptest.NewTLSServer(nil)
			defer victim.Close()
			var up *httptest.Server
			up = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if "/pair" != r.URL.Path || "" != r.Header.Get("Authorization") {
					t.Error("pairing exposed existing node credentials")
				}
				jsonResponse(
					w,
					http.StatusOK,
					protocol.PairingResponse{ID: test.id, Name: "new-node", Token: test.token, Fingerprint: testFingerprint(up)},
				)
			}))
			defer up.Close()
			s := persistedServer(t, victim)
			existing := s.config.Nodes["home"]
			before, err := os.ReadFile(s.config.path)
			if nil != err {
				t.Fatal(err)
			}
			w := perform(
				t,
				s,
				http.MethodPost,
				"/api/settings/nodes/pair",
				fmt.Sprintf(`{"url":%q,"pairing_token":"temporary-token"}`, up.URL),
			)
			if test.status != w.Code || strings.Contains(w.Body.String(), existing.token) ||
				strings.Contains(w.Body.String(), "new-node-token") {
				t.Fatalf("pairing response: %d %s", w.Code, w.Body.String())
			}
			if 1 != len(s.config.Nodes) || existing != s.config.Nodes["home"] {
				t.Fatal("rejected pairing changed existing node")
			}
			after, err := os.ReadFile(s.config.path)
			if nil != err || !bytes.Equal(before, after) {
				t.Fatal("rejected pairing changed saved configuration", err)
			}
		})
	}
}
