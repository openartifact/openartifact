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
	ip = ip.Unmap()
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true // includes 169.254.169.254 metadata
	}
	if ip.Is6() && ip.As16()[0] == 0xfd && ip.As16()[1] == 0x00 && ip.As16()[2] == 0x0e && ip.As16()[3] == 0xc2 {
		return true
	}
	// Cloud metadata endpoints outside link-local: always blocked, even with
	// allowPrivate (which re-allows CGNAT, where Alibaba's lives).
	if ip.Is4() && (ip.As4()[0] == 0 || alwaysBlocked[ip]) {
		return true // 0.0.0.0/8 "this network"
	}
	if allowPrivate() {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || isCGNAT(ip)
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
		MaxIdleConns:          20,
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
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}
