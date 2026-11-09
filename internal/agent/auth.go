package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const sourceDNSCacheTTL = time.Minute

type hostAddresses struct {
	addresses []netip.Addr
	expires   time.Time
}

type hostCache struct {
	mu      sync.Mutex
	entries map[string]hostAddresses
	lookup  func(context.Context, string, string) ([]netip.Addr, error)
}

func parseSourceEntries(entries []string) ([]netip.Prefix, []string, error) {
	var prefixes []netip.Prefix
	var hosts []string
	for _, entry := range entries {
		if prefix, err := netip.ParsePrefix(entry); nil == err {
			prefixes = append(prefixes, prefix.Masked())
		} else if ip, err := netip.ParseAddr(entry); nil == err {
			ip = ip.Unmap()
			prefixes = append(prefixes, netip.PrefixFrom(ip, ip.BitLen()))
		} else if sourceHostname(entry) {
			hosts = append(hosts, strings.ToLower(entry))
		} else {
			return nil, nil, fmt.Errorf("invalid IP, CIDR or hostname: %q", entry)
		}
	}
	return prefixes, hosts, nil
}

func sourceHostname(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if 0 == len(host) || 253 < len(host) {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if 0 == len(label) || 63 < len(label) || '-' == label[0] || '-' == label[len(label)-1] {
			return false
		}
		for _, char := range label {
			if ('a' <= char && 'z' >= char) || ('A' <= char && 'Z' >= char) || ('0' <= char && '9' >= char) || '-' == char || '_' == char {
				continue
			}
			return false
		}
	}
	return true
}

func (c *hostCache) addresses(ctx context.Context, host string) []netip.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[host]; ok && time.Now().Before(entry.expires) {
		return entry.addresses
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := c.lookup(lookupCtx, "ip", host)
	if nil != err {
		addresses = nil
		slog.Warn(
			fmt.Sprintf("Could not resolve authentication hostname %q: %v. Retrying in %s.", host, err, sourceDNSCacheTTL.String()),
			"host",
			host,
			"error",
			err,
			"retry_in",
			sourceDNSCacheTTL,
		)
	}
	c.entries[host] = hostAddresses{addresses: addresses, expires: time.Now().Add(sourceDNSCacheTTL)}
	return addresses
}

func (a *agent) matchesSource(ip netip.Addr, prefixes []netip.Prefix, hosts []string) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	for _, host := range hosts {
		for _, address := range a.dns.addresses(a.ctx, host) {
			if ip == address.Unmap() {
				return true
			}
		}
	}
	return false
}

func (a *agent) sourceIP(r *http.Request) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if nil != err {
		return netip.Addr{}, fmt.Errorf("invalid connection source address")
	}
	ip, err := netip.ParseAddr(host)
	if nil != err {
		return netip.Addr{}, fmt.Errorf("invalid connection source IP")
	}
	ip = ip.Unmap()
	forwarded := r.Header.Values("X-Forwarded-For")
	if 0 == len(forwarded) {
		return ip, nil
	}
	chain := strings.Split(strings.Join(forwarded, ","), ",")
	for index := len(chain) - 1; 0 <= index; index-- {
		if !a.matchesSource(ip, a.config.trusted, a.config.trustedHosts) {
			return ip, nil
		}
		ip, err = netip.ParseAddr(strings.TrimSpace(chain[index]))
		if nil != err {
			return netip.Addr{}, fmt.Errorf("invalid X-Forwarded-For address from trusted proxy %s", host)
		}
		ip = ip.Unmap()
	}
	return ip, nil
}

func (a *agent) authorized(w http.ResponseWriter, r *http.Request) bool {
	ip, err := a.sourceIP(r)
	if nil != err {
		slog.Warn(
			fmt.Sprintf("Rejected %s %s from peer %s: cannot determine the connection source: %v", r.Method, r.URL.Path, r.RemoteAddr, err),
			"peer",
			r.RemoteAddr,
			"error",
			err,
		)
		respondError(w, r, fault(403, "Cannot determine the connection source. Check trusted proxy and forwarding settings."))
		return false
	}
	if !a.matchesSource(ip, a.config.allowed, a.config.allowedHosts) {
		slog.Warn(
			fmt.Sprintf(
				"Rejected %s %s from source %s via peer %s: connection source is not allowed by auth.allowed_ips.",
				r.Method,
				r.URL.Path,
				ip,
				r.RemoteAddr,
			),
			"peer",
			r.RemoteAddr,
			"source",
			ip,
			"allowed_ips",
			a.config.Auth.AllowedIPs,
		)
		respondError(w, r, fault(403, "Connection source is not allowed by auth.allowed_ips."))
		return false
	}
	header := r.Header.Get("Authorization")
	parts := strings.SplitN(header, " ", 2)
	token := ""
	if 2 == len(parts) && strings.EqualFold(parts[0], "Bearer") {
		token = parts[1]
	}
	hash := sha256.Sum256([]byte(token))
	if 1 != subtle.ConstantTimeCompare(hash[:], a.config.token[:]) || "" == token {
		w.Header().Set("WWW-Authenticate", "Bearer")
		slog.Warn(
			fmt.Sprintf(
				"Rejected %s %s from source %s via peer %s: missing or invalid bearer token.",
				r.Method,
				r.URL.Path,
				ip,
				r.RemoteAddr,
			),
			"peer",
			r.RemoteAddr,
			"source",
			ip,
		)
		respondError(w, r, fault(401, "invalid bearer token"))
		return false
	}
	return true
}
