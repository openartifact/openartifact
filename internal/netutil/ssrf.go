// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package netutil provides SSRF-safe outbound HTTP helpers.
package netutil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// EnvAllowPrivate, when "true", permits outbound requests to private,
// loopback and link-local addresses (for on-prem upstream registries).
// Cloud metadata addresses (169.254.0.0/16, fd00:ec2::/16) remain blocked.
const EnvAllowPrivate = "OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS"

func allowPrivate() bool { return strings.EqualFold(os.Getenv(EnvAllowPrivate), "true") }

// ErrCannotResolve is returned when the host name does not resolve. Unlike
// ErrBlockedAddress it says nothing about the destination, only that the
// resolver or the network is not answering.
var ErrCannotResolve = errors.New("cannot resolve host")

// ErrBlockedAddress is returned when a destination address is not permitted.
var ErrBlockedAddress = errors.New("destination address is not allowed")

func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if blockedAddr(ip) {
		return true
	}

	// Transition mechanisms smuggle an IPv4 address through an IPv6 one; the
	// embedded address gets the same verdict it would get on its own.
	if emb, ok := embeddedIPv4(ip); ok {
		return blockedIP(emb)
	}

	return false
}

// blockedAddr applies the policy to ip itself, without looking inside
// IPv6 transition addresses.
func blockedAddr(ip netip.Addr) bool {
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true // includes 169.254.169.254 metadata
	}

	if ip.Is4() {
		if alwaysBlocked[ip] || prefixesContain(alwaysBlockedV4, ip) {
			return true
		}
	} else if prefixesContain(alwaysBlockedV6, ip) {
		return true
	}

	if allowPrivate() {
		return false
	}

	return ip.IsLoopback() || ip.IsPrivate() || isCGNAT(ip)
}

func prefixesContain(ps []netip.Prefix, ip netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}

	return false
}

//nolint:gochecknoglobals // read-only tables
var (
	// Blocked even with private upstreams allowed (which re-allows CGNAT,
	// where Alibaba's metadata endpoint lives).
	alwaysBlockedV4 = mustPrefixes(
		"0.0.0.0/8",     // "this network"
		"192.0.0.0/24",  // IETF protocol assignments
		"198.18.0.0/15", // benchmarking
		"240.0.0.0/4",   // reserved, incl. 255.255.255.255
	)
	alwaysBlockedV6 = mustPrefixes(
		"fd00:ec2::/32", // AWS IMDS over IPv6
		"100::/64",      // discard-only
		"2001::/32",     // Teredo: embedded addresses are obfuscated
	)

	nat64      = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour  = netip.MustParsePrefix("2002::/16")
	v4Compat   = netip.MustParsePrefix("::/96")
	v6Loopback = netip.MustParseAddr("::1")
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}

	return out
}

// embeddedIPv4 extracts the IPv4 address carried by an IPv6 NAT64, 6to4 or
// IPv4-compatible address.
func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() {
		return netip.Addr{}, false
	}

	b := ip.As16()

	switch {
	case nat64.Contains(ip), v4Compat.Contains(ip) && ip != v6Loopback:
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
	case nat64Local.Contains(ip):
		// RFC 6052 /48 layout: bits 48-63 and 72-87 (byte 8 is reserved).
		return netip.AddrFrom4([4]byte{b[6], b[7], b[9], b[10]}), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), true
	}

	return netip.Addr{}, false
}

var errBadNetwork = errors.New("invalid IP or CIDR")

//nolint:gochecknoglobals // read-only table
var alwaysBlocked = map[netip.Addr]bool{
	netip.MustParseAddr("168.63.129.16"):   true, // Azure wire server
	netip.MustParseAddr("100.100.100.200"): true, // Alibaba Cloud metadata
}

// ParseNetworks parses CIDR blocks, accepting bare IPs as /32 (/128). Blank
// entries are skipped; any unparsable entry is an error.
func ParseNetworks(entries []string) ([]*net.IPNet, error) {
	var out []*net.IPNet

	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}

		if !strings.Contains(e, "/") {
			ip := net.ParseIP(e)
			if ip == nil {
				return nil, fmt.Errorf("%w %q", errBadNetwork, e)
			}

			bits := 128
			if ip4 := ip.To4(); ip4 != nil {
				ip, bits = ip4, 32
			}

			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})

			continue
		}

		_, n, err := net.ParseCIDR(e)
		if err != nil {
			return nil, fmt.Errorf("%w %q: %w", errBadNetwork, e, err)
		}

		out = append(out, n)
	}

	return out, nil
}

func isCGNAT(ip netip.Addr) bool {
	return ip.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

var (
	errSchemeNotAllowed = errors.New("URL scheme must be http or https")
	errHostRequired     = errors.New("URL host is required")
	errTooManyRedirects = errors.New("too many redirects")
	errRedirectScheme   = errors.New("redirect to unsupported scheme")
)

// ValidateOutboundURL checks scheme, host and (resolved) addresses of raw.
// It is ValidateOutboundURLContext with a background context.
func ValidateOutboundURL(raw string) error {
	return ValidateOutboundURLContext(context.Background(), raw)
}

// ValidateOutboundURLContext checks scheme, host and (resolved) addresses of
// raw, resolving the host under ctx.
func ValidateOutboundURLContext(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errSchemeNotAllowed
	}
	host := u.Hostname()
	if host == "" {
		return errHostRequired
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if blockedIP(ip) {
			return ErrBlockedAddress
		}
		return nil
	}
	ips, err := (&net.Resolver{}).LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("%w %q", ErrCannotResolve, host)
	}
	for _, ipAddr := range ips {
		if a, ok := netip.AddrFromSlice(ipAddr.IP); ok && blockedIP(a) {
			return ErrBlockedAddress
		}
	}
	return nil
}

func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host
}

// stripCredentials removes the headers that authenticate the caller. Go's own
// client forwards the initial request's headers on redirects and only drops
// sensitive ones on a cross-domain hop, so a scheme downgrade on the same host
// would otherwise leak them.
func stripCredentials(h http.Header) {
	h.Del("Authorization")
	h.Del("Proxy-Authorization")
	h.Del("Cookie")
	h.Del("Cookie2")
}

// SafeHTTPClient returns a client that refuses to connect to blocked
// addresses (checked at dial time, defeating DNS rebinding) and caps and
// re-validates redirects, dropping credentials on cross-host redirects.
func SafeHTTPClient(timeout time.Duration) *http.Client {
	return SafeHTTPClientWithHeaderTimeout(timeout, 0)
}

// SafeHTTPClientWithHeaderTimeout is SafeHTTPClient with an additional bound
// on the wait between sending a request and receiving the response headers
// (0 means unbounded, leaving only the overall timeout). Body transfer is not
// covered by it, so large downloads are unaffected.
func SafeHTTPClientWithHeaderTimeout(timeout, responseHeaderTimeout time.Duration) *http.Client {
	d := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return err
			}
			if blockedIP(ap.Addr()) {
				return ErrBlockedAddress
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           func(ctx context.Context, n, a string) (net.Conn, error) { return d.DialContext(ctx, n, a) },
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		ForceAttemptHTTP2:     true,
		IdleConnTimeout:       60 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errTooManyRedirects
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errRedirectScheme
			}
			// Go re-copies the initial request's headers on every hop, so the
			// origin is compared with both the first and the previous hop: a
			// credential dropped once is never sent again (https://a ->
			// https://b -> https://a), and a scheme change drops it too.
			if !sameOrigin(req.URL, via[0].URL) || !sameOrigin(req.URL, via[len(via)-1].URL) {
				stripCredentials(req.Header)
			}
			return nil
		},
	}
}
