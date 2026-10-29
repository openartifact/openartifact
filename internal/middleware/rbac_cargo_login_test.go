// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/publicurl"
	"github.com/openartifact/openartifact/pkg/logger"
)

// cargoChallenge answers an anonymous request for a private cargo repository
// through publicurl.Middleware and RBACMiddleware, and returns the response.
func cargoChallenge(t *testing.T, cfg RBACConfig, trusted func(*http.Request) bool, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	log, err := logger.New("error", "json", "/dev/null")
	require.NoError(t, err)

	db := &benchRBACDB{repos: map[string]*models.Repository{"crates": {Name: "crates", Format: "cargo"}}}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := publicurl.Middleware(cfg.ClientBaseURL, "http://fallback.test", trusted)(
		RBACMiddleware(cfg, db, auth.NewEvaluator(db, log, false), log)(next))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repos/crates/index/config.json", http.NoBody)
	req.Host = "a.example"
	req.RemoteAddr = "192.0.2.1:1234"

	if mutate != nil {
		mutate(req)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	return w
}

// Cargo only sends its token after a challenge naming its login page; the page
// is on BASE_URL when set, else on the origin the client reached, so the
// challenge no longer needs a configured BASE_URL.
func TestCargoLoginChallengeUsesDerivedOrigin(t *testing.T) {
	t.Parallel()

	forward := func(r *http.Request) {
		r.Header.Set("X-Forwarded-Host", "artifacts.example.com")
		r.Header.Set("X-Forwarded-Proto", "https")
	}

	tests := []struct {
		name    string
		cfg     RBACConfig
		trusted func(*http.Request) bool
		mutate  func(*http.Request)
		want    string
	}{
		{"explicit BASE_URL", RBACConfig{ClientBaseURL: "https://oa.example.com/"}, func(*http.Request) bool { return true }, forward,
			`Cargo login_url="https://oa.example.com/login"`},
		{"explicit BASE_URL with a path prefix", RBACConfig{ClientBaseURL: "https://oa.example.com/artifacts"}, nil, nil,
			`Cargo login_url="https://oa.example.com/artifacts/login"`},
		{"Host header", RBACConfig{}, nil, nil, `Cargo login_url="http://a.example/login"`},
		{"trusted peer forwards host and proto", RBACConfig{}, func(*http.Request) bool { return true }, forward,
			`Cargo login_url="https://artifacts.example.com/login"`},
		{"untrusted peer cannot forward", RBACConfig{}, func(*http.Request) bool { return false }, forward,
			`Cargo login_url="http://a.example/login"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := cargoChallenge(t, tc.cfg, tc.trusted, tc.mutate)

			require.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Contains(t, w.Header().Values("WWW-Authenticate"), tc.want)
		})
	}
}

// The configured BaseURL (the MCP challenge's resource_metadata) plays no part
// in Cargo's login_url: with no override the request's origin is used even
// though BaseURL holds the localhost default.
func TestCargoLoginChallengeIgnoresConfiguredBaseURL(t *testing.T) {
	t.Parallel()

	w := cargoChallenge(t, RBACConfig{BaseURL: "http://localhost:8080"}, nil, nil)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Header().Values("WWW-Authenticate"), `Cargo login_url="http://a.example/login"`)
	assert.Contains(t, w.Header().Get("Vary"), "X-Forwarded-Host")
}
