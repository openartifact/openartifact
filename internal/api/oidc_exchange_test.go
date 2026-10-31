// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
)

const exchangeAudience = "https://artifacts.example"

// exchangeFakeDB serves trust policies and records the tokens and audit rows
// the exchange writes.
type exchangeFakeDB struct {
	mu       sync.Mutex
	policies []*models.OIDCTrustPolicy
	tokens   []*models.Token
	audits   []*models.AuditLog
}

func (d *exchangeFakeDB) ListEnabledOIDCPoliciesByIssuer(_ context.Context, issuer string) ([]*models.OIDCTrustPolicy, error) {
	var out []*models.OIDCTrustPolicy

	for _, p := range d.policies {
		if p.Issuer == issuer && p.Enabled {
			out = append(out, p)
		}
	}

	return out, nil
}

func (d *exchangeFakeDB) CreateToken(_ context.Context, token *models.Token) (*models.Token, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	saved := *token
	saved.ID = "00000000-0000-4000-8000-00000000abcd"
	d.tokens = append(d.tokens, &saved)

	return &saved, nil
}

func (d *exchangeFakeDB) InsertAuditLog(_ context.Context, event *models.AuditLog) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.audits = append(d.audits, event)

	return nil
}

// exchangeIssuer is a CI system with one signing key.
type exchangeIssuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey
}

func newExchangeIssuer(t *testing.T) *exchangeIssuer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	iss := &exchangeIssuer{key: key}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": iss.srv.URL, "jwks_uri": iss.srv.URL + "/jwks",
			"response_types_supported":              []string{"id_token"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: string(jose.RS256), Use: "sig"},
		}})
	})

	// The issuer listens on loopback, which the SSRF dial guard blocks.
	t.Setenv(netutil.EnvAllowPrivate, "true")

	iss.srv = httptest.NewServer(mux)
	t.Cleanup(iss.srv.Close)

	return iss
}

func (i *exchangeIssuer) sign(t *testing.T, key *rsa.PrivateKey, mutate func(map[string]any)) string {
	t.Helper()

	now := time.Now()
	claims := map[string]any{
		"iss": i.srv.URL, "sub": "repo:acme/widgets:ref:refs/heads/main", "aud": exchangeAudience,
		"repository": "acme/widgets", "ref": "refs/heads/main",
		"iat": now.Add(-time.Minute).Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	}
	if mutate != nil {
		mutate(claims)
	}

	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}

	raw, err := josejwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}

	return raw
}

func (i *exchangeIssuer) token(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()

	return i.sign(t, i.key, mutate)
}

func exchangePolicy(issuer string) *models.OIDCTrustPolicy {
	return &models.OIDCTrustPolicy{
		ID: "11111111-1111-4111-8111-111111111111", Name: "publish", Issuer: issuer, Audience: exchangeAudience,
		Conditions:       []models.OIDCCondition{{Claim: "repository", Patterns: []string{"acme/*"}}},
		ServiceAccountID: "22222222-2222-4222-8222-222222222222", ServiceAccountName: "ci-publisher",
		TokenTTLSeconds: 600, Enabled: true,
	}
}

type exchangeRig struct {
	iss *exchangeIssuer
	db  *exchangeFakeDB
	h   *OIDCExchangeHandler
}

func newExchangeRig(t *testing.T, mutate func(*models.OIDCTrustPolicy)) *exchangeRig {
	t.Helper()

	iss := newExchangeIssuer(t)
	policy := exchangePolicy(iss.srv.URL)

	if mutate != nil {
		mutate(policy)
	}

	db := &exchangeFakeDB{policies: []*models.OIDCTrustPolicy{policy}}

	return &exchangeRig{iss: iss, db: db, h: NewOIDCExchangeHandler(db, newFixTestLogger(t), nil)}
}

func (r *exchangeRig) post(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/auth/oidc/exchange", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.7:4711"

	rw := httptest.NewRecorder()
	r.h.handleExchange(rw, req)

	return rw
}

func (r *exchangeRig) exchange(token string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"token": token})

	return r.post(string(body))
}

// captureExchangeAudit makes db the process audit sink; the returned function
// drains the logger and returns what was recorded.
func captureExchangeAudit(t *testing.T, db *exchangeFakeDB) func() []*models.AuditLog {
	t.Helper()

	l := audit.NewLogger(db, newFixTestLogger(t), nil, 16)
	audit.SetDefault(l)

	t.Cleanup(func() { audit.SetDefault(nil) })

	return func() []*models.AuditLog {
		audit.SetDefault(nil)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := l.Shutdown(ctx); err != nil {
			t.Fatalf("audit shutdown: %v", err)
		}

		db.mu.Lock()
		defer db.mu.Unlock()

		return append([]*models.AuditLog(nil), db.audits...)
	}
}

func TestOIDCExchange_IssuesShortLivedDeployToken(t *testing.T) {
	rig := newExchangeRig(t, nil)
	drain := captureExchangeAudit(t, rig.db)

	before := time.Now()
	rw := rig.exchange(rig.iss.token(t, nil))

	if rw.Code != http.StatusOK {
		t.Fatalf("exchange = %d: %s", rw.Code, rw.Body.String())
	}

	if cc := rw.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	var resp oidcExchangeResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(resp.AccessToken, auth.TokenPrefixDeploy) || resp.TokenType != "Bearer" || resp.ExpiresIn != 600 {
		t.Fatalf("response = %+v", resp)
	}

	if len(rig.db.tokens) != 1 {
		t.Fatalf("%d tokens stored, want 1", len(rig.db.tokens))
	}

	tok := rig.db.tokens[0]

	if tok.Kind != auth.TokenKindDeploy || tok.UserID != "22222222-2222-4222-8222-222222222222" || tok.Name != "oidc:publish" {
		t.Errorf("token = kind %q user %q name %q", tok.Kind, tok.UserID, tok.Name)
	}

	if tok.ExpiresAt == nil || tok.ExpiresAt.Before(before.Add(599*time.Second)) || tok.ExpiresAt.After(time.Now().Add(601*time.Second)) {
		t.Errorf("token expires at %v, want about 600s from now", tok.ExpiresAt)
	}

	rows := drain()
	if len(rows) != 1 || rows[0].Action != audit.ActionAuthOIDCExchange {
		t.Fatalf("audit rows = %+v, want one %s", rows, audit.ActionAuthOIDCExchange)
	}

	details := string(rows[0].DetailsJSON)
	for _, want := range []string{"ci-publisher", "publish", "repo:acme/widgets:ref:refs/heads/main"} {
		if !strings.Contains(details, want) {
			t.Errorf("audit details %s lack %q", details, want)
		}
	}

	if strings.Contains(details, resp.AccessToken) {
		t.Error("audit details carry the issued token")
	}
}

// Whatever is wrong with the token, the caller sees the same response, so the
// endpoint reveals nothing about issuers, policies, claims or accounts.
func TestOIDCExchange_EveryRefusalLooksTheSame(t *testing.T) {
	rig := newExchangeRig(t, func(p *models.OIDCTrustPolicy) {})

	disabled := newExchangeRig(t, func(p *models.OIDCTrustPolicy) { p.ServiceAccountDisabled = true })

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		rig  *exchangeRig
		raw  func() string
	}{
		{"garbage", rig, func() string { return "not-a-jwt" }},
		{"unknown issuer", rig, func() string {
			return rig.iss.token(t, func(c map[string]any) { c["iss"] = "https://ci.unknown.example" })
		}},
		{"wrong signature", rig, func() string { return rig.iss.sign(t, otherKey, nil) }},
		{"expired", rig, func() string {
			return rig.iss.token(t, func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() })
		}},
		{"wrong audience", rig, func() string {
			return rig.iss.token(t, func(c map[string]any) { c["aud"] = "https://elsewhere.example" })
		}},
		{"claims do not match", rig, func() string {
			return rig.iss.token(t, func(c map[string]any) { c["repository"] = "evil/widgets" })
		}},
		{"service account disabled", disabled, func() string { return disabled.iss.token(t, nil) }},
	}

	var want string

	for _, tt := range tests {
		rw := tt.rig.exchange(tt.raw())

		if rw.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401: %s", tt.name, rw.Code, rw.Body.String())
			continue
		}

		if want == "" {
			want = rw.Body.String()
		}

		if rw.Body.String() != want {
			t.Errorf("%s: body %q differs from %q", tt.name, rw.Body.String(), want)
		}

		if len(tt.rig.db.tokens) != 0 {
			t.Errorf("%s: a token was issued", tt.name)
		}
	}

	if !strings.Contains(want, "invalid_token") {
		t.Errorf("refusal body = %q", want)
	}
}

func TestOIDCExchange_AuditsOnlyGenuineTokensNoPolicyAccepted(t *testing.T) {
	rig := newExchangeRig(t, nil)
	drain := captureExchangeAudit(t, rig.db)

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	// Noise: unverifiable tokens leave no audit row.
	rig.exchange("not-a-jwt")
	rig.exchange(rig.iss.sign(t, otherKey, nil))
	rig.exchange(rig.iss.token(t, func(c map[string]any) { c["iss"] = "https://ci.unknown.example" }))

	// A genuine token that no policy accepts does.
	if rw := rig.exchange(
		rig.iss.token(t, func(c map[string]any) { c["repository"] = "evil/widgets" }),
	); rw.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rw.Code)
	}

	rows := drain()
	if len(rows) != 1 || rows[0].Action != audit.ActionAuthOIDCExchangeFailed {
		t.Fatalf("audit rows = %+v, want one %s", rows, audit.ActionAuthOIDCExchangeFailed)
	}

	if d := string(rows[0].DetailsJSON); !strings.Contains(d, "repo:acme/widgets") || !strings.Contains(d, "conditions_not_met") {
		t.Errorf("details = %s", d)
	}
}

func TestOIDCExchange_Request(t *testing.T) {
	rig := newExchangeRig(t, nil)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"empty body", ``, http.StatusBadRequest},
		{"not json", `token=abc`, http.StatusBadRequest},
		{"no token", `{}`, http.StatusBadRequest},
		{"blank token", `{"token":"  "}`, http.StatusBadRequest},
		{"oversized", `{"token":"` + strings.Repeat("a", maxExchangeBodyBytes) + `"}`, http.StatusRequestEntityTooLarge},
	}

	for _, tt := range tests {
		if rw := rig.post(tt.body); rw.Code != tt.want {
			t.Errorf("%s: status %d, want %d: %s", tt.name, rw.Code, tt.want, rw.Body.String())
		}
	}
}

func TestOIDCExchange_NamedPolicy(t *testing.T) {
	rig := newExchangeRig(t, nil)

	body, _ := json.Marshal(map[string]string{"token": rig.iss.token(t, nil), "policy": "no-such-policy"})
	if rw := rig.post(string(body)); rw.Code != http.StatusUnauthorized {
		t.Fatalf("unknown policy name = %d, want 401", rw.Code)
	}

	body, _ = json.Marshal(map[string]string{"token": rig.iss.token(t, nil), "policy": "publish"})
	if rw := rig.post(string(body)); rw.Code != http.StatusOK {
		t.Fatalf("named policy = %d: %s", rw.Code, rw.Body.String())
	}
}

func TestOIDCExchange_RateLimitedPerAddress(t *testing.T) {
	rig := newExchangeRig(t, nil)
	rig.h.limiter = newSlidingWindowLimiter(3, time.Minute)

	for i := range 3 {
		if rw := rig.exchange("not-a-jwt"); rw.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i, rw.Code)
		}
	}

	rw := rig.exchange(rig.iss.token(t, nil))
	if rw.Code != http.StatusTooManyRequests || rw.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit = %d (Retry-After %q), want 429", rw.Code, rw.Header().Get("Retry-After"))
	}

	if !bytes.Contains(rw.Body.Bytes(), []byte("rate_limited")) || len(rig.db.tokens) != 0 {
		t.Fatalf("body %s, tokens %d", rw.Body.String(), len(rig.db.tokens))
	}
}
