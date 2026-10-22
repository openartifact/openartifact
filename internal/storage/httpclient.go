// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"net"
	"net/http"
	"time"
)

const (
	storageDialTimeout           = 10 * time.Second
	storageTLSHandshakeTimeout   = 10 * time.Second
	storageResponseHeaderTimeout = 60 * time.Second
)

// newStreamingHTTPClient returns an HTTP client for blob backends spoken to
// over plain REST (Azure, OCI). It deliberately has no http.Client.Timeout:
// that deadline covers reading the response body, so it would cut off any
// blob that takes longer than the limit to stream (and any large upload).
// Slow or dead peers are bounded by dial, TLS and response-header timeouts;
// the caller's context bounds the transfer itself. A nil base uses a clone of
// http.DefaultTransport semantics.
func newStreamingHTTPClient(base http.RoundTripper) *http.Client {
	if base == nil {
		base = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: storageDialTimeout, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   storageTLSHandshakeTimeout,
			ResponseHeaderTimeout: storageResponseHeaderTimeout,
			ExpectContinueTimeout: time.Second,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		}
	}

	return &http.Client{Transport: base}
}
