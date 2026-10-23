// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"errors"
	"net/http"
	"strings"
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
// resolve only to addresses ValidateOutboundURL allows (no private, loopback,
// link-local or metadata ranges).
func NewDownloadClient(timeout time.Duration, trustedHost string) *http.Client {
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
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

			if !strings.EqualFold(req.URL.Host, trustedHost) {
				stripCredentials(req.Header)

				return ValidateOutboundURLContext(req.Context(), req.URL.String())
			}

			return nil
		},
	}
}
