package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arabcoders/dcompose/internal/configutil"
	"github.com/go-jose/go-jose/v4"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
)

type fakeOIDC struct {
	server    *httptest.Server
	nonce     string
	challenge string
	mode      string
	discovery atomic.Int32
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if nil != err {
		t.Fatal(err)
	}
	f := &fakeOIDC{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			f.discovery.Add(1)
			issuer := f.server.URL
			if "discovery" == f.mode {
				issuer += "/wrong"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": f.server.URL + "/authorize", "token_endpoint": f.server.URL + "/token", "jwks_uri": f.server.URL + "/keys", "id_token_signing_alg_values_supported": []string{"ES256"},
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "ES256", Use: "sig"}}})
		case "/token":
			if err := r.ParseForm(); nil != err {
				t.Error(err)
			}
			client, secret, ok := r.BasicAuth()
			if !ok {
				client, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
			}
			if "dcompose" != client || "client-secret" != secret || "authorization_code" != r.Form.Get("grant_type") || "test-code" != r.Form.Get("code") || "http://localhost:8082/api/auth/oidc/callback" != r.Form.Get("redirect_uri") || "" == f.challenge || f.challenge != oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) {
				t.Error("invalid code exchange, client authentication or PKCE verifier")
				http.Error(w, "invalid_request", 400)
				return
			}
			if "exchange" == f.mode {
				http.Error(w, "provider error", 400)
				return
			}
			claims := map[string]any{"iss": f.server.URL, "aud": "dcompose", "sub": "provider-user", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": f.nonce}
			switch f.mode {
			case "issuer":
				claims["iss"] = "https://wrong.example"
			case "audience":
				claims["aud"] = "another-client"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "nonce":
				claims["nonce"] = "wrong"
			case "subject":
				delete(claims, "sub")
			case "issued_at":
				delete(claims, "iat")
			case "expiry":
				delete(claims, "exp")
			case "authorized_party":
				claims["azp"] = "another-client"
			case "multi_audience":
				claims["aud"] = []string{"dcompose", "other"}
			case "valid_multi_audience":
				claims["aud"] = []string{"dcompose", "other"}
				claims["azp"] = "dcompose"
			}
			signingKey := key
			if "signature" == f.mode {
				signingKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if nil != err {
					t.Error(err)
					return
				}
			}
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: signingKey}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
			if nil != err {
				t.Error(err)
				return
			}
			payload, _ := json.Marshal(claims)
			signed, err := signer.Sign(payload)
			if nil != err {
				t.Error(err)
				return
			}
			raw, err := signed.CompactSerialize()
			if nil != err {
				t.Error(err)
				return
			}
			response := map[string]any{"access_token": "unused-access-token", "token_type": "Bearer", "id_token": raw}
			if "missing_token" == f.mode {
				delete(response, "id_token")
			}
			_ = json.NewEncoder(w).Encode(response)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func startOIDC(t *testing.T, s *Server, f *fakeOIDC) (*http.Cookie, string) {
	t.Helper()
	w := perform(t, s, "GET", "/api/auth/oidc/login", "")
	if 302 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if nil != err {
		t.Fatal(err)
	}
	q := u.Query()
	if f.server.URL+"/authorize" != u.Scheme+"://"+u.Host+u.Path || "dcompose" != q.Get("client_id") || "code" != q.Get("response_type") || "openid profile email" != q.Get("scope") || "S256" != q.Get("code_challenge_method") || "" == q.Get("state") || "" == q.Get("nonce") {
		t.Fatal("invalid authorization request", u)
	}
	f.nonce, f.challenge = q.Get("nonce"), q.Get("code_challenge")
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || http.SameSiteLaxMode != cookie.SameSite || 300 != cookie.MaxAge || cookie.Value != q.Get("state") {
		t.Fatal("missing browser state binding")
	}
	return cookie, q.Get("state")
}

func TestOIDC(t *testing.T) {
	for _, mode := range []string{"success", "valid_multi_audience", "public_origin", "issuer", "audience", "expired", "nonce", "subject", "issued_at", "expiry", "authorized_party", "multi_audience", "signature", "missing_token", "exchange", "state", "cookie", "replay", "timeout", "provider_error"} {
		t.Run(mode, func(t *testing.T) {
			f := newFakeOIDC(t)
			f.mode = mode
			s := testServer(t, nil)
			s.config.Auth.OIDC = &OIDCConfig{Issuer: f.server.URL, ClientID: "dcompose", ClientSecret: "client-secret", clientSecret: "client-secret", RedirectURI: "http://localhost:8082/api/auth/oidc/callback"}
			if "public_origin" == mode {
				s.config.Global.Origin = "https://public.example"
				s.config.Global.SecureCookie = true
			}
			cookie, state := startOIDC(t, s, f)
			if "timeout" == mode {
				pending := s.oidcPending[state]
				pending.expires = time.Now().Add(-time.Second)
				s.oidcPending[state] = pending
			}
			if "state" == mode {
				state = "wrong"
			}
			if "cookie" == mode {
				cookie.Value = "wrong"
			}
			if "replay" == mode {
				delete(s.oidcPending, state)
			}
			path := "/api/auth/oidc/callback?state=" + url.QueryEscape(state) + "&code=test-code"
			if "provider_error" == mode {
				path += "&error=access_denied"
			}
			r := httptest.NewRequest("GET", path, nil)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if "success" != mode && "valid_multi_audience" != mode && "public_origin" != mode {
				if 400 != w.Code || 1 != len(w.Result().Cookies()) || "dcompose_oidc" != w.Result().Cookies()[0].Name {
					t.Fatal("invalid OIDC login accepted", w.Code, w.Body.String())
				}
				return
			}
			expected := "http://localhost:8082/"
			if "public_origin" == mode {
				expected = "https://public.example/"
			}
			if 302 != w.Code || expected != w.Header().Get("Location") {
				t.Fatal(w.Code, w.Body.String(), w.Header().Get("Location"))
			}
			var session *http.Cookie
			for _, value := range w.Result().Cookies() {
				if "session" == value.Name {
					session = value
				}
			}
			if nil == session || !session.HttpOnly || http.SameSiteStrictMode != session.SameSite {
				t.Fatal("OIDC did not create existing local session")
			}
			if "public_origin" == mode && (!session.Secure || !cookie.Secure) {
				t.Fatal("public-origin cookies are not secure")
			}
			r = httptest.NewRequest("GET", "/api/auth/user", nil)
			r.AddCookie(session)
			w = httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if 200 != w.Code || !strings.Contains(w.Body.String(), `"username":"admin"`) || strings.Contains(w.Body.String(), "provider-user") {
				t.Fatal("OIDC did not map to local administrator", w.Body.String())
			}
			r = httptest.NewRequest("GET", path, nil)
			r.AddCookie(cookie)
			w = httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if 400 != w.Code {
				t.Fatal("callback replay accepted")
			}
			if 200 != perform(t, s, "POST", "/api/auth/login", `{"username":"admin","password":"test-password"}`).Code {
				t.Fatal("OIDC disabled local login")
			}
		})
	}
}

func TestOIDCDiscovery(t *testing.T) {
	f := newFakeOIDC(t)
	f.mode = "discovery"
	s := testServer(t, nil)
	s.config.Auth.OIDC = &OIDCConfig{Issuer: f.server.URL}
	if 502 != perform(t, s, "GET", "/api/auth/oidc/login", "").Code {
		t.Fatal("mismatched discovery issuer accepted")
	}
}

func TestOIDCCache(t *testing.T) {
	f := newFakeOIDC(t)
	s := testServer(t, nil)
	s.config.Auth.OIDC = &OIDCConfig{Issuer: f.server.URL, ClientID: "dcompose", clientSecret: "client-secret", RedirectURI: "http://localhost:8082/api/auth/oidc/callback"}
	before := time.Now()
	startOIDC(t, s, f)
	cache := s.oidcCache
	if cache.expires.Before(before.Add(time.Minute)) || cache.expires.After(time.Now().Add(time.Minute)) {
		t.Fatal("provider cache does not expire after one minute")
	}
	startOIDC(t, s, f)
	if 1 != f.discovery.Load() || !cache.expires.Equal(s.oidcCache.expires) {
		t.Fatal("provider and verifier were not reused within cache TTL")
	}
	s.oidcCache.expires = time.Now().Add(-time.Second)
	startOIDC(t, s, f)
	if 2 != f.discovery.Load() || !time.Now().Before(s.oidcCache.expires) {
		t.Fatal("expired provider cache was not refreshed")
	}
}

func TestOIDCLoginValues(t *testing.T) {
	f := newFakeOIDC(t)
	s := testServer(t, nil)
	s.config.Auth.OIDC = &OIDCConfig{Issuer: f.server.URL, ClientID: "dcompose", clientSecret: "client-secret", RedirectURI: "http://localhost:8082/api/auth/oidc/callback"}
	_, first := startOIDC(t, s, f)
	firstNonce, firstChallenge := f.nonce, f.challenge
	_, second := startOIDC(t, s, f)
	if first == second || firstNonce == f.nonce || firstChallenge == f.challenge || s.oidcPending[first].verifier == s.oidcPending[second].verifier {
		t.Fatal("cached discovery reused login state, nonce or PKCE verifier")
	}
	for _, state := range []string{first, second} {
		login := s.oidcPending[state]
		if state == login.nonce || state == login.verifier || login.nonce == login.verifier {
			t.Fatal("login state, nonce and PKCE verifier are not independent")
		}
	}
	if 1 != f.discovery.Load() {
		t.Fatal("login values test did not use cached discovery")
	}
}

func TestOIDCCacheContext(t *testing.T) {
	f := newFakeOIDC(t)
	s := testServer(t, nil)
	s.config.Auth.OIDC = &OIDCConfig{Issuer: f.server.URL, ClientID: "dcompose", clientSecret: "client-secret", RedirectURI: "http://localhost:8082/api/auth/oidc/callback"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("GET", "/api/auth/oidc/login", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 302 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	cancel()
	cookie, state := startOIDC(t, s, f)
	r = httptest.NewRequest("GET", "/api/auth/oidc/callback?state="+url.QueryEscape(state)+"&code=test-code", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if 302 != w.Code || 1 != f.discovery.Load() {
		t.Fatal("cached provider could not verify after discovery request cancellation", w.Code, w.Body.String())
	}
}

func TestOIDCConfig(t *testing.T) {
	s := persistedServer(t, nil)
	dir := filepath.Dir(s.config.path)
	if err := configutil.Write(filepath.Join(dir, "oidc.secret"), []byte("client-secret\n")); nil != err {
		t.Fatal(err)
	}
	s.config.Auth.OIDC = &OIDCConfig{Issuer: "https://id.example", ClientID: "dcompose", ClientSecret: "file:oidc.secret", RedirectURI: "https://app.example/api/auth/oidc/callback"}
	if err := saveConfig(s.config); nil != err {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	c, err := LoadConfig(s.config.path)
	if nil != err || "client-secret" != c.Auth.OIDC.clientSecret || "file:oidc.secret" != c.Auth.OIDC.ClientSecret {
		t.Fatal("OIDC secret not resolved relative to config", err)
	}
	s = New(c, nil)
	w := perform(t, s, "GET", "/api/auth/status", "")
	if !strings.Contains(w.Body.String(), `"oidc_available":true`) {
		t.Fatal(w.Body.String())
	}
	w = perform(t, s, "POST", "/api/settings/account", `{"username":"renamed","password":"","current_password":"test-password"}`)
	if 200 != w.Code {
		t.Fatal(w.Code, w.Body.String())
	}
	w = perform(t, s, "GET", "/api/settings", "")
	if 200 != w.Code || strings.Contains(w.Body.String(), "client-secret") || strings.Contains(w.Body.String(), "oidc.secret") {
		t.Fatal("settings leaked OIDC secret", w.Code, w.Body.String())
	}
	c, err = LoadConfig(c.path)
	if nil != err || "file:oidc.secret" != c.Auth.OIDC.ClientSecret {
		t.Fatal("settings overwrote OIDC reference", err)
	}
	data, err := os.ReadFile(c.path)
	if nil != err || strings.Contains(string(data), "client-secret") {
		t.Fatal("resolved OIDC secret persisted", err)
	}
	if err := SetPassword(c.path, "recovered", "recovery-password"); nil != err {
		t.Fatal("OIDC blocked CLI password recovery", err)
	}
	c, err = LoadConfig(c.path)
	if nil != err || "file:oidc.secret" != c.Auth.OIDC.ClientSecret || nil != bcrypt.CompareHashAndPassword([]byte(c.Auth.passwordHash), []byte("recovery-password")) {
		t.Fatal("password recovery lost OIDC configuration", err)
	}
	c.Auth.Username, c.Auth.Password, c.Auth.passwordHash = "", "", ""
	s = New(c, nil)
	w = perform(t, s, "GET", "/api/auth/status", "")
	if !strings.Contains(w.Body.String(), `"oidc_available":false`) || 404 != perform(t, s, "GET", "/api/auth/oidc/login", "").Code {
		t.Fatal("OIDC available without local administrator")
	}
	for name, value := range map[string]string{"issuer": "not-a-url", "client_id": "", "client_secret": "", "redirect_uri": "invalid"} {
		t.Run(name, func(t *testing.T) {
			text := fmt.Sprintf("[auth.oidc]\nissuer = %q\nclient_id = 'app'\nclient_secret = 'secret'\nredirect_uri = 'https://app.example/api/auth/oidc/callback'\n", "https://id.example")
			switch name {
			case "issuer":
				text = strings.Replace(text, `"https://id.example"`, fmt.Sprintf("%q", value), 1)
			case "client_id":
				text = strings.Replace(text, "'app'", "''", 1)
			case "client_secret":
				text = strings.Replace(text, "'secret'", "''", 1)
			case "redirect_uri":
				text = strings.Replace(text, "'https://app.example/api/auth/oidc/callback'", fmt.Sprintf("%q", value), 1)
			}
			path := filepath.Join(t.TempDir(), "dcompose.toml")
			if err := os.WriteFile(path, []byte(text), 0600); nil != err {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); nil == err {
				t.Fatal("invalid OIDC config accepted")
			}
		})
	}
}
