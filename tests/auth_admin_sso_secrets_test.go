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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
)

func ssoAdminDo(t *testing.T, router chi.Router, p *auth.Principal, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var rdr *bytes.Reader

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}

	req := httptest.NewRequestWithContext(context.Background(), method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req = withAdminPrincipal(req, p)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func useSecretKey(t *testing.T) {
	t.Helper()

	if err := crypto.Init("admin-sso-test-key-0123456789"); err != nil {
		t.Fatalf("crypto.Init: %v", err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })
}

func seedAdminOIDC(t *testing.T, db *MockDatabase, slug, secret string, enabled bool) *models.SSOProvider {
	t.Helper()

	cfg, _ := json.Marshal(models.OIDCConfig{Issuer: "https://203.0.113.10", ClientID: "cid", ClientSecret: secret})
	now := time.Now()
	p := &models.SSOProvider{
		Slug: slug, Kind: "oidc", DisplayName: slug, Enabled: enabled, OIDCConfig: cfg,
		GroupMapping: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}

	if err := db.CreateSSOProvider(context.Background(), p); err != nil {
		t.Fatalf("CreateSSOProvider: %v", err)
	}

	return p
}

func storedOIDCConfig(t *testing.T, db *MockDatabase, slug string) models.OIDCConfig {
	t.Helper()

	p, _ := db.GetSSOProviderBySlug(context.Background(), slug)
	if p == nil {
		t.Fatalf("provider %q not stored", slug)
	}

	var cfg models.OIDCConfig

	if err := json.Unmarshal(p.OIDCConfig, &cfg); err != nil {
		t.Fatalf("unmarshal stored config: %v", err)
	}

	return cfg
}

func TestAdminSSO_Create_EncryptsSecretAndNeverReturnsIt(t *testing.T) {
	useSecretKey(t)

	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	rw := ssoAdminDo(t, router, principal, "POST", "/api/v1/admin/sso-providers", map[string]any{
		"slug": "enc", "kind": "oidc", "display_name": "Enc",
		"oidc_config": map[string]any{"issuer": "https://203.0.113.10", "client_id": "cid", "client_secret": "plain-secret"},
	})
	if rw.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", rw.Code, rw.Body.String())
	}

	if bytes.Contains(rw.Body.Bytes(), []byte("plain-secret")) {
		t.Error("response must not contain the client secret")
	}

	var resp map[string]any

	_ = json.Unmarshal(rw.Body.Bytes(), &resp)

	cfg, _ := resp["oidc_config"].(map[string]any)
	if _, has := cfg["client_secret"]; has {
		t.Error("response oidc_config must not contain client_secret")
	}

	if cfg["has_secret"] != true {
		t.Errorf("expected has_secret=true, got %v", cfg["has_secret"])
	}

	stored := storedOIDCConfig(t, db, "enc")
	if !crypto.IsEncrypted(stored.ClientSecret) {
		t.Errorf("client_secret must be stored encrypted, got %q", stored.ClientSecret)
	}

	if dec, err := crypto.Decrypt(stored.ClientSecret); err != nil || dec != "plain-secret" {
		t.Errorf("stored secret must decrypt to the original, got %q (%v)", dec, err)
	}
}

func TestAdminSSO_Create_RejectsUnsafeOrMalformedConfig(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	cases := map[string]map[string]any{
		"loopback issuer":        {"issuer": "http://127.0.0.1:9", "client_id": "c", "client_secret": "s"},
		"metadata service":       {"issuer": "http://169.254.169.254", "client_id": "c", "client_secret": "s"},
		"missing client id":      {"issuer": "https://203.0.113.10", "client_secret": "s"},
		"missing secret":         {"issuer": "https://203.0.113.10", "client_id": "c"},
		"pasted ciphertext":      {"issuer": "https://203.0.113.10", "client_id": "c", "client_secret": "enc:v1:AAAA"},
		"non-http issuer scheme": {"issuer": "file:///etc/passwd", "client_id": "c", "client_secret": "s"},
	}

	for name, cfg := range cases {
		rw := ssoAdminDo(t, router, principal, "POST", "/api/v1/admin/sso-providers", map[string]any{
			"slug": "bad", "kind": "oidc", "display_name": "Bad", "oidc_config": cfg,
		})
		if rw.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d (%s)", name, rw.Code, rw.Body.String())
		}
	}

	if p, _ := db.GetSSOProviderBySlug(context.Background(), "bad"); p != nil {
		t.Error("rejected configs must not be stored")
	}
}

func TestAdminSSO_Create_RejectsInvalidSAMLConfig(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	for name, cfg := range map[string]map[string]any{
		"garbage xml":      {"idp_metadata_xml": "not xml"},
		"loopback url":     {"idp_metadata_url": "http://127.0.0.1:9/metadata"},
		"nothing provided": {},
	} {
		rw := ssoAdminDo(t, router, principal, "POST", "/api/v1/admin/sso-providers", map[string]any{
			"slug": "badsaml", "kind": "saml", "display_name": "Bad", "saml_config": cfg,
		})
		if rw.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d (%s)", name, rw.Code, rw.Body.String())
		}
	}
}

func TestAdminSSO_Patch_PartialOIDCConfigKeepsSecretAndOtherFields(t *testing.T) {
	useSecretKey(t)

	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	enc, _ := crypto.Encrypt("keep-me")
	seedAdminOIDC(t, db, "merge", enc, true)

	rw := ssoAdminDo(t, router, principal, "PATCH", "/api/v1/admin/sso-providers/merge", map[string]any{
		"oidc_config": map[string]any{"client_id": "new-client"},
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rw.Code, rw.Body.String())
	}

	if bytes.Contains(rw.Body.Bytes(), []byte("keep-me")) || bytes.Contains(rw.Body.Bytes(), []byte(enc)) {
		t.Error("PATCH response must not expose the secret")
	}

	stored := storedOIDCConfig(t, db, "merge")
	if stored.ClientID != "new-client" {
		t.Errorf("client_id not updated: %q", stored.ClientID)
	}

	if stored.Issuer != "https://203.0.113.10" {
		t.Errorf("absent issuer must be kept, got %q", stored.Issuer)
	}

	if dec, _ := crypto.Decrypt(stored.ClientSecret); dec != "keep-me" {
		t.Errorf("absent client_secret must keep the stored secret, got %q", dec)
	}
}

func TestAdminSSO_Patch_NewSecretIsEncrypted(t *testing.T) {
	useSecretKey(t)

	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	enc, _ := crypto.Encrypt("old")
	seedAdminOIDC(t, db, "rotate", enc, true)

	rw := ssoAdminDo(t, router, principal, "PATCH", "/api/v1/admin/sso-providers/rotate", map[string]any{
		"oidc_config": map[string]any{"client_secret": "brand-new"},
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rw.Code, rw.Body.String())
	}

	stored := storedOIDCConfig(t, db, "rotate")
	if !crypto.IsEncrypted(stored.ClientSecret) {
		t.Fatalf("new secret must be stored encrypted, got %q", stored.ClientSecret)
	}

	if dec, _ := crypto.Decrypt(stored.ClientSecret); dec != "brand-new" {
		t.Errorf("expected rotated secret, got %q", dec)
	}
}

func TestAdminSSO_Patch_RejectsUnsafeIssuerAndKindMismatch(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	seedAdminOIDC(t, db, "guard", "s", true)

	rw := ssoAdminDo(t, router, principal, "PATCH", "/api/v1/admin/sso-providers/guard", map[string]any{
		"oidc_config": map[string]any{"issuer": "http://169.254.169.254"},
	})
	if rw.Code != http.StatusBadRequest {
		t.Errorf("SSRF issuer on PATCH: expected 400, got %d (%s)", rw.Code, rw.Body.String())
	}

	rw = ssoAdminDo(t, router, principal, "PATCH", "/api/v1/admin/sso-providers/guard", map[string]any{
		"saml_config": map[string]any{"idp_metadata_url": "https://203.0.113.10/metadata"},
	})
	if rw.Code != http.StatusBadRequest {
		t.Errorf("saml_config on an oidc provider: expected 400, got %d (%s)", rw.Code, rw.Body.String())
	}

	rw = ssoAdminDo(t, router, principal, "PATCH", "/api/v1/admin/sso-providers/guard", map[string]any{
		"enabled": "yes",
	})
	if rw.Code != http.StatusBadRequest {
		t.Errorf("non-boolean enabled: expected 400, got %d (%s)", rw.Code, rw.Body.String())
	}
}

func TestAdminSSO_Test_DisabledProviderCanStillBeTested(t *testing.T) {
	db := newMockDatabase()
	router, principal := setupAdminSSORouter(t, db)

	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	cfg, _ := json.Marshal(models.OIDCConfig{Issuer: closedURL, ClientID: "cid", ClientSecret: "s"})
	now := time.Now()
	_ = db.CreateSSOProvider(context.Background(), &models.SSOProvider{
		Slug: "off", Kind: "oidc", DisplayName: "Off", Enabled: false, OIDCConfig: cfg,
		GroupMapping: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	})

	rw := ssoAdminDo(t, router, principal, "POST", "/api/v1/admin/sso-providers/off/test", map[string]any{})
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any

	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if resp["error"] != "oidc_discovery_failed" {
		t.Errorf("a disabled provider must reach discovery (admin test), got %v", resp)
	}
}
