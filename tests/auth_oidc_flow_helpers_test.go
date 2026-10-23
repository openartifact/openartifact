// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-jose/go-jose/v4"
	josejwt "github.com/go-jose/go-jose/v4/jwt"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

const flowBaseURL = "http://localhost:8080"

// oidcFlowDB adds the per-session SSO record (RP-initiated logout state) to
// MockDatabase.
type oidcFlowDB struct {
	*MockDatabase

	sso map[string]models.SessionSSO // by session token hash
}

func newOIDCFlowDB() *oidcFlowDB {
	return &oidcFlowDB{MockDatabase: newMockDatabase(), sso: map[string]models.SessionSSO{}}
}

var errFlowSessionUnknown = errors.New("session not found")

func (d *oidcFlowDB) SetSessionSSO(_ context.Context, sessionID, providerSlug, encryptedIDToken string) error {
	for hash, s := range d.authSessions {
		if s.ID == sessionID {
			d.sso[hash] = models.SessionSSO{ProviderSlug: providerSlug, IDToken: encryptedIDToken}
			return nil
		}
	}

	return errFlowSessionUnknown
}

func (d *oidcFlowDB) GetSessionSSO(_ context.Context, tokenHash string) (*models.SessionSSO, error) {
	rec, ok := d.sso[tokenHash]
	if !ok {
		return nil, nil
	}

	return &rec, nil
}

func (d *oidcFlowDB) DeleteSession(ctx context.Context, tokenHash string) error {
	delete(d.sso, tokenHash)
	return d.MockDatabase.DeleteSession(ctx, tokenHash)
}

// ssoGroupNames returns the local group names synced for userID and whether a
// sync happened at all (an absent groups claim never syncs).
func (d *oidcFlowDB) ssoGroupNames(userID string) ([]string, bool) {
	set, synced := d.authUserGroupsSSO[userID]
	if !synced {
		return nil, false
	}

	names := []string{}

	for id := range set {
		names = append(names, d.authGroups[id].Name)
	}

	slices.Sort(names)

	return names, true
}

// userByExternalID finds the provisioned user bound to the IdP subject.
func (d *oidcFlowDB) userByExternalID(externalID string) *models.User {
	for _, u := range d.authUsers {
		if u.ExternalID != nil && *u.ExternalID == externalID {
			return u
		}
	}

	return nil
}

// observedLogger returns a logger whose entries can be inspected.
func observedLogger() (*logger.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return &logger.Logger{SugaredLogger: zap.New(core).Sugar()}, logs
}

// logText flattens every captured entry (message and fields) for substring
// assertions.
func logText(logs *observer.ObservedLogs) string {
	var b strings.Builder

	for _, e := range logs.All() {
		fmt.Fprintf(&b, "%s %v\n", e.Message, e.ContextMap())
	}

	return b.String()
}

// flowRouter wires the OIDC handler and the generic logout the way the server
// does (both outside the CSRF middleware).
func flowRouter(t *testing.T, db *oidcFlowDB) (chi.Router, *observer.ObservedLogs) {
	t.Helper()

	log, logs := observedLogger()
	eval := auth.NewEvaluator(db, log, false)
	router := chi.NewRouter()

	api.NewOIDCHandler(db, log, eval, []byte("test-hmac-key"), flowBaseURL).RegisterPublic(router)
	api.NewAuthHandler(db, log, eval, nil).WithBaseURL(flowBaseURL).RegisterPublic(router)

	return router, logs
}

// seedFlowProvider stores an enabled auto-provisioning oidc provider.
func seedFlowProvider(t *testing.T, db *oidcFlowDB, slug string, cfg models.OIDCConfig, groupMapping map[string]string) *models.SSOProvider {
	t.Helper()

	if cfg.ClientID == "" {
		cfg.ClientID = "test-client"
	}

	if cfg.ClientSecret == "" {
		cfg.ClientSecret = "test-secret"
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal oidc config: %v", err)
	}

	now := time.Now()
	p := &models.SSOProvider{
		Slug: slug, Kind: "oidc", DisplayName: slug, Enabled: true, OIDCConfig: raw,
		GroupMapping: groupMapping, AutoProvisionUsers: true, CreatedAt: now, UpdatedAt: now,
	}

	if err := db.CreateSSOProvider(context.Background(), p); err != nil {
		t.Fatalf("CreateSSOProvider: %v", err)
	}

	return p
}

// flowIdP is a configurable OIDC provider on loopback.
type flowIdP struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	// issuerPath is where discovery lives (default root). discoveryIssuer, if
	// set, replaces the issuer the discovery document advertises; it may
	// contain %s for the server URL.
	issuerPath      string
	discoveryIssuer string
	endSession      bool

	mu     sync.Mutex
	tokens map[string]flowTokens
}

type flowTokens struct{ idToken, accessToken string }

func newFlowIdP(t *testing.T, issuerPath, discoveryIssuer string, endSession bool) *flowIdP {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	f := &flowIdP{
		key: key, issuerPath: issuerPath, discoveryIssuer: discoveryIssuer,
		endSession: endSession, tokens: map[string]flowTokens{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc(issuerPath+"/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "flow-key", Algorithm: string(jose.RS256), Use: "sig"},
		}})
	})
	mux.HandleFunc("/token", f.token)

	// The IdP listens on loopback, which the SSRF dial guard blocks.
	t.Setenv("OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS", "true")

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	return f
}

func (f *flowIdP) issuer() string { return f.srv.URL + f.issuerPath }

func (f *flowIdP) discovery(w http.ResponseWriter, _ *http.Request) {
	iss := f.issuer()
	if f.discoveryIssuer != "" {
		iss = strings.ReplaceAll(f.discoveryIssuer, "%s", f.srv.URL)
	}

	doc := map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                f.srv.URL + "/auth",
		"token_endpoint":                        f.srv.URL + "/token",
		"jwks_uri":                              f.srv.URL + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	}
	if f.endSession {
		doc["end_session_endpoint"] = f.srv.URL + "/end-session"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

func (f *flowIdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()

	f.mu.Lock()
	tok, ok := f.tokens[r.FormValue("code")]
	f.mu.Unlock()

	if !ok {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": tok.accessToken, "token_type": "Bearer", "expires_in": 3600, "id_token": tok.idToken,
	})
}

// addCode registers an authorization code that redeems to an ID token with the
// given claims (iss/aud/sub/exp default sensibly) and returns the raw token.
func (f *flowIdP) addCode(t *testing.T, code string, claims map[string]any) string {
	t.Helper()

	setDefault := func(k string, v any) {
		if _, ok := claims[k]; !ok {
			claims[k] = v
		}
	}

	setDefault("iss", f.issuer())
	setDefault("aud", []string{"test-client"})
	setDefault("sub", "flow-subject")
	setDefault("iat", time.Now().Unix())
	setDefault("exp", time.Now().Add(5*time.Minute).Unix())

	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "flow-key"))
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}

	raw, err := josejwt.Signed(sig).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	f.mu.Lock()
	f.tokens[code] = flowTokens{idToken: raw, accessToken: "fake-access-token"}
	f.mu.Unlock()

	return raw
}

// callbackOpts tweaks flowCallback.
type callbackOpts struct {
	nonce     string
	sessionCk string // pre-login session cookie value
	state     string // state query value; default matches the state cookie
}

// flowCallback performs the browser's callback request with matching
// PKCE/state/nonce cookies.
func flowCallback(t *testing.T, router chi.Router, slug, code string, o callbackOpts) *httptest.ResponseRecorder {
	t.Helper()

	state := o.state
	if state == "" {
		state = "flow-state"
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v1/auth/sso/oidc/"+slug+"/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), nil)
	req.AddCookie(&http.Cookie{Name: "oa_oidc_pkce_" + slug, Value: "fake-verifier"})
	req.AddCookie(&http.Cookie{Name: "oa_oidc_state_" + slug, Value: "flow-state"})

	if o.nonce != "" {
		req.AddCookie(&http.Cookie{Name: "oa_oidc_nonce_" + slug, Value: o.nonce})
	}

	if o.sessionCk != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: o.sessionCk})
	}

	rw := httptest.NewRecorder()
	router.ServeHTTP(rw, req)

	return rw
}

func cookieNamed(rw *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rw.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}

	return nil
}

func responseCode(t *testing.T, rw *httptest.ResponseRecorder) string {
	t.Helper()

	var resp map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body %q: %v", rw.Body.String(), err)
	}

	code, _ := resp["code"].(string)

	return code
}

// requireDenied asserts the generic SSO denial: 403 sso_login_denied, no
// session cookie, and no user provisioned for subject.
func requireDenied(t *testing.T, db *oidcFlowDB, rw *httptest.ResponseRecorder) {
	t.Helper()

	if rw.Code != http.StatusForbidden || responseCode(t, rw) != "sso_login_denied" {
		t.Fatalf("expected 403 sso_login_denied, got %d %s", rw.Code, rw.Body.String())
	}

	if c := cookieNamed(rw, auth.SessionCookieName); c != nil && c.Value != "" {
		t.Error("a denied login must not set a session cookie")
	}

	if len(db.authSessions) != 0 {
		t.Errorf("a denied login must not create a session, have %d", len(db.authSessions))
	}
}
