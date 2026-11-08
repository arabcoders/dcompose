package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTicketMint(t *testing.T) {
	for _, auth := range []string{"session", "bearer"} {
		t.Run(auth, func(t *testing.T) {
			s := testServer(t, nil)
			r := httptest.NewRequest("POST", "/api/auth/tickets", strings.NewReader(`{}`))
			if "session" == auth {
				r.AddCookie(testSession(t, s))
			} else {
				s.config.Auth.apiKey = strings.Repeat("cd", 32)
				r.Header.Set("Authorization", "Bearer "+s.config.Auth.apiKey)
			}
			before := time.Now()
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			var response struct {
				Ticket    string `json:"ticket"`
				ExpiresIn int    `json:"expires_in"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || http.StatusCreated != w.Code || 30 != response.ExpiresIn {
				t.Fatal("ticket issuance failed", w.Code, err)
			}
			decoded, err := base64.RawURLEncoding.DecodeString(response.Ticket)
			if nil != err || 32 != len(decoded) {
				t.Fatal("ticket must contain 32 random bytes", err)
			}
			if "no-store" != w.Header().Get("Cache-Control") || "application/json; charset=utf-8" != w.Header().Get("Content-Type") {
				t.Fatal("incorrect ticket response headers")
			}
			ticket, exists := s.tickets[response.Ticket]
			if !exists || ticket.expires.Before(before.Add(30*time.Second)) || ticket.expires.After(time.Now().Add(30*time.Second)) {
				t.Fatal("incorrect ticket expiry")
			}
			if 0 != len(w.Result().Cookies()) {
				t.Fatal("ticket issuance created a session")
			}
		})
	}
}

func TestTicketBody(t *testing.T) {
	s := testServer(t, nil)
	for _, body := range []string{
		``, `null`, `[]`, `true`, `"ticket"`, `{"permissions":["read"]}`, `{"extra":true}`,
		`{} {}`, `{"`, strings.Repeat(" ", 16<<10) + `{}`,
	} {
		w := perform(t, s, "POST", "/api/auth/tickets", body)
		var response struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || 400 != w.Code || 400 != response.Error.Code || "" == response.Error.Message {
			t.Fatal("invalid ticket body accepted or incorrect error response", w.Code, err)
		}
	}
	if 0 != len(s.tickets) {
		t.Fatal("invalid body minted tickets")
	}
}

func TestTicketAuth(t *testing.T) {
	s := testServer(t, nil)
	for _, header := range []string{"", "Bearer invalid"} {
		r := httptest.NewRequest("POST", "/api/auth/tickets", strings.NewReader(`{}`))
		if "" != header {
			r.Header.Set("Authorization", header)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if 401 != w.Code {
			t.Fatal("unauthenticated ticket issued", w.Code)
		}
	}
	if 0 != len(s.tickets) {
		t.Fatal("unauthenticated request minted a ticket")
	}
}

func TestTicketREST(t *testing.T) {
	s := testServer(t, nil)
	w := perform(t, s, "POST", "/api/auth/tickets", `{}`)
	var response struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); nil != err || 201 != w.Code || "" == response.Ticket {
		t.Fatal("ticket issuance failed", w.Code, err)
	}
	for _, credential := range []string{"bearer", "query", "cookie"} {
		r := httptest.NewRequest("GET", "/api/nodes", nil)
		switch credential {
		case "bearer":
			r.Header.Set("Authorization", "Bearer "+response.Ticket)
		case "query":
			r.URL.RawQuery = "ticket=" + response.Ticket
		case "cookie":
			r.AddCookie(&http.Cookie{Name: "session", Value: response.Ticket})
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if 401 != w.Code {
			t.Fatal("ticket accepted as REST credential", credential, w.Code)
		}
	}
	if _, valid := s.consumeWebsocketTicket(response.Ticket); !valid {
		t.Fatal("REST attempt consumed ticket")
	}
}

func TestTicketConsume(t *testing.T) {
	s := New(Config{}, nil)
	future := time.Now().Add(time.Minute)
	s.tickets["valid"] = websocketTicket{expires: future}
	ticket, valid := s.consumeWebsocketTicket("valid")
	if !valid || !ticket.expires.Equal(future) {
		t.Fatal("valid ticket rejected")
	}
	if _, valid := s.consumeWebsocketTicket("valid"); valid {
		t.Fatal("ticket replay accepted")
	}
	s.tickets["expired"] = websocketTicket{expires: time.Now().Add(-time.Second)}
	if _, valid := s.consumeWebsocketTicket("expired"); valid {
		t.Fatal("expired ticket accepted")
	}
	if _, exists := s.tickets["expired"]; exists {
		t.Fatal("expired ticket not deleted")
	}
	if _, valid := s.consumeWebsocketTicket("missing"); valid {
		t.Fatal("unknown ticket accepted")
	}
}

func TestTicketConcurrent(t *testing.T) {
	s := New(Config{}, nil)
	s.tickets["ticket"] = websocketTicket{expires: time.Now().Add(time.Minute)}
	var winners atomic.Int32
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			if _, valid := s.consumeWebsocketTicket("ticket"); valid {
				winners.Add(1)
			}
		}()
	}
	close(start)
	workers.Wait()
	if 1 != winners.Load() {
		t.Fatal("ticket must have one redemption winner", winners.Load())
	}
}

func TestTicketCleanup(t *testing.T) {
	s := testServer(t, nil)
	s.tickets["expired"] = websocketTicket{expires: time.Now().Add(-time.Second)}
	s.tickets["live"] = websocketTicket{expires: time.Now().Add(time.Minute)}
	if w := perform(t, s, "POST", "/api/auth/tickets", `{}`); 201 != w.Code {
		t.Fatal("ticket issuance failed", w.Code)
	}
	if _, exists := s.tickets["expired"]; exists {
		t.Fatal("mint did not remove expired ticket")
	}
	if _, exists := s.tickets["live"]; !exists || 2 != len(s.tickets) {
		t.Fatal("mint removed live ticket")
	}
}

func TestTicketOrigin(t *testing.T) {
	for _, test := range []struct {
		name    string
		values  []string
		origins []string
		valid   bool
	}{
		{name: "match", values: []string{"https://integration.example"}, origins: []string{"https://integration.example"}, valid: true},
		{name: "case", values: []string{"HTTPS://INTEGRATION.EXAMPLE"}, origins: []string{"https://integration.example"}, valid: true},
		{name: "ipv6", values: []string{"http://[::1]:8080"}, origins: []string{"http://[::1]:8080"}, valid: true},
		{name: "absent", origins: []string{"https://integration.example"}},
		{name: "empty", values: []string{""}, origins: []string{""}},
		{name: "disabled", values: []string{"https://integration.example"}},
		{name: "multiple", values: []string{"https://integration.example", "https://integration.example"}, origins: []string{"https://integration.example"}},
		{name: "joined", values: []string{"https://integration.example https://integration.example"}, origins: []string{"https://integration.example"}},
		{name: "scheme", values: []string{"http://integration.example"}, origins: []string{"https://integration.example"}},
		{name: "port", values: []string{"https://integration.example:443"}, origins: []string{"https://integration.example"}},
		{name: "host", values: []string{"https://other.example"}, origins: []string{"https://integration.example"}},
		{name: "wildcard", values: []string{"https://integration.example"}, origins: []string{"https://*.example"}},
		{name: "path", values: []string{"https://integration.example/"}, origins: []string{"https://integration.example/"}},
		{name: "query", values: []string{"https://integration.example?"}, origins: []string{"https://integration.example?"}},
		{name: "fragment", values: []string{"https://integration.example#"}, origins: []string{"https://integration.example#"}},
		{name: "userinfo", values: []string{"https://user@integration.example"}, origins: []string{"https://user@integration.example"}},
		{name: "null", values: []string{"null"}, origins: []string{"null"}},
		{name: "ipv6_invalid", values: []string{"http://[invalid]"}, origins: []string{"http://[invalid]"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/events", nil)
			if nil != test.values {
				r.Header["Origin"] = test.values
			}
			if test.valid != ticketOrigin(r, test.origins) {
				t.Fatal("incorrect ticket origin result")
			}
		})
	}
}

func TestWebsocketOrigins(t *testing.T) {
	for _, test := range []struct {
		origin string
		valid  bool
	}{
		{origin: "https://integration.example", valid: true},
		{origin: "HTTP://INTEGRATION.EXAMPLE:8080", valid: true},
		{origin: "http://[::1]", valid: true},
		{origin: "https://[2001:db8::1]:8443", valid: true},
		{origin: ""}, {origin: "*"}, {origin: "https://*.example"}, {origin: "https://integration?.example"},
		{origin: "https://user@integration.example"}, {origin: "https://integration.example/"},
		{origin: "https://integration.example/path"}, {origin: "https://integration.example?query=1"},
		{origin: "https://integration.example?"}, {origin: "https://integration.example#fragment"},
		{origin: "https://integration.example#"}, {origin: "wss://integration.example"},
		{origin: "//integration.example"}, {origin: "https://"}, {origin: "https://:443"},
		{origin: "https://integration.example:invalid"}, {origin: "https://[invalid]"},
		{origin: "https://::1"}, {origin: "https://[127.0.0.1]"},
	} {
		t.Run(test.origin, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "dcompose.toml")
			data := fmt.Sprintf("[global]\nwebsocket_origins = [%q]\n", test.origin)
			if err := os.WriteFile(path, []byte(data), 0600); nil != err {
				t.Fatal(err)
			}
			c, err := LoadConfig(path)
			if test.valid != (nil == err) {
				t.Fatal("incorrect origin validation result", err)
			}
			if test.valid {
				stored, err := readConfig(path)
				if nil != err || 1 != len(c.Global.WebsocketOrigins) || test.origin != c.Global.WebsocketOrigins[0] || 1 != len(stored.Global.WebsocketOrigins) || test.origin != stored.Global.WebsocketOrigins[0] {
					t.Fatal("origins not preserved", err)
				}
			}
		})
	}
}

func TestWebsocketOriginsClone(t *testing.T) {
	c := Config{Global: GlobalConfig{WebsocketOrigins: []string{"https://integration.example"}}}
	cloned := cloneConfig(c)
	cloned.Global.WebsocketOrigins[0] = "https://other.example"
	if "https://integration.example" != c.Global.WebsocketOrigins[0] {
		t.Fatal("clone shares origins slice")
	}
	s := New(c, nil)
	c.Global.WebsocketOrigins[0] = "https://other.example"
	snapshot, _ := s.snapshot()
	if "https://integration.example" != snapshot.Global.WebsocketOrigins[0] {
		t.Fatal("server shares input origins slice")
	}
	encoded, err := encodeConfig(Config{})
	if nil != err || strings.Contains(string(encoded), "websocket_origins") {
		t.Fatal("empty origins not omitted", err)
	}
}
