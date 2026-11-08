// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
)

func ssoCall(t *testing.T, router chi.Router, p *auth.Principal, method, path string, body any) (int, map[string]any) {
	t.Helper()

	var rdr *bytes.Reader

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}

		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}

	req := httptest.NewRequestWithContext(context.Background(), method, path, rdr)
	req.Header.Set("Content-Type", "application/json")

	if p != nil {
		req = withAdminPrincipal(req, p)
	}

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	var out map[string]any

	_ = json.Unmarshal(rw.Body.Bytes(), &out)

	return rw.Code, out
}

func oidcBody(slug, issuer, icon string) map[string]any {
	b := map[string]any{
		"slug": slug, "kind": "oidc", "display_name": slug,
		"oidc_config": map[string]any{"issuer": issuer, "client_id": "c", "client_secret": "s"},
	}
	if icon != "" {
		b["icon"] = icon
	}

	return b
}

func TestAdminSSO_IconUnknownKeyIsRejectedOnCreate(t *testing.T) {
	router, principal := setupSSORouter(t, newMockDatabase())

	code, body := ssoCall(t, router, principal, "POST", "/api/v1/admin/sso-providers",
		oidcBody("bad", "https://203.0.113.10", "microsoft"))
	if code != http.StatusBadRequest || body["code"] != "validation_failed" {
		t.Fatalf("got %d %v, want 400 validation_failed", code, body)
	}
}

func TestAdminSSO_IconAutomaticResolvesFromIssuerAndCallbackURL(t *testing.T) {
	router, principal := setupSSORouter(t, newMockDatabase())

	code, body := ssoCall(t, router, principal, "POST", "/api/v1/admin/sso-providers",
		oidcBody("g", "https://accounts.google.com", ""))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}

	if body["icon"] != "" || body["resolved_icon"] != "google" {
		t.Errorf("icon=%v resolved_icon=%v, want empty and google", body["icon"], body["resolved_icon"])
	}

	if body["callback_url"] != "http://localhost:8080/api/v1/auth/sso/oidc/g/callback" {
		t.Errorf("callback_url = %v", body["callback_url"])
	}
}

func TestAdminSSO_IconExplicitShowsOnPublicList(t *testing.T) {
	router, principal := setupSSORouter(t, newMockDatabase())

	for slug, spec := range map[string][2]string{
		"kc": {"https://203.0.113.10", "keycloak"},
		"g":  {"https://accounts.google.com", ""},
	} {
		if code, body := ssoCall(t, router, principal, "POST", "/api/v1/admin/sso-providers",
			oidcBody(slug, spec[0], spec[1])); code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", slug, code, body)
		}
	}

	code, pub := ssoCall(t, router, nil, "GET", "/api/v1/auth/sso/providers", nil)
	if code != http.StatusOK {
		t.Fatalf("public list: %d", code)
	}

	icons := map[string]any{}

	for _, p := range pub["providers"].([]any) {
		m := p.(map[string]any)
		icons[m["slug"].(string)] = m["icon"]
	}

	if icons["kc"] != "keycloak" || icons["g"] != "google" {
		t.Errorf("public icons = %v", icons)
	}
}

func TestAdminSSO_IconPatchSetsClearsAndRejects(t *testing.T) {
	router, principal := setupSSORouter(t, newMockDatabase())

	if code, body := ssoCall(t, router, principal, "POST", "/api/v1/admin/sso-providers",
		oidcBody("g", "https://accounts.google.com", "")); code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, body)
	}

	const path = "/api/v1/admin/sso-providers/g"

	code, body := ssoCall(t, router, principal, "PATCH", path, map[string]any{"icon": "okta"})
	if code != http.StatusOK || body["icon"] != "okta" || body["resolved_icon"] != "okta" {
		t.Fatalf("set: %d %v", code, body)
	}

	code, body = ssoCall(t, router, principal, "PATCH", path, map[string]any{"icon": ""})
	if code != http.StatusOK || body["icon"] != "" || body["resolved_icon"] != "google" {
		t.Fatalf("clear: %d %v", code, body)
	}

	for _, bad := range []any{"dex", 3} {
		if code, _ = ssoCall(t, router, principal, "PATCH", path, map[string]any{"icon": bad}); code != http.StatusBadRequest {
			t.Fatalf("icon %v: %d, want 400", bad, code)
		}
	}

	code, body = ssoCall(t, router, principal, "GET", path, nil)
	if code != http.StatusOK || body["resolved_icon"] != "google" {
		t.Fatalf("a rejected patch changed the provider: %d %v", code, body)
	}
}
