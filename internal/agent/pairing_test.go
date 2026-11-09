package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/arabcoders/dcompose/internal/protocol"
)

func pairRequest(a *agent, token string) *httptest.ResponseRecorder {
	data, _ := json.Marshal(map[string]string{"token": token})
	r := httptest.NewRequest(http.MethodPost, "/pair", strings.NewReader(string(data)))
	r.RemoteAddr = "172.22.0.1:1234"
	r.Header.Set(protocol.Header, strconv.Itoa(protocol.AgentAPI))
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestPairing(t *testing.T) {
	path := configFixture(t, "custom_option = 'keep'", "allowed_ips = ['127.0.0.1']")
	c, err := loadConfig(path)
	if nil != err {
		t.Fatal(err)
	}
	a := newAgent(context.Background(), c)
	token := a.pairingToken
	if "" == token || "test-secret" == token {
		t.Fatal("missing temporary pairing token")
	}
	if w := pairRequest(
		a,
		"wrong",
	); 401 != w.Code || token != a.pairingToken || a.config.Global.Paired ||
		strings.Contains(w.Body.String(), token) {
		t.Fatal(w.Code, w.Body.String())
	}
	w := pairRequest(a, token)
	var paired protocol.PairingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &paired); nil != err || 200 != w.Code {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	_, fingerprint, err := connectionInfo(c, path)
	if nil != err || c.Global.NodeID != paired.ID || "" == paired.Name || "test-secret" != paired.Token ||
		fingerprint != paired.Fingerprint ||
		"no-store" != w.Header().Get("Cache-Control") {
		t.Fatal("incorrect exchanged credentials", err)
	}
	if w := pairRequest(a, token); 409 != w.Code {
		t.Fatal("pairing token was reused", w.Code)
	}
	reloaded, err := loadConfig(path)
	if nil != err || !reloaded.Global.Paired || "" != newAgent(context.Background(), reloaded).pairingToken {
		t.Fatal("paired state was not retained across restart", err)
	}
}

func TestPairingSaveFailure(t *testing.T) {
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	c, err := loadConfig(configFixture(t, "", ""))
	if nil != err {
		t.Fatal(err)
	}
	c.path = filepath.Join(filepath.Dir(c.path), "missing.toml")
	a := newAgent(context.Background(), c)
	token := a.pairingToken
	w := pairRequest(a, token)
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(
		w.Body.Bytes(),
		&response,
	); nil != err || 500 != w.Code || "" == response.Error || strings.Contains(w.Body.String(), c.path) || strings.Contains(w.Body.String(), token) || token != a.pairingToken ||
		a.config.Global.Paired {
		t.Fatal("failed save consumed pairing token", w.Code, w.Body.String())
	}
	if !strings.Contains(logs.String(), c.path) {
		t.Fatal("pairing failure diagnostics were lost", logs.String())
	}
}

func TestPairingJSON(t *testing.T) {
	a, _ := fixture(t)
	for _, body := range []string{`{"token":123}`, `{"internal_field":"secret"}`} {
		r := httptest.NewRequest(http.MethodPost, "/pair", strings.NewReader(body))
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		var response struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(
			w.Body.Bytes(),
			&response,
		); nil != err || 400 != w.Code || "" == response.Error || strings.Contains(w.Body.String(), "internal_field") ||
			strings.Contains(w.Body.String(), "secret") {
			t.Fatal("decoder details exposed", w.Code, w.Body.String())
		}
	}
}

func TestPairingRestart(t *testing.T) {
	c, err := loadConfig(configFixture(t, "", ""))
	if nil != err {
		t.Fatal(err)
	}
	first := newAgent(context.Background(), c)
	second := newAgent(context.Background(), c)
	if first.pairingToken == second.pairingToken {
		t.Fatal("unpaired restart reused temporary token")
	}
	if w := pairRequest(second, first.pairingToken); 401 != w.Code {
		t.Fatal("old temporary token accepted after restart")
	}
}

func TestUnpair(t *testing.T) {
	a, fake := fixture(t)
	previous := a.pairingToken
	if w := pairRequest(a, previous); http.StatusOK != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	output, err := os.CreateTemp(t.TempDir(), "pairing-*")
	if nil != err {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = output
	t.Cleanup(func() { os.Stderr = original; _ = output.Close() })
	logs := captureAutomaticLogs(t)
	w := request(a, http.MethodPost, "/unpair", "")
	token := a.pairingToken
	if http.StatusOK != w.Code || a.config.Global.Paired || "" == token || previous == token ||
		"no-store" != w.Header().Get("Cache-Control") {
		t.Fatal("node did not return to pairing mode", w.Code, w.Body.String())
	}
	local, err := os.ReadFile(output.Name())
	if nil != err || !strings.Contains(string(local), token) || strings.Contains(logs.text(), token) ||
		strings.Contains(w.Body.String(), token) ||
		strings.Contains(w.Body.String(), "test-secret") {
		t.Fatal("pairing token was not restricted to local output", err)
	}
	_, fingerprint, err := connectionInfo(a.config, a.config.path)
	if nil != err || !strings.Contains(string(local), fingerprint) {
		t.Fatal("pairing output did not include the node fingerprint", err)
	}
	reloaded, err := loadConfig(a.config.path)
	if nil != err || reloaded.Global.Paired || reloaded.Global.NodeID != a.config.Global.NodeID ||
		reloaded.Auth.Token != a.config.Auth.Token ||
		"" == newAgent(context.Background(), reloaded).pairingToken {
		t.Fatal("pairing reset was not persisted", err)
	}
	if _, err := os.Stat(filepath.Join(fake, "argv")); !os.IsNotExist(err) {
		t.Fatal("unpair invoked the container engine", err)
	}
	if w := request(a, http.MethodPost, "/unpair", ""); http.StatusOK != w.Code || token != a.pairingToken {
		t.Fatal("repeated reset changed the current pairing token")
	}
	if w := pairRequest(a, previous); http.StatusUnauthorized != w.Code {
		t.Fatal("old pairing token was accepted", w.Code)
	}
	if w := pairRequest(a, token); http.StatusOK != w.Code || !a.config.Global.Paired {
		t.Fatal("node could not pair again", w.Code, w.Body.String())
	}
}

func TestUnpairAuth(t *testing.T) {
	a, _ := fixture(t)
	if w := pairRequest(a, a.pairingToken); http.StatusOK != w.Code {
		t.Fatal(w.Code)
	}
	for _, test := range []struct {
		token, peer, version string
		status               int
	}{
		{"", "127.0.0.1:42", "", http.StatusUnauthorized},
		{"wrong", "127.0.0.1:42", "", http.StatusUnauthorized},
		{"test-secret", "192.0.2.1:42", "", http.StatusForbidden},
		{"test-secret", "127.0.0.1:42", "999", http.StatusUpgradeRequired},
	} {
		r := httptest.NewRequest(http.MethodPost, "/unpair", nil)
		r.RemoteAddr = test.peer
		r.Header.Set("Authorization", "Bearer "+test.token)
		r.Header.Set(protocol.Header, test.version)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if test.status != w.Code || !a.config.Global.Paired || "" != a.pairingToken {
			t.Fatal("rejected reset changed pairing", w.Code, w.Body.String())
		}
	}
}

func TestUnpairSaveFailure(t *testing.T) {
	a, _ := fixture(t)
	if w := pairRequest(a, a.pairingToken); http.StatusOK != w.Code {
		t.Fatal(w.Code)
	}
	path := a.config.path
	a.config.path = filepath.Join(filepath.Dir(path), "missing.toml")
	w := request(a, http.MethodPost, "/unpair", "")
	if http.StatusInternalServerError != w.Code || !a.config.Global.Paired || "" != a.pairingToken ||
		strings.Contains(w.Body.String(), a.config.path) {
		t.Fatal("failed save changed pairing", w.Code, w.Body.String())
	}
	reloaded, err := loadConfig(path)
	if nil != err || !reloaded.Global.Paired {
		t.Fatal("failed save changed persisted pairing", err)
	}
}
