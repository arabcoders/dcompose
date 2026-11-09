package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func sourceRequest(a *agent, peer, token string, forwarded ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.RemoteAddr = peer
	r.Header.Set("Authorization", token)
	for _, value := range forwarded {
		r.Header.Add("X-Forwarded-For", value)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestHostAuth(t *testing.T) {
	c, err := loadConfig(configFixture(t, "", "allowed_ips = ['dash_service.invalid']"))
	if nil != err {
		t.Fatal(err)
	}
	type contextKey struct{}
	key := contextKey{}
	a := newAgent(context.WithValue(context.Background(), key, "agent"), c)
	calls := 0
	a.dns.lookup = func(ctx context.Context, network, host string) ([]netip.Addr, error) {
		calls++
		deadline, bounded := ctx.Deadline()
		if "ip" != network || "dash_service.invalid" != host || "agent" != ctx.Value(key) || nil != ctx.Err() || !bounded || 0 >= time.Until(deadline) || 5*time.Second < time.Until(deadline) {
			t.Fatalf("lookup did not use bounded agent context: %s %s %v", network, host, ctx.Err())
		}
		return []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("2001:db8::10"), netip.MustParseAddr("::ffff:192.0.2.11")}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodGet, "/health", nil).WithContext(ctx)
	r.RemoteAddr = "192.0.2.10:42"
	r.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if 200 != w.Code {
		t.Fatalf("request cancellation affected source lookup: %d %s", w.Code, w.Body)
	}
	for _, test := range []struct {
		peer, token string
		code        int
	}{
		{"192.0.2.10:42", "Bearer test-secret", 200},
		{"[2001:db8::10]:42", "Bearer test-secret", 200},
		{"[::ffff:192.0.2.10]:42", "Bearer test-secret", 200},
		{"192.0.2.11:42", "Bearer test-secret", 200},
		{"192.0.2.12:42", "Bearer test-secret", 403},
		{"192.0.2.10:42", "", 401},
		{"192.0.2.10:42", "Bearer wrong", 401},
	} {
		if w := sourceRequest(a, test.peer, test.token); test.code != w.Code {
			t.Fatalf("%s %q: %d %s", test.peer, test.token, w.Code, w.Body)
		}
	}
	if 1 != calls {
		t.Fatalf("hostname lookup count: %d", calls)
	}
}

func TestHostCache(t *testing.T) {
	c, err := loadConfig(configFixture(t, "", "allowed_ips = ['shared.invalid']\ntrusted_proxies = ['shared.invalid']"))
	if nil != err {
		t.Fatal(err)
	}
	a := newAgent(context.Background(), c)
	addresses := []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.11")}
	calls := 0
	a.dns.lookup = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		calls++
		if "shared.invalid" != host {
			t.Fatalf("unexpected hostname %q", host)
		}
		return addresses, nil
	}
	before := time.Now()
	for range 2 {
		if w := sourceRequest(a, "192.0.2.10:42", "Bearer test-secret", "192.0.2.11"); 200 != w.Code {
			t.Fatalf("shared hostname: %d %s", w.Code, w.Body)
		}
	}
	if w := sourceRequest(a, "192.0.2.10:42", "Bearer test-secret"); 200 != w.Code {
		t.Fatalf("missing forwarding header did not use peer: %d %s", w.Code, w.Body)
	}
	entry := a.dns.entries["shared.invalid"]
	if 1 != calls || entry.expires.Before(before.Add(time.Minute)) || entry.expires.After(time.Now().Add(time.Minute)) {
		t.Fatalf("cache was not shared for 60s: calls=%d expires=%v", calls, entry.expires)
	}
	addresses = []netip.Addr{netip.MustParseAddr("192.0.2.20"), netip.MustParseAddr("192.0.2.21")}
	entry.expires = time.Now().Add(-time.Second)
	a.dns.entries["shared.invalid"] = entry
	for _, test := range []struct {
		peer, forwarded string
		code            int
	}{
		{"192.0.2.20:42", "192.0.2.21", 200},
		{"192.0.2.10:42", "192.0.2.11", 403},
		{"192.0.2.20:42", "192.0.2.11", 403},
	} {
		if w := sourceRequest(a, test.peer, "Bearer test-secret", test.forwarded); test.code != w.Code {
			t.Fatalf("refreshed %s via %s: %d %s", test.forwarded, test.peer, w.Code, w.Body)
		}
	}
	if 2 != calls {
		t.Fatalf("expiry lookup count: %d", calls)
	}
}

func TestHostNegativeCache(t *testing.T) {
	c, err := loadConfig(configFixture(t, "", "allowed_ips = ['failed.invalid', 'other.invalid', '198.51.100.1']"))
	if nil != err {
		t.Fatal(err)
	}
	a := newAgent(context.Background(), c)
	calls := make(map[string]int)
	recovered := false
	a.dns.lookup = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		calls[host]++
		if "failed.invalid" == host {
			if !recovered {
				return nil, errors.New("DNS unavailable")
			}
			return []netip.Addr{netip.MustParseAddr("192.0.2.10")}, nil
		}
		if "other.invalid" != host {
			t.Fatalf("unexpected hostname %q", host)
		}
		return []netip.Addr{netip.MustParseAddr("192.0.2.20")}, nil
	}
	before := time.Now()
	for _, test := range []struct {
		peer string
		code int
	}{
		{"192.0.2.10:42", 403},
		{"192.0.2.10:42", 403},
		{"192.0.2.20:42", 200},
		{"198.51.100.1:42", 200},
	} {
		if w := sourceRequest(a, test.peer, "Bearer test-secret"); test.code != w.Code {
			t.Fatalf("negative cache %s: %d %s", test.peer, w.Code, w.Body)
		}
	}
	entry := a.dns.entries["failed.invalid"]
	if 1 != calls["failed.invalid"] || 1 != calls["other.invalid"] || 0 != len(entry.addresses) || entry.expires.Before(before.Add(time.Minute)) || entry.expires.After(time.Now().Add(time.Minute)) {
		t.Fatalf("negative cache: calls=%v entry=%+v", calls, entry)
	}
	recovered = true
	if w := sourceRequest(a, "192.0.2.10:42", "Bearer test-secret"); 403 != w.Code || 1 != calls["failed.invalid"] {
		t.Fatal("negative cache refreshed before expiry")
	}
	entry.expires = time.Now().Add(-time.Second)
	a.dns.entries["failed.invalid"] = entry
	if w := sourceRequest(a, "192.0.2.10:42", "Bearer test-secret"); 200 != w.Code || 2 != calls["failed.invalid"] {
		t.Fatalf("negative cache did not recover: %d %s, calls=%v", w.Code, w.Body, calls)
	}
}

func TestTrustedProxy(t *testing.T) {
	c, err := loadConfig(configFixture(t, "", "allowed_ips = ['192.0.2.10', '2001:db8::10']\ntrusted_proxies = ['198.51.100.0/24', '2001:db8:1::/64']"))
	if nil != err {
		t.Fatal(err)
	}
	a := newAgent(context.Background(), c)
	for _, test := range []struct {
		name, peer, token string
		forwarded         []string
		code              int
	}{
		{"untrusted_forgery", "203.0.113.1:42", "Bearer test-secret", []string{"192.0.2.10"}, 403},
		{"untrusted_allowed", "192.0.2.10:42", "Bearer test-secret", []string{"malformed"}, 200},
		{"single", "198.51.100.1:42", "Bearer test-secret", []string{"192.0.2.10"}, 200},
		{"multiple", "198.51.100.1:42", "Bearer test-secret", []string{"192.0.2.10, 198.51.100.2"}, 200},
		{"header_lines", "198.51.100.1:42", "Bearer test-secret", []string{"192.0.2.10", "198.51.100.2"}, 200},
		{"fake_left", "198.51.100.1:42", "Bearer test-secret", []string{"192.0.2.10, 203.0.113.1"}, 403},
		{"ignored_left", "198.51.100.1:42", "Bearer test-secret", []string{"malformed, 192.0.2.10, 198.51.100.2"}, 200},
		{"missing", "198.51.100.1:42", "Bearer test-secret", nil, 403},
		{"malformed", "198.51.100.1:42", "Bearer test-secret", []string{"192.0.2.10, malformed"}, 403},
		{"empty_hop", "198.51.100.1:42", "Bearer test-secret", []string{"192.0.2.10, , 198.51.100.2"}, 403},
		{"ipv6", "[2001:db8:1::1]:42", "Bearer test-secret", []string{"2001:db8::10"}, 200},
		{"no_bearer", "198.51.100.1:42", "", []string{"192.0.2.10"}, 401},
		{"bad_bearer", "198.51.100.1:42", "Bearer wrong", []string{"192.0.2.10"}, 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			if w := sourceRequest(a, test.peer, test.token, test.forwarded...); test.code != w.Code {
				t.Fatalf("got %d, want %d: %s", w.Code, test.code, w.Body)
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	r.RemoteAddr = "198.51.100.1:42"
	r.Header.Set("Authorization", "Bearer test-secret")
	r.Header.Set("Forwarded", "for=192.0.2.10")
	r.Header.Set("X-Real-IP", "192.0.2.10")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if 403 != w.Code {
		t.Fatalf("accepted unsupported forwarding headers: %d %s", w.Code, w.Body)
	}
}
