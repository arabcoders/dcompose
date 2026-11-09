package server

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type websocketTicket struct {
	expires time.Time
	auth    http.Header
}

func (s *Server) createWebsocketTicket(w http.ResponseWriter, r *http.Request) {
	var input *struct{}
	if !decodeBody(w, r, &input, 16<<10) {
		return
	}
	if nil == input {
		apiError(w, http.StatusBadRequest, "Invalid JSON request.")
		return
	}

	var token [32]byte
	if _, err := rand.Read(token[:]); nil != err {
		apiError(w, http.StatusInternalServerError, "Could not create a WebSocket ticket.")
		return
	}
	value := base64.RawURLEncoding.EncodeToString(token[:])
	auth := make(http.Header)
	if authorization := r.Header.Get("Authorization"); "" != authorization {
		auth.Set("Authorization", authorization)
	} else if cookie, err := r.Cookie("session"); nil == err {
		auth.Set("Cookie", cookie.String())
	}
	now := time.Now()
	s.ticketMu.Lock()
	for value, ticket := range s.tickets {
		if !now.Before(ticket.expires) {
			delete(s.tickets, value)
		}
	}
	s.tickets[value] = websocketTicket{expires: now.Add(30 * time.Second), auth: auth}
	s.ticketMu.Unlock()

	jsonResponse(w, http.StatusCreated, map[string]any{"ticket": value, "expires_in": 30})
}

func (s *Server) consumeWebsocketTicket(value string) (websocketTicket, bool) {
	s.ticketMu.Lock()
	defer s.ticketMu.Unlock()
	ticket, exists := s.tickets[value]
	delete(s.tickets, value)
	if !exists || !time.Now().Before(ticket.expires) {
		return websocketTicket{}, false
	}
	return ticket, true
}

func ticketOrigin(r *http.Request, origins []string) bool {
	values := r.Header.Values("Origin")
	if 1 != len(values) {
		return false
	}
	origin, valid := validWebsocketOrigin(values[0])
	if !valid {
		return false
	}
	for _, value := range origins {
		allowed, valid := validWebsocketOrigin(value)
		if valid && strings.EqualFold(origin.Scheme, allowed.Scheme) && strings.EqualFold(origin.Host, allowed.Host) {
			return true
		}
	}
	return false
}

func validWebsocketOrigin(value string) (*url.URL, bool) {
	u, err := url.Parse(value)
	if nil != err || ("http" != u.Scheme && "https" != u.Scheme) || "" == u.Hostname() || nil != u.User || "" != u.Path ||
		"" != u.RawQuery ||
		"" != u.Fragment ||
		strings.ContainsAny(value, "*?#") {
		return nil, false
	}
	if strings.HasPrefix(u.Host, "[") {
		if address, err := netip.ParseAddr(u.Hostname()); nil != err || !address.Is6() {
			return nil, false
		}
	} else if strings.ContainsAny(u.Hostname(), ":[]") {
		return nil, false
	}
	return u, true
}
