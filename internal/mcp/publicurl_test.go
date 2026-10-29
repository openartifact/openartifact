// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !nomcp

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/openartifact/openartifact/internal/publicurl"
	"github.com/openartifact/openartifact/pkg/logger"
)

// originHandler serves a server whose BASE_URL is explicit ("" when unset)
// behind publicurl.Middleware, as cmd/server mounts it ahead of the RBAC chain.
func originHandler(t *testing.T, explicit string, trusted func(*http.Request) bool) http.Handler {
	t.Helper()

	log := &logger.Logger{SugaredLogger: zap.NewNop().Sugar()}
	s := NewServer(fixtureDB(), nil, log, Options{Version: "1.2.3", BaseURL: explicit})
	s.SetAuthorizer(func(context.Context, string, string) bool { return true })

	return publicurl.Middleware(explicit, "http://fallback.test", trusted)(withPrincipal(s.HTTPHandler()))
}

// callAt runs a tool through h as a request with the given Host and extra
// headers from the TCP peer 192.0.2.1.
func callAt(t *testing.T, h http.Handler, host string, headers map[string]string, tool string, args map[string]any) string {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp",
		strings.NewReader(rpcBody(t, 1, "tools/call", map[string]any{"name": tool, "arguments": args})))
	req.Host = host
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	req.Header.Set("Authorization", "Bearer test")

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	resp := decodeToolResponse(t, rec)
	require.Nil(t, resp.Error, rec.Body.String())
	require.NotEmpty(t, resp.Result.Content, rec.Body.String())
	require.False(t, resp.Result.IsError, resp.Result.Content[0].Text)

	return resp.Result.Content[0].Text
}

// The tool handler's context is the HTTP request's own (the stateless Streamable
// HTTP transport passes req.Context() on, which is also how the authorizer finds
// the principal), so the origin publicurl derived for the request reaches the
// client-setup snippets and the download URLs.
func TestToolsUseTheRequestOrigin(t *testing.T) {
	t.Parallel()

	forward := map[string]string{"X-Forwarded-Host": "artifacts.example.com", "X-Forwarded-Proto": "https"}
	trustAll := func(*http.Request) bool { return true }
	trustNone := func(*http.Request) bool { return false }

	tests := []struct {
		name     string
		explicit string
		trusted  func(*http.Request) bool
		headers  map[string]string
		want     string
	}{
		{"explicit BASE_URL wins", "https://oa.example.com/", trustAll, forward, "https://oa.example.com"},
		{"Host header", "", trustNone, nil, "http://a.example"},
		{"trusted peer forwards host and proto", "", trustAll, forward, "https://artifacts.example.com"},
		{"untrusted peer cannot forward", "", trustNone, forward, "http://a.example"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := originHandler(t, tc.explicit, tc.trusted)

			var setup struct {
				BaseURL string `json:"base_url"`
			}

			text := callAt(t, h, "a.example", tc.headers, "client_setup", map[string]any{"repository": "web-npm"})
			require.NoError(t, json.Unmarshal([]byte(text), &setup))
			assert.Equal(t, tc.want+"/repos/web-npm", setup.BaseURL)
			assert.Contains(t, text, "npm config set registry "+tc.want+"/repos/web-npm/")

			text = callAt(t, h, "a.example", tc.headers, "get_package", map[string]any{"repository": "web-npm", "name": "left-pad"})
			assert.Contains(t, text, `"download_url":"`+tc.want+"/api/v1/repositories/web-npm/packages/c1/download\"")
		})
	}
}

// Without the middleware (or an explicit BASE_URL) there is no origin: download
// paths stay relative instead of inventing one.
func TestToolsWithoutAnOriginYieldRelativeDownloadPaths(t *testing.T) {
	t.Parallel()

	log := &logger.Logger{SugaredLogger: zap.NewNop().Sugar()}
	s := NewServer(fixtureDB(), nil, log, Options{Version: "1.2.3"})
	s.SetAuthorizer(func(context.Context, string, string) bool { return true })

	args := map[string]any{"repository": "web-npm", "name": "left-pad"}
	text := callAt(t, withPrincipal(s.HTTPHandler()), "a.example", nil, "get_package", args)
	assert.Contains(t, text, `"download_url":"/api/v1/repositories/web-npm/packages/c1/download"`)
}
