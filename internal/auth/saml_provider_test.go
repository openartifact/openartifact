// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/crewjam/saml"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database/models"
)

func swapSAMLMetadataCache(t *testing.T, c *idpMetadataCache) {
	t.Helper()

	prev := samlMetadata
	samlMetadata = c

	t.Cleanup(func() { samlMetadata = prev })
}

func samlProviderRow(t *testing.T, slug string, cfg models.SAMLConfig, updated time.Time) *models.SSOProvider {
	t.Helper()

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { samlCache.Delete(slug) })

	return &models.SSOProvider{ID: slug, Slug: slug, Kind: "saml", Enabled: true, SAMLConfig: raw, UpdatedAt: updated}
}

// An IdP that rolls its signing certificate is followed on its own: once the
// refresh interval has passed the next login rebuilds the provider with the
// new certificate, with no PATCH of the provider in between.
func TestLoadSAMLProviderRow_FollowsRotatedMetadataURL(t *testing.T) {
	cache, clock, stub := newTestMetadataCache()
	swapSAMLMetadataCache(t, cache)
	newReplica(t)

	stub.set(idpDoc("", newTestCert(t, "old")), nil)

	cfg := models.SAMLConfig{IDPMetadataURL: testMetadataURL, IDPMetadataRefreshSeconds: 300}
	row := samlProviderRow(t, "rotating", cfg, clock.now())
	store := &keySettingsStore{m: map[string]string{}}
	ctx := context.Background()

	load := func() *SAMLProvider {
		t.Helper()

		p, err := LoadSAMLProviderRow(ctx, store, row, "https://oa.example.com", &config.Config{}, nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}

		return p
	}

	signingCN := func(p *SAMLProvider) string {
		t.Helper()

		v, ok := p.SP.ServiceProvider.SignatureVerifier.(*idpCertVerifier)
		if !ok || len(v.certs) == 0 {
			t.Fatal("provider must verify with the IdP's metadata certificates")
		}

		return v.certs[0].Subject.CommonName
	}

	first := load()
	if signingCN(first) != "old" {
		t.Fatalf("expected the old certificate, got %q", signingCN(first))
	}

	stub.set(idpDoc("", newTestCert(t, "new")), nil)
	clock.advance(299 * time.Second)

	if load() != first {
		t.Error("within the interval the cached provider must be reused")
	}

	clock.advance(2 * time.Second)

	second := load()
	if second == first || signingCN(second) != "new" {
		t.Fatalf("after the interval the rotated certificate must be in use, got %q", signingCN(second))
	}

	// The IdP going away does not take logins down: the last copy is kept.
	stub.set("", errIDPMetadataStatus)
	clock.advance(time.Hour)

	if third := load(); signingCN(third) != "new" {
		t.Errorf("stale-if-error must keep the last good certificate, got %q", signingCN(third))
	}
}

func TestLoadSAMLProviderRow_ServiceProviderSetup(t *testing.T) {
	cache, clock, stub := newTestMetadataCache()
	swapSAMLMetadataCache(t, cache)
	newReplica(t)

	stub.set(idpDoc("", newTestCert(t, "idp")), nil)

	store := &keySettingsStore{m: map[string]string{}}
	ctx := context.Background()
	base := models.SAMLConfig{IDPMetadataURL: testMetadataURL}

	strictOnly, err := LoadSAMLProviderRow(ctx, store, samlProviderRow(t, "strict", base, clock.now()), "https://oa.example.com", &config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if strictOnly.idpInitiatedSP != nil {
		t.Error("no IdP-initiated service provider unless allow_idp_initiated is set")
	}

	if strictOnly.SP.ServiceProvider.AllowIDPInitiated {
		t.Error("the strict service provider must never allow IdP-initiated responses")
	}

	if got := strictOnly.SP.ServiceProvider.AuthnNameIDFormat; got != saml.UnspecifiedNameIDFormat {
		t.Errorf("AuthnRequest must not ask for a specific NameID format (library default is transient), got %q", got)
	}

	if strictOnly.EntityID != "https://oa.example.com/api/v1/auth/sso/saml/strict/metadata" ||
		strictOnly.AcsURL != "https://oa.example.com/api/v1/auth/sso/saml/strict/acs" {
		t.Errorf("unexpected SP URLs: %q %q", strictOnly.EntityID, strictOnly.AcsURL)
	}

	enabled := base
	enabled.AllowIDPInitiated = true

	both, err := LoadSAMLProviderRow(ctx, store, samlProviderRow(t, "both", enabled, clock.now()), "https://oa.example.com", &config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if both.idpInitiatedSP == nil || !both.idpInitiatedSP.AllowIDPInitiated {
		t.Fatal("allow_idp_initiated must create the IdP-initiated service provider")
	}

	if both.SP.ServiceProvider.AllowIDPInitiated {
		t.Error("enabling IdP-initiated must not loosen the strict service provider")
	}

	// The IdP-initiated provider insists on an empty InResponseTo.
	hook := both.idpInitiatedSP.ValidateRequestID
	if hook == nil {
		t.Fatal("IdP-initiated service provider needs the InResponseTo hook")
	}

	if err := hook(saml.Response{}, nil); err != nil {
		t.Errorf("an unsolicited response must pass the hook: %v", err)
	}

	if err := hook(saml.Response{InResponseTo: "id-1"}, nil); err == nil {
		t.Error("a response naming a request must be rejected by the IdP-initiated hook")
	}

	// Flipping the flag on the same row rebuilds the cached provider.
	row := samlProviderRow(t, "flip", base, clock.now())

	off, err := LoadSAMLProviderRow(ctx, store, row, "https://oa.example.com", &config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(enabled)
	row.SAMLConfig = raw

	on, err := LoadSAMLProviderRow(ctx, store, row, "https://oa.example.com", &config.Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if off.idpInitiatedSP != nil || on.idpInitiatedSP == nil {
		t.Error("toggling allow_idp_initiated must take effect without waiting for a cache expiry")
	}
}
