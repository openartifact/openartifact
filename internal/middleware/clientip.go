// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net"
	"net/http"
	"strings"
)

// TrustedProxies holds the parsed CIDR blocks of reverse proxies allowed to
// set X-Forwarded-For / X-Real-IP. Build one with NewTrustedProxies at
// startup and share it across IPAllowlistMiddleware, RateLimitMiddleware, and
// any other code that needs the caller's real IP (e.g. login rate limiting).
type TrustedProxies struct {
	networks []*net.IPNet
}

// NewTrustedProxies parses cidrs into a TrustedProxies set. Invalid or blank
// entries are skipped. A nil/empty result still behaves correctly: no proxy
// is trusted, so ClientIP always falls back to the TCP peer address.
func NewTrustedProxies(cidrs []string) *TrustedProxies {
	t := &TrustedProxies{}

	for _, cidr := range cidrs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}

		if _, network, err := net.ParseCIDR(cidr); err == nil {
			t.networks = append(t.networks, network)
		}
	}

	return t
}

// Configured reports whether at least one trusted proxy network is set. When it
// is false ClientIP always returns the TCP peer address.
func (t *TrustedProxies) Configured() bool {
	return t != nil && len(t.networks) > 0
}

func (t *TrustedProxies) trusts(ip net.IP) bool {
	if t == nil || ip == nil {
		return false
	}

	for _, network := range t.networks {
		if network.Contains(ip) {
			return true
		}
	}

	return false
}

// remoteHost strips the port from an http.Request.RemoteAddr, which is
// always "host:port" (or "[ipv6]:port") as set by net/http itself — never
// attacker-controlled.
func remoteHost(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}

	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}

	return remoteAddr
}

// ClientIP returns the request's client IP. X-Forwarded-For / X-Real-IP are
// honored ONLY when the immediate TCP peer (r.RemoteAddr) is a configured
// trusted proxy; otherwise the peer address itself is returned. This means a
// direct, non-proxied client can never spoof its own IP by setting either
// header — the classic bypass this replaces.
func ClientIP(r *http.Request, trusted *TrustedProxies) string {
	peer := remoteHost(r.RemoteAddr)

	if trusted == nil || len(trusted.networks) == 0 {
		return peer
	}

	peerIP := net.ParseIP(peer)
	if peerIP == nil || !trusted.trusts(peerIP) {
		return peer
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if ip := strings.TrimSpace(strings.Split(xff, ",")[0]); ip != "" {
			return ip
		}
	}

	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	return peer
}
