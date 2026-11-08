package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/arabcoders/dcompose/internal/engine/registry"
	"github.com/arabcoders/dcompose/internal/protocol"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/crypto/bcrypt"
)

type Server struct {
	config         Config
	configMu       sync.RWMutex
	version        string
	assets         fs.FS
	nodes          map[string]*http.Client
	loginMu        sync.Mutex
	loginNext      time.Time
	oidcMu         sync.Mutex
	oidcPending    map[string]oidcLogin
	ticketMu       sync.Mutex
	tickets        map[string]websocketTicket
	hubMu          sync.Mutex
	hubContext     context.Context
	hubCancel      context.CancelFunc
	hubClosed      bool
	hubWorkers     sync.WaitGroup
	channels       map[string]*nodeChannel
	runtime        map[string]*nodeRuntime
	watchers       map[chan []byte]struct{}
	nextStream     atomic.Uint64
	registrySync   protocol.RegistrySync // guarded by hubMu
	registryClient *registry.Client
	images         *imageChecks // guarded by hubMu
	logHistory     *logHistory
	logLogger      *slog.Logger
	logPrevious    *slog.Logger

	relays map[relayKey]bool // guarded by hubMu; only node self-update relays
}

func New(config Config, assets fs.FS, versions ...string) *Server {
	s := &Server{config: cloneConfig(config), assets: assets, version: "dev", nodes: make(map[string]*http.Client), oidcPending: make(map[string]oidcLogin), tickets: make(map[string]websocketTicket), channels: make(map[string]*nodeChannel), runtime: make(map[string]*nodeRuntime), watchers: make(map[chan []byte]struct{})}
	s.registrySync = registrySnapshot(config.Registries)
	s.registryClient = registry.NewClient(&http.Client{Transport: registryTransport{server: s}, Timeout: 30 * time.Second})
	s.images = newImageChecks()
	s.relays = make(map[relayKey]bool)
	if 0 < len(versions) {
		s.version = versions[0]
	}
	for id, node := range config.Nodes {
		s.nodes[id] = nodeClient(node)
	}
	return s
}

// Snapshots share maps and slices that are replaced, never mutated, by settings writes.
func (s *Server) snapshot() (Config, map[string]*http.Client) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config, s.nodes
}

func apiError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]interface{}{"error": map[string]interface{}{"code": status, "message": message}})
}

func jsonResponse(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	if "/api" != r.URL.Path && !strings.HasPrefix(r.URL.Path, "/api/") {
		s.static(w, r)
		return
	}
	if "GET" != r.Method && "HEAD" != r.Method && !sameOrigin(r) {
		apiError(w, http.StatusForbidden, "Cross-origin requests are not allowed.")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if "/api/version" == path && "GET" == r.Method {
		jsonResponse(w, 200, map[string]interface{}{"version": s.version, "api_version": protocol.ServerAPI, "agent_api_version": protocol.AgentAPI})
		return
	}
	if "/api/auth/login" == path && "POST" == r.Method {
		s.login(w, r)
		return
	}
	if "/api/auth/status" == path && "GET" == r.Method {
		s.authStatus(w, r)
		return
	}
	if "/api/auth/setup" == path && "POST" == r.Method {
		s.setup(w, r)
		return
	}
	if "/api/auth/oidc/login" == path && "GET" == r.Method {
		s.oidcStart(w, r)
		return
	}
	if "/api/auth/oidc/callback" == path && "GET" == r.Method {
		s.oidcCallback(w, r)
		return
	}
	if "/api/events" == path && "GET" == r.Method {
		s.workspaceEvents(w, r)
		return
	}
	if !s.authenticated(r) {
		if 0 < len(r.Header.Values("Authorization")) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			apiError(w, 401, "Invalid API key.")
		} else {
			w.Header().Set("WWW-Authenticate", "Session")
			apiError(w, 401, "Sign in to continue.")
		}
		return
	}
	switch {
	case "/api/logs" == path && "GET" == r.Method:
		s.logs(w, r)
	case "/api/logs/stream" == path && "GET" == r.Method:
		s.streamLogs(w, r)
	case "/api/auth/tickets" == path && "POST" == r.Method:
		s.createWebsocketTicket(w, r)
	case "/api/auth/user" == path && "GET" == r.Method:
		config, _ := s.snapshot()
		jsonResponse(w, 200, map[string]interface{}{"account": map[string]string{"username": config.Auth.Username}, "authenticated": true})
	case "/api/settings" == path || strings.HasPrefix(path, "/api/settings/"):
		s.settings(w, r, path)
	case "/api/auth/logout" == path && "POST" == r.Method:
		s.logout(w, r)
	case "/api/backup" == path && "POST" == r.Method:
		s.backup(w, r)
	case "/api/nodes" == path && "GET" == r.Method:
		s.listNodes(w)
	case strings.HasPrefix(path, "/api/nodes/"):
		s.proxy(w, r, strings.TrimPrefix(path, "/api/nodes/"))
	default:
		apiError(w, 404, "Not found.")
	}
}

func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); "" != site {
		return strings.EqualFold(site, "none") || strings.EqualFold(site, "same-origin")
	}
	origin := r.Header.Get("Origin")
	if "" == origin {
		return true
	}
	parsed, err := url.Parse(origin)
	if nil != err || ("http" != parsed.Scheme && "https" != parsed.Scheme) || "" == parsed.Host || "" != parsed.Path || "" != parsed.RawQuery || "" != parsed.Fragment || nil != parsed.User {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func (s *Server) authenticated(r *http.Request) bool {
	config, _ := s.snapshot()
	if config.Auth.setupRequired() {
		return false
	}
	if headers := r.Header.Values("Authorization"); 0 < len(headers) {
		parts := strings.Fields(headers[0])
		if 1 != len(headers) || 2 != len(parts) || !strings.EqualFold(parts[0], "Bearer") || "" == config.Auth.apiKey {
			return false
		}
		return 1 == subtle.ConstantTimeCompare([]byte(parts[1]), []byte(config.Auth.apiKey))
	}
	cookie, err := r.Cookie("session")
	if nil != err || 32 != len(config.Auth.sessionKey) {
		return false
	}
	token, err := jwt.ParseSigned(cookie.Value, []jose.SignatureAlgorithm{jose.HS256})
	if nil != err {
		return false
	}
	var claims sessionClaims
	if err := token.Claims([]byte(config.Auth.sessionKey), &claims); nil != err {
		return false
	}
	if nil == claims.Expiry || nil != claims.ValidateWithLeeway(jwt.Expected{Time: time.Now()}, 0) {
		return false
	}
	return hmac.Equal([]byte(claims.CredentialRevision), []byte(credentialRevision(config.Auth)))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &input, 16<<10) {
		return
	}
	// Pace bcrypt checks globally across login attempts.
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	if time.Now().Before(s.loginNext) {
		w.Header().Set("Retry-After", "1")
		apiError(w, 429, "Wait one second before trying again.")
		return
	}
	s.loginNext = time.Now().Add(time.Second)
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	validPassword := nil == bcrypt.CompareHashAndPassword([]byte(s.config.Auth.passwordHash), []byte(input.Password))
	validUsername := 1 == subtle.ConstantTimeCompare([]byte(input.Username), []byte(s.config.Auth.Username))
	if !validPassword || !validUsername {
		apiError(w, 401, "Invalid username or password.")
		return
	}
	if !s.createSession(w, r, s.config, s.config.Global.SecureCookie) {
		return
	}
	jsonResponse(w, 200, map[string]bool{"authenticated": true})
}

type sessionClaims struct {
	jwt.Claims
	CredentialRevision string `json:"credential_revision"`
}

func credentialRevision(auth AuthConfig) string {
	revision := hmac.New(sha256.New, []byte(auth.sessionKey))
	_, _ = revision.Write([]byte("credentials\x00" + auth.Username + "\x00" + auth.passwordHash))
	return base64.RawURLEncoding.EncodeToString(revision.Sum(nil))
}

func sessionToken(auth AuthConfig, expires time.Time) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte(auth.sessionKey)}, (&jose.SignerOptions{}).WithType("JWT"))
	if nil != err {
		return "", err
	}
	return jwt.Signed(signer).Claims(sessionClaims{
		Claims:             jwt.Claims{Expiry: jwt.NewNumericDate(expires)},
		CredentialRevision: credentialRevision(auth),
	}).Serialize()
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request, config Config, secure bool) bool {
	if 32 != len(config.Auth.sessionKey) {
		apiError(w, 500, "Could not sign you in. Try again.")
		return false
	}
	expires := time.Now().Add(24 * time.Hour)
	token, err := sessionToken(config.Auth, expires)
	if nil != err {
		apiError(w, 500, "Could not sign you in. Try again.")
		return false
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: token, Path: "/", HttpOnly: true, Secure: secure || nil != r.TLS, SameSite: http.SameSiteStrictMode, MaxAge: 86400, Expires: expires})
	return true
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	config, _ := s.snapshot()
	http.SetCookie(w, &http.Cookie{Name: "session", Path: "/", HttpOnly: true, Secure: config.Global.SecureCookie || nil != r.TLS, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	jsonResponse(w, 200, map[string]bool{"authenticated": false})
}

func decodeBody(w http.ResponseWriter, r *http.Request, target interface{}, limit int64) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); nil != err {
		apiError(w, 400, "Invalid JSON request.")
		return false
	}
	if err := decoder.Decode(new(interface{})); io.EOF != err {
		apiError(w, 400, "Invalid JSON request.")
		return false
	}
	return true
}

func (s *Server) listNodes(w http.ResponseWriter) {
	config, _ := s.snapshot()
	ids := orderedNodeIDs(config)
	type publicNodeInfo struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		PublicHost string `json:"public_host"`
		Paused     bool   `json:"paused,omitempty"`
	}
	nodes := make([]publicNodeInfo, 0, len(ids))
	for _, id := range ids {
		node := config.Nodes[id]
		nodes = append(nodes, publicNodeInfo{ID: id, Name: node.Name, PublicHost: strings.Trim(node.PublicHost, "[]"), Paused: node.Paused})
	}
	jsonResponse(w, 200, map[string]interface{}{"nodes": nodes})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if "GET" != r.Method && "HEAD" != r.Method {
		http.NotFound(w, r)
		return
	}
	if nil == s.assets {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if "" == name {
		name = "index.html"
	}
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	if stat, err := fs.Stat(s.assets, name); nil != err || stat.IsDir() {
		if strings.Contains(name[strings.LastIndex(name, "/")+1:], ".") {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(s.assets, "index.html")
		if nil != err {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		if "HEAD" != r.Method {
			_, _ = w.Write(data)
		}
		return
	}
	http.FileServer(http.FS(s.assets)).ServeHTTP(w, r)
}
