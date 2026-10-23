// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

type resourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
}

func (st *stack) metadata(path string) (*httptest.ResponseRecorder, resourceMetadata) {
	st.t.Helper()

	req := httptest.NewRequestWithContext(st.t.Context(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	st.handler.ServeHTTP(rec, req)

	var meta resourceMetadata
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
			st.t.Fatalf("invalid metadata %q: %v", rec.Body.String(), err)
		}
	}

	return rec, meta
}

func oidcProvider(slug, issuer string) *models.SSOProvider {
	cfg, _ := json.Marshal(models.OIDCConfig{Issuer: issuer, ClientID: "cid"})

	return &models.SSOProvider{ID: "id-" + slug, Slug: slug, Kind: "oidc", Enabled: true, OIDCConfig: cfg}
}

func TestProtectedResourceMetadataWithoutOIDC(t *testing.T) {
	st := newStack(t, nil)

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		rec, meta := st.metadata(path) // no credentials: the document is public

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", path, rec.Code)
		}

		if meta.Resource != "https://artifacts.example.com/mcp" {
			t.Errorf("%s: resource = %q", path, meta.Resource)
		}

		if !slices.Equal(meta.BearerMethodsSupported, []string{"header"}) {
			t.Errorf("%s: bearer_methods_supported = %v, API tokens travel in the header", path, meta.BearerMethodsSupported)
		}

		if len(meta.AuthorizationServers) != 0 {
			t.Errorf("%s: authorization_servers = %v, want none without an OIDC provider", path, meta.AuthorizationServers)
		}
	}
}

func TestProtectedResourceMetadataListsOIDCIssuers(t *testing.T) {
	st := newStack(t, nil)
	st.db.providers = []*models.SSOProvider{
		oidcProvider("corp", "https://idp.example.org"),
		oidcProvider("corp-again", "https://idp.example.org"),
		{ID: "saml", Slug: "saml", Kind: "saml", Enabled: true},
	}

	rec, meta := st.metadata("/.well-known/oauth-protected-resource/mcp")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	if want := []string{"https://idp.example.org"}; !slices.Equal(meta.AuthorizationServers, want) {
		t.Fatalf("authorization_servers = %v, want %v", meta.AuthorizationServers, want)
	}
}

func TestProtectedResourceMetadataDerivesResourceWithoutBaseURL(t *testing.T) {
	st := newStack(t, func(o *stackOptions) { o.baseURL = "" })

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/.well-known/oauth-protected-resource/mcp", nil)
	req.Host = "registry.internal:8080"

	rec := httptest.NewRecorder()
	st.handler.ServeHTTP(rec, req)

	var meta resourceMetadata
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}

	if meta.Resource != "http://registry.internal:8080/mcp" {
		t.Fatalf("resource = %q", meta.Resource)
	}
}

func TestProtectedResourceMetadataIsReadOnly(t *testing.T) {
	st := newStack(t, nil)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/.well-known/oauth-protected-resource", nil)
	rec := httptest.NewRecorder()
	st.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// oidcAccessToken is what the stub verifier accepts as an OIDC access token for
// the "reader" user.
const oidcAccessToken = "header.payload.signature"

func stubOIDCVerifier(st *stack) func(context.Context, string) (*models.User, error) {
	return func(_ context.Context, token string) (*models.User, error) {
		if token == oidcAccessToken {
			return st.db.users["reader"], nil
		}

		return nil, auth.ErrAccessTokenNotAccepted
	}
}

func TestMCPAcceptsOIDCAccessTokenForTheMappedUser(t *testing.T) {
	var st *stack

	st = newStack(t, func(o *stackOptions) {
		o.bearerVerify = func(ctx context.Context, token string) (*models.User, error) { return stubOIDCVerifier(st)(ctx, token) }
	})

	rec := st.do(http.MethodPost, "", rpcBody(t, 1, "tools/call", map[string]any{"name": "list_repositories", "arguments": map[string]any{}}),
		map[string]string{"Authorization": "Bearer " + oidcAccessToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	resp := decodeToolResponse(t, rec)
	if got, want := repoNames(t, resp), []string{"images", "web-npm"}; !slices.Equal(got, want) {
		t.Fatalf("the OIDC caller must have the mapped user's permissions: %v, want %v", got, want)
	}

	bad := st.do(http.MethodPost, "", listBody(t), map[string]string{"Authorization": "Bearer another.jwt.token"})
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("a token the verifier rejects must be 401, got %d", bad.Code)
	}
}

func TestMCPOIDCVerifierErrorsDoNotLeak(t *testing.T) {
	st := newStack(t, func(o *stackOptions) {
		o.bearerVerify = func(context.Context, string) (*models.User, error) {
			return nil, errors.New("dial tcp 10.0.0.5:443: connection refused")
		}
	})

	rec := st.do(http.MethodPost, "", listBody(t), map[string]string{"Authorization": "Bearer a.b.c"})
	if rec.Code != http.StatusUnauthorized || containsAny(rec.Body.String(), "10.0.0.5", "dial") {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(sub string) bool { return strings.Contains(s, sub) })
}
