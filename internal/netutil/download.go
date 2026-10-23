// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

var errRedirectNotHTTPS = errors.New("redirect to a non-https URL")

// NewDownloadClient returns a client for fetching one operator-configured
// URL, such as a release archive on a mirror.
//
// Unlike SafeHTTPClient it connects to trustedHost (host or host:port of the
// configured URL) even when that is on a private network, because the
// operator chose it, and it honours HTTP_PROXY, HTTPS_PROXY and NO_PROXY like
// the rest of an enterprise network expects. A redirect is a different
// matter: the server, not the operator, picks its target, so a redirect must
// stay on https, go to at most 3 hops, and either return to trustedHost or
// reach only addresses ValidateOutboundURL allows (no private, loopback,
// link-local or metadata ranges). That is enforced where the connection is
// made, so a DNS answer that changes between a check and the dial cannot get
// around it. When a proxy serves a request the proxy, not this process,
// resolves and connects to the host, so only the scheme and literal IP
// addresses can be checked here and the proxy's own egress policy applies.
func NewDownloadClient(timeout time.Duration, trustedHost string) *http.Client {
	return newDownloadClient(timeout, trustedHost, http.ProxyFromEnvironment)
}

func newDownloadClient(timeout time.Duration, trustedHost string, proxy func(*http.Request) (*url.URL, error)) *http.Client {
	trusted := dialAddress(trustedHost, "443")
	proxies := &proxySet{}

	dialer := &net.Dialer{
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
	// The operator chose the trusted host and the proxy: no address filter.
	trustedDialer := &net.Dialer{Timeout: 10 * time.Second}

	tr := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			u, err := proxy(req)
			if u != nil {
				proxies.add(u)
			}

			return u, err
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if strings.EqualFold(addr, trusted) || proxies.has(addr) {
				return trustedDialer.DialContext(ctx, network, addr)
			}

			return dialer.DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
		IdleConnTimeout:     30 * time.Second,
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errTooManyRedirects
			}

			if req.URL.Scheme != "https" {
				return errRedirectNotHTTPS
			}

			if strings.EqualFold(req.URL.Host, trustedHost) {
				return nil
			}

			stripCredentials(req.Header)

			// Literal addresses never reach a resolver, so they are checked
			// here whichever way the request travels.
			if ip, err := netip.ParseAddr(req.URL.Hostname()); err == nil && blockedIP(ip) {
				return ErrBlockedAddress
			}

			return nil
		},
	}
}

// dialAddress is host with port appended when it has none.
func dialAddress(host, defaultPort string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}

	return net.JoinHostPort(strings.Trim(host, "[]"), defaultPort)
}

// proxySet remembers the proxies the transport was told to use, so that
// connecting to one is not mistaken for connecting to a redirect target.
type proxySet struct {
	mu    sync.RWMutex
	addrs map[string]struct{}
}

func (p *proxySet) add(u *url.URL) {
	port := "80"

	switch u.Scheme {
	case "https":
		port = "443"
	case "socks5", "socks5h":
		port = "1080"
	}

	addr := strings.ToLower(dialAddress(u.Host, port))

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.addrs == nil {
		p.addrs = map[string]struct{}{}
	}

	p.addrs[addr] = struct{}{}
}

func (p *proxySet) has(addr string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	_, ok := p.addrs[strings.ToLower(addr)]

	return ok
}
