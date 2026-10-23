// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"net/http"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/middleware"
)

// serverCapabilities is advertised on ping. "revisions" is mandatory for a
// Conan 2 client and lets a Conan 1 client with revisions enabled use the v2
// routes; "checksum_deploy" is the upload by checksum of checksum.go. The other
// optional capabilities (oauth_token, matrix_params, only_v2) are not
// implemented, so they are not advertised.
const serverCapabilities = "revisions,checksum_deploy"

// handlePing answers the conan client's capability probe. The client pings
// {remote}/v1/ping even when it then speaks v2.
func (h *HTTPHandler) handlePing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Conan-Server-Capabilities", serverCapabilities)
	w.WriteHeader(http.StatusOK)
}

// handleAuthenticate exchanges the user:token Basic credentials of
// `conan remote login` for the token conan then sends as `Authorization:
// Bearer`. The token is the API token itself, which the RBAC middleware accepts
// in that position; nothing is minted. Anything but a valid token (a wrong
// password, a session cookie, the legacy API key) is a 401, so the client asks
// for the credentials again.
func (h *HTTPHandler) handleAuthenticate(w http.ResponseWriter, r *http.Request) {
	p := middleware.PrincipalFromContext(r.Context())
	if p == nil || p.IsAnonymous || p.Credential != auth.CredentialBasicToken {
		unauthorized(w)
		return
	}

	_, token, ok := r.BasicAuth()
	if !ok {
		unauthorized(w)
		return
	}

	writeText(w, token)
}

// handleCheckCredentials returns the name of the authenticated user; 401 when
// the request carries no valid credentials, which makes the client log in.
func (h *HTTPHandler) handleCheckCredentials(w http.ResponseWriter, r *http.Request) {
	p := middleware.PrincipalFromContext(r.Context())
	if p == nil || p.IsAnonymous {
		unauthorized(w)
		return
	}

	writeText(w, p.Username)
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="OpenArtifact"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// writeText answers a plain-text body, which is how conan reads tokens and user
// names.
func writeText(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	//nolint:gosec // G705: plain-text token or user name for the conan client, not HTML output
	_, _ = w.Write([]byte(body))
}
