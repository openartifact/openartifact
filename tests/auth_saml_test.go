// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database/models"
)

const testIDPMetadataXML = `<?xml version="1.0"?>
<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata"
    entityID="https://idp.example.com/saml">
  <IDPSSODescriptor WantAuthnRequestsSigned="false"
      protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
    <SingleSignOnService
        Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect"
        Location="https://idp.example.com/saml/sso"/>
  </IDPSSODescriptor>
</EntityDescriptor>`

const testIDPMetadataXMLInvalid = `not valid xml at all <<<>>>`

func seedSAMLProvider(t *testing.T, db *MockDatabase, slug, metadataXML string) *models.SSOProvider {
	t.Helper()

	cfg, err := json.Marshal(models.SAMLConfig{
		IDPMetadataXML: metadataXML,
	})
	if err != nil {
		t.Fatalf("marshal saml config: %v", err)
	}

	now := time.Now()

	p := &models.SSOProvider{
		ID:                 "saml-provider-" + slug,
		Slug:               slug,
		Kind:               "saml",
		DisplayName:        "Test SAML " + slug,
		Enabled:            true,
		SAMLConfig:         cfg,
		GroupMapping:       make(map[string]string),
		AutoProvisionUsers: true,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := db.CreateSSOProvider(context.Background(), p); err != nil {
		t.Fatalf("CreateSSOProvider: %v", err)
	}

	return p
}

func setupSAMLRouter(t *testing.T, mock *MockDatabase, baseURL string) chi.Router {
	t.Helper()
	log := newHandlerLogger(t)
	db := newSAMLReplayDB(mock)
	eval := auth.NewEvaluator(db, log, false)
	cfg := &config.Config{BaseURL: baseURL}
	h := api.NewSAMLHandler(db, log, eval, []byte("test-hmac-key"), baseURL, cfg)
	router := chi.NewRouter()
	h.RegisterPublic(router)

	return router
}

func TestSAML_LoginRedirectsToIdP(t *testing.T) {
	db := newMockDatabase()
	seedSAMLProvider(t, db, "test", testIDPMetadataXML)

	router := setupSAMLRouter(t, db, "http://localhost:8080")

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/saml/test/login", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	loc := rw.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://idp.example.com/saml/sso") {
		t.Errorf("expected redirect to IdP SSO URL, got %q", loc)
	}

	if !strings.Contains(loc, "SAMLRequest=") {
		t.Errorf("expected SAMLRequest param in Location, got %q", loc)
	}
}

func TestSAML_Login_ProviderNotFound(t *testing.T) {
	db := newMockDatabase()
	router := setupSAMLRouter(t, db, "http://localhost:8080")

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/saml/nosuchslug/login", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	var resp map[string]any

	_ = json.NewDecoder(rw.Body).Decode(&resp)
	if resp["code"] != "provider_not_found" {
		t.Errorf("expected code=provider_not_found, got %v", resp["code"])
	}
}

func TestSAML_Login_InvalidIDPMetadata(t *testing.T) {
	db := newMockDatabase()
	seedSAMLProvider(t, db, "bad", testIDPMetadataXMLInvalid)

	router := setupSAMLRouter(t, db, "http://localhost:8080")

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/saml/bad/login", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code < 400 {
		t.Fatalf("expected 4xx/5xx for invalid metadata, got %d", rw.Code)
	}
}

func TestSAML_MetadataXML_ServesSPMetadata(t *testing.T) {
	db := newMockDatabase()
	seedSAMLProvider(t, db, "test", testIDPMetadataXML)

	router := setupSAMLRouter(t, db, "http://localhost:8080")

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/saml/test/metadata", nil)
	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rw.Code, rw.Body.String())
	}

	ct := rw.Header().Get("Content-Type")
	if !strings.Contains(ct, "samlmetadata+xml") {
		t.Errorf("expected Content-Type application/samlmetadata+xml, got %q", ct)
	}

	body := rw.Body.String()
	if !strings.Contains(body, "EntityDescriptor") {
		t.Errorf("expected EntityDescriptor in SP metadata body")
	}

	if !strings.Contains(body, "http://localhost:8080/api/v1/auth/sso/saml/test/metadata") {
		t.Errorf("expected SP entity ID in metadata body, got:\n%s", body)
	}
}
