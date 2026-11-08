package server

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type oidcLogin struct {
	nonce    string
	verifier string
	expires  time.Time
	oauth    oauth2.Config
	idToken  *oidc.IDTokenVerifier
}

func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	c, _ := s.snapshot()
	if nil == c.Auth.OIDC || c.Auth.setupRequired() {
		apiError(w, 404, "External sign-in is not available. Sign in with your username and password.")
		return
	}
	ctx := oidc.ClientContext(r.Context(), &http.Client{Timeout: 15 * time.Second})
	provider, err := oidc.NewProvider(ctx, c.Auth.OIDC.Issuer)
	if nil != err {
		apiError(w, 502, "Could not reach your sign-in provider. Try again.")
		return
	}
	pending := oidcLogin{
		nonce:    oauth2.GenerateVerifier(),
		verifier: oauth2.GenerateVerifier(),
		expires:  time.Now().Add(5 * time.Minute),
		oauth: oauth2.Config{
			ClientID: c.Auth.OIDC.ClientID, ClientSecret: c.Auth.OIDC.clientSecret,
			RedirectURL: c.Auth.OIDC.RedirectURI, Endpoint: provider.Endpoint(),
			Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
		},
		idToken: provider.Verifier(&oidc.Config{ClientID: c.Auth.OIDC.ClientID}),
	}
	state := oauth2.GenerateVerifier()
	s.oidcMu.Lock()
	for key, login := range s.oidcPending {
		if !time.Now().Before(login.expires) {
			delete(s.oidcPending, key)
		}
	}
	s.oidcPending[state] = pending
	s.oidcMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "dcompose_oidc", Value: state, Path: "/api/auth/oidc", HttpOnly: true, Secure: c.Global.SecureCookie || strings.HasPrefix(c.Auth.OIDC.RedirectURI, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: 300})
	http.Redirect(w, r, pending.oauth.AuthCodeURL(state, oidc.Nonce(pending.nonce), oauth2.S256ChallengeOption(pending.verifier)), http.StatusFound)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	c, _ := s.snapshot()
	secure := c.Global.SecureCookie || nil != r.TLS
	if nil != c.Auth.OIDC {
		secure = secure || strings.HasPrefix(c.Auth.OIDC.RedirectURI, "https://")
	}
	http.SetCookie(w, &http.Cookie{Name: "dcompose_oidc", Path: "/api/auth/oidc", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	state := r.URL.Query().Get("state")
	binding, err := r.Cookie("dcompose_oidc")
	if nil == c.Auth.OIDC || c.Auth.setupRequired() || "" == state || nil != err || 1 != subtle.ConstantTimeCompare([]byte(state), []byte(binding.Value)) {
		apiError(w, 400, "External sign-in failed. Try signing in again.")
		return
	}
	s.oidcMu.Lock()
	pending, ok := s.oidcPending[state]
	delete(s.oidcPending, state)
	s.oidcMu.Unlock()
	if !ok || !time.Now().Before(pending.expires) || "" != r.URL.Query().Get("error") || "" == r.URL.Query().Get("code") {
		apiError(w, 400, "External sign-in failed. Try signing in again.")
		return
	}
	ctx := oidc.ClientContext(r.Context(), &http.Client{Timeout: 15 * time.Second})
	token, err := pending.oauth.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(pending.verifier))
	if nil != err {
		apiError(w, 400, "External sign-in failed. Try signing in again.")
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		apiError(w, 400, "External sign-in failed. Try signing in again.")
		return
	}
	identity, err := pending.idToken.Verify(ctx, raw)
	if nil != err {
		apiError(w, 400, "External sign-in failed. Try signing in again.")
		return
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
	}
	if err := identity.Claims(&claims); nil != err {
		apiError(w, 400, "External sign-in failed. Try signing in again.")
		return
	}
	if "" == strings.TrimSpace(identity.Subject) || identity.IssuedAt.IsZero() || 1 != subtle.ConstantTimeCompare([]byte(identity.Nonce), []byte(pending.nonce)) {
		apiError(w, 400, "External sign-in failed. Try signing in again.")
		return
	}
	if ("" != claims.AuthorizedParty && pending.oauth.ClientID != claims.AuthorizedParty) || (1 < len(identity.Audience) && pending.oauth.ClientID != claims.AuthorizedParty) {
		apiError(w, 400, "External sign-in failed. Try signing in again.")
		return
	}
	// Provider-authorized identities all sign in as the single local administrator.
	if !s.createSession(w, r, c, secure) {
		return
	}
	origin := c.Global.Origin
	if "" == origin {
		callback, _ := url.Parse(c.Auth.OIDC.RedirectURI)
		origin = callback.Scheme + "://" + callback.Host
	}
	http.Redirect(w, r, origin+"/", http.StatusFound)
}
