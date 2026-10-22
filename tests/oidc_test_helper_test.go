// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
)

type mockOIDCServer struct {
	Server     *httptest.Server
	privateKey *rsa.PrivateKey
	kid        string
	tokens     map[string]string
}

func newMockOIDCServer(t *testing.T) *mockOIDCServer {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}

	m := &mockOIDCServer{
		privateKey: privKey,
		kid:        "test-key-1",
		tokens:     make(map[string]string),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := m.Server.URL
		doc := map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/auth",
			"token_endpoint":                        base + "/token",
			"jwks_uri":                              base + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwks := jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{
				{
					Key:       &privKey.PublicKey,
					KeyID:     m.kid,
					Algorithm: string(jose.RS256),
					Use:       "sig",
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}

		code := r.FormValue("code")

		body, ok := m.tokens[code]
		if !ok {
			http.Error(w, `{"error":"unknown_code"}`, http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	// The mock IdP listens on loopback, which the SSRF dial guard blocks.
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	m.Server = httptest.NewServer(mux)
	t.Cleanup(m.Server.Close)

	return m
}

func (m *mockOIDCServer) signIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()

	base := m.Server.URL
	if _, ok := claims["iss"]; !ok {
		claims["iss"] = base
	}

	if _, ok := claims["aud"]; !ok {
		claims["aud"] = []string{"test-client"}
	}

	if _, ok := claims["sub"]; !ok {
		claims["sub"] = "test-subject"
	}

	now := time.Now()
	if _, ok := claims["iat"]; !ok {
		claims["iat"] = now.Unix()
	}

	if _, ok := claims["exp"]; !ok {
		claims["exp"] = now.Add(5 * time.Minute).Unix()
	}

	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: m.privateKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", m.kid),
	)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}

	raw, err := josejwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	return raw
}

func (m *mockOIDCServer) addCode(t *testing.T, code string, idToken string) {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"access_token": "fake-access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idToken,
	})
	if err != nil {
		t.Fatalf("marshal token response: %v", err)
	}

	m.tokens[code] = string(body)
}
