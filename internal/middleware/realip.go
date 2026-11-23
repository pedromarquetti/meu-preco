package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
)

// realIPKey stores the resolved client IP in the request context. The zero-size
// struct type makes accidental collisions with other context keys unlikely.
type realIPKey struct{}

const (
	// maxForwardedEntries bounds how many forwarding hops are inspected. A
	// client can send an arbitrarily long header; only the rightmost entries
	// matter for resolving the real client.
	maxForwardedEntries = 32
	// maxForwardedHeaderBytes bounds the bytes read from one forwarding header
	// before splitting. We keep the rightmost slice, which is the part that
	// determines the client.
	maxForwardedHeaderBytes = 8 << 10
	// maxWarnedPeers bounds the "untrusted peer" warning dedupe map so a flood
	// of connections cannot grow it without limit.
	maxWarnedPeers = 1024
)

// RealIPResolver resolves the real client address from proxy headers, but only
// when the immediate peer is a configured trusted proxy. It is safe for
// concurrent use.
type RealIPResolver struct {
	known []netip.Prefix
	log   *slog.Logger

	mu     sync.Mutex
	warned map[string]struct{}
}

// NewRealIPResolver builds a resolver from the trusted proxy prefixes. A nil
// logger disables the misconfiguration warning.
func NewRealIPResolver(known []netip.Prefix, log *slog.Logger) *RealIPResolver {
	return &RealIPResolver{
		known:  known,
		log:    log,
		warned: make(map[string]struct{}),
	}
}

// Resolve returns the best-known client IP for the request. When the immediate
// peer is not trusted, forwarding headers are ignored and the peer is returned.
func (resolver *RealIPResolver) Resolve(r *http.Request) string {
	peer, ok := parseRemoteAddr(r.RemoteAddr)
	if !ok {
		return peerIP(r.RemoteAddr)
	}

	if !resolver.isKnown(peer) {
		if hasForwardingHeader(r.Header) {
			resolver.warnUntrusted(peer.String())
		}
		return peer.String()
	}

	// X-Forwarded-For is the canonical chain: walk right-to-left and return
	// the closest hop that is not itself a known proxy. A client-injected
	// value sits on the far left and is therefore ignored.
	if addr, ok := resolver.fromEntries(xffEntries(r.Header.Values("X-Forwarded-For"))); ok {
		return addr.String()
	}

	// RFC 7239 Forwarded: for=... is the standards-compliant equivalent.
	if addr, ok := resolver.fromEntries(forwardedForEntries(r.Header.Values("Forwarded"))); ok {
		return addr.String()
	}

	// Single-value headers are last-resort hints. They are only meaningful
	// when X-Forwarded-For is absent; skip values that are themselves proxies.
	for _, header := range []string{"CF-Connecting-IP", "True-Client-IP", "X-Real-IP"} {
		if addr, ok := parseIPEntry(r.Header.Get(header)); ok && !resolver.isKnown(addr) {
			return addr.String()
		}
	}

	return peer.String()
}

// fromEntries walks a forwarding list from right to left and returns the first
// valid address that is not a known proxy.
func (rr *RealIPResolver) fromEntries(entries []string) (netip.Addr, bool) {
	for _, entry := range slices.Backward(entries) {
		addr, ok := parseIPEntry(entry)
		if !ok || rr.isKnown(addr) {
			continue
		}
		return addr, true
	}
	return netip.Addr{}, false
}

func (rr *RealIPResolver) isKnown(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range rr.known {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// warnUntrusted logs once per untrusted peer so a missing KNOWN_PROXIES entry
// is actionable without flooding the log.
func (rr *RealIPResolver) warnUntrusted(peer string) {
	if rr.log == nil {
		return
	}

	rr.mu.Lock()
	if _, seen := rr.warned[peer]; seen {
		rr.mu.Unlock()
		return
	}
	if len(rr.warned) >= maxWarnedPeers {
		rr.mu.Unlock()
		return
	}
	rr.warned[peer] = struct{}{}
	rr.mu.Unlock()

	rr.log.Warn(
		"proxy headers ignored from untrusted peer; add it to KNOWN_PROXIES if it is a trusted proxy",
		"peer", peer,
	)
}

// RealIP is the middleware that stores the resolved client IP in the request
// context. ClientIP and every rate-limit key read it from there.
func RealIP(rr *RealIPResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rr == nil {
				next.ServeHTTP(w, r)
				return
			}

			ip := rr.Resolve(r)
			if ip == "" {
				next.ServeHTTP(w, r)
				return
			}

			ctx := context.WithValue(r.Context(), realIPKey{}, ip)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// peerIP returns the host portion of a RemoteAddr, tolerating values without a
// port (tests often set "203.0.113.9").
func peerIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func parseRemoteAddr(remoteAddr string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(peerIP(remoteAddr))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// parseIPEntry accepts the shapes seen in forwarding headers:
//
//	1.2.3.4
//	1.2.3.4:5678
//	2001:db8::1
//	[2001:db8::1]
//	[2001:db8::1]:443
//	"203.0.113.9" (RFC 7239 quoted form)
func parseIPEntry(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	if s == "" {
		return netip.Addr{}, false
	}

	if strings.HasPrefix(s, "[") {
		end := strings.IndexByte(s, ']')
		if end < 0 {
			return netip.Addr{}, false
		}
		rest := s[end+1:]
		if rest != "" && !strings.HasPrefix(rest, ":") {
			return netip.Addr{}, false
		}
		return parseAddr(s[1:end])
	}

	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap(), true
	}

	// IPv4 with a port has exactly one colon. Bare IPv6 was handled above.
	if strings.Count(s, ":") == 1 {
		if host, _, err := net.SplitHostPort(s); err == nil {
			return parseAddr(host)
		}
	}

	return netip.Addr{}, false
}

func parseAddr(s string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// xffEntries flattens X-Forwarded-For values into a list, keeping at most the
// rightmost maxForwardedEntries entries.
func xffEntries(values []string) []string {
	entries := make([]string, 0, len(values))
	for _, value := range values {
		if len(value) > maxForwardedHeaderBytes {
			value = value[len(value)-maxForwardedHeaderBytes:]
		}
		for part := range strings.SplitSeq(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			entries = append(entries, part)
		}
	}
	return rightmost(entries, maxForwardedEntries)
}

// forwardedForEntries extracts the for= parameter from RFC 7239 Forwarded
// headers, keeping at most the rightmost maxForwardedEntries entries.
func forwardedForEntries(values []string) []string {
	entries := make([]string, 0, len(values))
	for _, value := range values {
		if len(value) > maxForwardedHeaderBytes {
			value = value[len(value)-maxForwardedHeaderBytes:]
		}
		for elem := range strings.SplitSeq(value, ",") {
			for param := range strings.SplitSeq(elem, ";") {
				key, val, ok := strings.Cut(param, "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(key), "for") {
					continue
				}
				val = strings.Trim(strings.TrimSpace(val), `"`)
				if val == "" || strings.HasPrefix(val, "_") {
					continue
				}
				entries = append(entries, val)
			}
		}
	}
	return rightmost(entries, maxForwardedEntries)
}

func rightmost(entries []string, limit int) []string {
	if len(entries) > limit {
		return entries[len(entries)-limit:]
	}
	return entries
}

func hasForwardingHeader(h http.Header) bool {
	for _, key := range []string{
		"X-Forwarded-For",
		"Forwarded",
		"CF-Connecting-IP",
		"True-Client-IP",
		"X-Real-IP",
	} {
		if h.Get(key) != "" {
			return true
		}
	}
	return false
}
