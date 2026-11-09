package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	c, _ := s.snapshot()
	jsonResponse(w, 200, map[string]bool{
		"setup_required": c.Auth.setupRequired(),
		"authenticated":  s.authenticated(r),
		"oidc_available": nil != c.Auth.OIDC && !c.Auth.setupRequired(),
	})
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &input, 16<<10) {
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if !s.config.Auth.setupRequired() {
		apiError(w, 409, "An account already exists. Sign in to continue.")
		return
	}
	if "" == strings.TrimSpace(input.Username) || "" == input.Password || 72 < len(input.Password) {
		apiError(w, 400, "Provide a username and a password of 1-72 bytes.")
		return
	}
	c, err := changeCredentials(cloneConfig(s.config), input.Username, input.Password)
	if nil != err {
		slog.Error(
			fmt.Sprintf(
				"Could not save the first account in configuration %q: %v. Check configuration directory permissions.",
				s.config.path,
				err,
			),
			"error",
			err,
		)
		apiError(w, 500, "Could not save the account. Check the configuration directory's permissions.")
		return
	}
	s.applyConfig(c)
	if s.createSession(w, r, c, c.Global.SecureCookie) {
		jsonResponse(w, 201, map[string]bool{"authenticated": true})
	}
}
