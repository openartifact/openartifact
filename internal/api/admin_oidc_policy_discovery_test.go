// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"maps"
	"net/http"
	"testing"

	"github.com/go-jose/go-jose/v4"

	"github.com/openartifact/openartifact/internal/database/models"
)

// publicJWKS is a JWKS holding one fresh public signing key; the private half is
// returned as a JWKS too, to show it is refused.
func publicJWKS(t *testing.T) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	encode := func(k any) string {
		raw, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: k, KeyID: "k1", Algorithm: "ES256", Use: "sig"}}})
		if err != nil {
			t.Fatalf("marshal jwks: %v", err)
		}

		return string(raw)
	}

	return encode(&key.PublicKey), encode(key)
}

func discoveryPolicyBody(t *testing.T, issuer string, extra map[string]any) string {
	t.Helper()

	body := map[string]any{
		"name": "tekton", "issuer": issuer, "audience": "https://oa.example", "service_account_id": saID,
		"conditions": []map[string]any{{"claim": "kubernetes.io.namespace", "patterns": []string{"team-a"}}},
	}
	maps.Copy(body, extra)

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	return string(raw)
}

func newDiscoveryRig(t *testing.T) *saRig {
	t.Helper()

	rig := newSARig(t)
	rig.db.accounts[saID] = &models.User{ID: saID, Username: "ci-publisher", Kind: models.UserKindServiceAccount}

	return rig
}

func TestOIDCPolicyDiscoveryCreate(t *testing.T) {
	public, private := publicJWKS(t)

	tests := []struct {
		name       string
		issuer     string
		extra      map[string]any
		wantStatus int
		wantMode   string
		wantJWKS   bool
		wantPriv   bool
	}{
		{"no discovery is anonymous", "https://8.8.8.8", nil, http.StatusCreated, models.OIDCDiscoveryAnonymous, false, false},
		{
			"serviceaccount on a public issuer", "https://8.8.8.8",
			map[string]any{"discovery": map[string]any{"mode": "serviceaccount"}},
			http.StatusCreated, models.OIDCDiscoveryServiceAccount, false, false,
		},
		{
			"serviceaccount on a cluster-internal issuer with allow_private_issuer", "https://10.96.0.1",
			map[string]any{"discovery": map[string]any{"mode": "serviceaccount"}, "allow_private_issuer": true},
			http.StatusCreated, models.OIDCDiscoveryServiceAccount, false, true,
		},
		{
			"a private issuer without allow_private_issuer is refused", "https://10.96.0.1",
			map[string]any{"discovery": map[string]any{"mode": "serviceaccount"}},
			http.StatusBadRequest, "", false, false,
		},
		{
			"allow_private_issuer does not reach cloud metadata", "https://169.254.169.254",
			map[string]any{"discovery": map[string]any{"mode": "serviceaccount"}, "allow_private_issuer": true},
			http.StatusBadRequest, "", false, false,
		},
		{
			"static discovery with a public key set", "https://8.8.8.8",
			map[string]any{"discovery": map[string]any{"mode": "static", "jwks": json.RawMessage(public)}},
			http.StatusCreated, models.OIDCDiscoveryStatic, true, false,
		},
		{
			"static discovery contacts nobody, so a private issuer name is fine", "https://10.96.0.1",
			map[string]any{"discovery": map[string]any{"mode": "static", "jwks": json.RawMessage(public)}},
			http.StatusCreated, models.OIDCDiscoveryStatic, true, false,
		},
		{
			"static discovery refuses private keys", "https://8.8.8.8",
			map[string]any{"discovery": map[string]any{"mode": "static", "jwks": json.RawMessage(private)}},
			http.StatusBadRequest, "", false, false,
		},
		{
			"static discovery needs the key set", "https://8.8.8.8",
			map[string]any{"discovery": map[string]any{"mode": "static"}},
			http.StatusBadRequest, "", false, false,
		},
		{
			"a key set belongs to the static mode", "https://8.8.8.8",
			map[string]any{"discovery": map[string]any{"mode": "anonymous", "jwks": json.RawMessage(public)}},
			http.StatusBadRequest, "", false, false,
		},
		{
			"unknown mode", "https://8.8.8.8",
			map[string]any{"discovery": map[string]any{"mode": "magic"}},
			http.StatusBadRequest, "", false, false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newDiscoveryRig(t)

			rw := rig.do(true, http.MethodPost, "/api/v1/oidc-trust-policies", discoveryPolicyBody(t, tt.issuer, tt.extra))
			if rw.Code != tt.wantStatus {
				t.Fatalf("create = %d, want %d: %s", rw.Code, tt.wantStatus, rw.Body.String())
			}

			if tt.wantStatus != http.StatusCreated {
				if len(rig.db.policies) != 0 {
					t.Fatal("a refused request created a policy")
				}

				return
			}

			var got oidcPolicyResponse
			if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}

			if got.Discovery.Mode != tt.wantMode || (len(got.Discovery.JWKS) > 0) != tt.wantJWKS || got.AllowPrivateIssuer != tt.wantPriv {
				t.Fatalf("response discovery = %q jwks %d private %v, want %q jwks %v private %v",
					got.Discovery.Mode, len(got.Discovery.JWKS), got.AllowPrivateIssuer, tt.wantMode, tt.wantJWKS, tt.wantPriv)
			}

			stored := rig.db.policies[got.ID]
			if stored.Discovery.Mode != tt.wantMode || stored.AllowPrivateIssuer != tt.wantPriv {
				t.Fatalf("stored discovery = %+v private %v", stored.Discovery, stored.AllowPrivateIssuer)
			}

			// The key set is public: GET returns it as written.
			rw = rig.do(true, http.MethodGet, "/api/v1/oidc-trust-policies/"+got.ID, "")
			if rw.Code != http.StatusOK {
				t.Fatalf("get = %d", rw.Code)
			}

			var read oidcPolicyResponse
			if err := json.Unmarshal(rw.Body.Bytes(), &read); err != nil {
				t.Fatal(err)
			}

			if string(read.Discovery.JWKS) != string(got.Discovery.JWKS) {
				t.Fatalf("get jwks = %s, want %s", read.Discovery.JWKS, got.Discovery.JWKS)
			}
		})
	}
}

func TestOIDCPolicyDiscoveryPatch(t *testing.T) {
	public, _ := publicJWKS(t)

	// A policy that predates the discovery modes, on an issuer the outbound
	// check would refuse today.
	legacy := func() *models.OIDCTrustPolicy {
		return &models.OIDCTrustPolicy{
			ID: policyID, Name: "legacy", Issuer: "https://10.96.0.1", Audience: "a", ServiceAccountID: saID,
			TokenTTLSeconds: 900, Enabled: true,
			Conditions: []models.OIDCCondition{{Claim: "kubernetes.io.namespace", Patterns: []string{"team-a"}}},
		}
	}

	tests := []struct {
		name       string
		start      func(p *models.OIDCTrustPolicy)
		body       string
		wantStatus int
		check      func(t *testing.T, p *models.OIDCTrustPolicy)
	}{
		{
			name: "an unrelated edit does not re-check the issuer", start: func(*models.OIDCTrustPolicy) {},
			body: `{"enabled":false}`, wantStatus: http.StatusOK,
			check: func(t *testing.T, p *models.OIDCTrustPolicy) {
				t.Helper()

				if p.Discovery.EffectiveMode() != models.OIDCDiscoveryAnonymous || p.AllowPrivateIssuer {
					t.Fatalf("discovery changed: %+v", p)
				}
			},
		},
		{
			name: "allow_private_issuer admits the private issuer", start: func(*models.OIDCTrustPolicy) {},
			body: `{"allow_private_issuer":true,"discovery":{"mode":"serviceaccount"}}`, wantStatus: http.StatusOK,
			check: func(t *testing.T, p *models.OIDCTrustPolicy) {
				t.Helper()

				if p.Discovery.Mode != models.OIDCDiscoveryServiceAccount || !p.AllowPrivateIssuer {
					t.Fatalf("discovery = %+v private %v", p.Discovery, p.AllowPrivateIssuer)
				}
			},
		},
		{
			name: "a new mode re-checks the issuer", start: func(*models.OIDCTrustPolicy) {},
			body: `{"discovery":{"mode":"serviceaccount"}}`, wantStatus: http.StatusBadRequest,
		},
		{
			name: "withdrawing allow_private_issuer re-checks the issuer",
			start: func(p *models.OIDCTrustPolicy) {
				p.AllowPrivateIssuer = true
				p.Discovery.Mode = models.OIDCDiscoveryServiceAccount
			},
			body: `{"allow_private_issuer":false}`, wantStatus: http.StatusBadRequest,
		},
		{
			name: "static discovery replaces the fetching", start: func(*models.OIDCTrustPolicy) {},
			body: `{"discovery":{"mode":"static","jwks":` + public + `}}`, wantStatus: http.StatusOK,
			check: func(t *testing.T, p *models.OIDCTrustPolicy) {
				t.Helper()

				if p.Discovery.Mode != models.OIDCDiscoveryStatic || len(p.Discovery.JWKS) == 0 {
					t.Fatalf("discovery = %+v", p.Discovery)
				}
			},
		},
		{
			name: "leaving the static mode drops the key set",
			start: func(p *models.OIDCTrustPolicy) {
				p.Issuer = "https://8.8.8.8"
				p.Discovery = models.OIDCDiscovery{Mode: models.OIDCDiscoveryStatic, JWKS: json.RawMessage(public)}
			},
			body: `{"discovery":{"mode":"anonymous"}}`, wantStatus: http.StatusOK,
			check: func(t *testing.T, p *models.OIDCTrustPolicy) {
				t.Helper()

				if p.Discovery.Mode != models.OIDCDiscoveryAnonymous || len(p.Discovery.JWKS) != 0 {
					t.Fatalf("discovery = %+v", p.Discovery)
				}
			},
		},
		{
			name:       "private keys are refused",
			start:      func(*models.OIDCTrustPolicy) {},
			body:       `{"discovery":{"mode":"static","jwks":{"keys":[{"kty":"oct","k":"c2VjcmV0c2VjcmV0c2VjcmV0"}]}}}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := newDiscoveryRig(t)

			p := legacy()
			tt.start(p)
			rig.db.policies[policyID] = p

			rw := rig.do(true, http.MethodPatch, "/api/v1/oidc-trust-policies/"+policyID, tt.body)
			if rw.Code != tt.wantStatus {
				t.Fatalf("patch = %d, want %d: %s", rw.Code, tt.wantStatus, rw.Body.String())
			}

			if tt.check != nil {
				tt.check(t, rig.db.policies[policyID])
			}
		})
	}
}

func TestOIDCPolicyDiscoveryNeedsAdmin(t *testing.T) {
	rig := newDiscoveryRig(t)

	body := discoveryPolicyBody(t, "https://10.96.0.1", map[string]any{
		"discovery": map[string]any{"mode": "serviceaccount"}, "allow_private_issuer": true,
	})

	if rw := rig.do(false, http.MethodPost, "/api/v1/oidc-trust-policies", body); rw.Code == http.StatusCreated {
		t.Fatalf("a non-admin created a private-issuer policy: %d", rw.Code)
	}

	if len(rig.db.policies) != 0 {
		t.Fatal("a non-admin request created a policy")
	}
}
