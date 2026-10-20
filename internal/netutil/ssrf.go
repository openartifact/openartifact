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

// ErrBlockedAddress is returned when a destination address is not permitted.
var ErrBlockedAddress = errors.New("destination address is not allowed")

func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true // includes 169.254.169.254 metadata
	}
	if ip.Is6() && ip.As16()[0] == 0xfd && ip.As16()[1] == 0x00 && ip.As16()[2] == 0x0e && ip.As16()[3] == 0xc2 {
		return true
	}
	if allowPrivate() {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || isCGNAT(ip)
}

func isCGNAT(ip netip.Addr) bool {
	return ip.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

var (
	errSchemeNotAllowed = errors.New("URL scheme must be http or https")
	errHostRequired     = errors.New("URL host is required")
	errCannotResolve    = errors.New("cannot resolve host")
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
		return fmt.Errorf("%w %q", errCannotResolve, host)
	}
	for _, ipAddr := range ips {
		if a, ok := netip.AddrFromSlice(ipAddr.IP); ok && blockedIP(a) {
			return ErrBlockedAddress
		}
	}
	return nil
}

// SafeHTTPClient returns a client that refuses to connect to blocked
// addresses (checked at dial time, defeating DNS rebinding) and caps and
// re-validates redirects, dropping credentials on cross-host redirects.
func SafeHTTPClient(timeout time.Duration) *http.Client {
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
		Proxy:               nil,
		DialContext:         func(ctx context.Context, n, a string) (net.Conn, error) { return d.DialContext(ctx, n, a) },
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        20,
		IdleConnTimeout:     60 * time.Second,
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
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}
