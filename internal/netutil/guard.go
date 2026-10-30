// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"time"
)

// GuardTransport makes tr connect only to addresses ValidateOutboundURLContext
// would accept, checked where the connection is made (dialControl), so a DNS
// answer that changes after a save-time check cannot reach a private or
// metadata address. The proxies tr.Proxy names are exempt: the operator chose
// them, and a proxy resolves and connects to the destination under its own
// egress policy. tr keeps its other settings.
func GuardTransport(tr *http.Transport) {
	proxy := tr.Proxy
	if proxy == nil {
		proxy = func(*http.Request) (*url.URL, error) { return nil, nil } //nolint:nilnil // no proxy
	}

	proxies := &proxySet{}
	guarded := safeDialer()
	trusted := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

	tr.Proxy = func(req *http.Request) (*url.URL, error) {
		u, err := proxy(req)
		if u != nil {
			proxies.add(u)
		}

		return u, err
	}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if proxies.has(addr) {
			return trusted.DialContext(ctx, network, addr)
		}

		return guarded.DialContext(ctx, network, addr)
	}
}
