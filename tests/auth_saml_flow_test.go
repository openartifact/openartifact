// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/xml"
	"html"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewjam/saml"
	samllogger "github.com/crewjam/saml/logger"
	"github.com/crewjam/saml/samlsp"
	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/database/models"
)

// samlReplayDB adds the DB-backed assertion replay cache and the system
// settings store (the persisted SP keypair) to MockDatabase.
type samlReplayDB struct {
	*MockDatabase

	mu       sync.Mutex
	seen     map[string]bool
	consumed int
	settings map[string]string
}

func newSAMLReplayDB(mock *MockDatabase) *samlReplayDB {
	return &samlReplayDB{MockDatabase: mock, seen: map[string]bool{}, settings: map[string]string{}}
}

// GetUserByExternalID finds users the provisioner created, so a second login by
// the same identity signs into the same account (MockDatabase only knows
// pre-seeded bindings).
func (d *samlReplayDB) GetUserByExternalID(_ context.Context, externalID, providerSlug string) (*models.User, error) {
	for _, u := range d.authUsers {
		if u.ExternalID != nil && *u.ExternalID == externalID && u.ProviderSlug != nil && *u.ProviderSlug == providerSlug {
			return u, nil
		}
	}

	return nil, nil
}

func (d *samlReplayDB) GetSystemSetting(_ context.Context, key string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.settings[key], nil
}

func (d *samlReplayDB) CreateSystemSettingIfAbsent(_ context.Context, key, value string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.settings[key]; ok {
		return false, nil
	}

	d.settings[key] = value

	return true, nil
}

func (d *samlReplayDB) ConsumeSAMLAssertion(_ context.Context, slug, assertionID string, _ time.Time) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.consumed++

	key := slug + "|" + assertionID
	if d.seen[key] {
		return false, nil
	}

	d.seen[key] = true

	return true, nil
}

type fixedSAMLSession struct{}

func (fixedSAMLSession) GetSession(_ http.ResponseWriter, _ *http.Request, _ *saml.IdpAuthnRequest) *saml.Session {
	now := saml.TimeNow()
	attr := func(name, value string) saml.Attribute {
		return saml.Attribute{Name: name, Values: []saml.AttributeValue{{Type: "xs:string", Value: value}}}
	}

	return &saml.Session{
		ID:         "idp-session-1",
		CreateTime: now,
		ExpireTime: now.Add(time.Hour),
		Index:      "1",
		NameID:     "alice-nameid",
		CustomAttributes: []saml.Attribute{
			attr("uid", "alice"),
			attr("mail", "alice@example.com"),
			attr("displayName", "Alice Example"),
		},
	}
}

type staticSPProvider struct{ md *saml.EntityDescriptor }

func (s *staticSPProvider) GetServiceProvider(_ *http.Request, _ string) (*saml.EntityDescriptor, error) {
	return s.md, nil
}

func newTestSAMLIdP(t *testing.T) *saml.IdentityProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate idp key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "idp.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create idp cert: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse idp cert: %v", err)
	}

	metadataURL, _ := url.Parse("https://idp.test/metadata")
	ssoURL, _ := url.Parse("https://idp.test/sso")

	return &saml.IdentityProvider{
		Key:             key,
		Certificate:     cert,
		Logger:          samllogger.DefaultLogger,
		MetadataURL:     *metadataURL,
		SSOURL:          *ssoURL,
		SessionProvider: fixedSAMLSession{},
		SignatureMethod: "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256",
	}
}

type samlFlowEnv struct {
	t      *testing.T
	db     *samlReplayDB
	router chi.Router
	idp    *saml.IdentityProvider
	// plain is idp without assertion encryption (see plainTextIDP).
	plain *saml.IdentityProvider
	row   *models.SSOProvider
	slug  string
}

func idpMetadataXML(t *testing.T, idp *saml.IdentityProvider) string {
	t.Helper()

	b, err := xml.Marshal(idp.Metadata())
	if err != nil {
		t.Fatalf("marshal idp metadata: %v", err)
	}

	return string(b)
}

// samlEnvOption adjusts the test IdP or the provider's saml_config before the
// provider is first loaded.
type samlEnvOption func(t *testing.T, idp *saml.IdentityProvider, cfg *models.SAMLConfig)

func withAllowIDPInitiated(_ *testing.T, _ *saml.IdentityProvider, cfg *models.SAMLConfig) {
	cfg.AllowIDPInitiated = true
}

func newSAMLFlowEnv(t *testing.T, slug string, opts ...samlEnvOption) *samlFlowEnv {
	t.Helper()

	const baseURL = "http://localhost:8080"

	mock := newMockDatabase()
	db := newSAMLReplayDB(mock)
	idp := newTestSAMLIdP(t)
	row := seedSAMLProvider(t, mock, slug, idpMetadataXML(t, idp))

	if len(opts) > 0 {
		cfg := models.SAMLConfig{IDPMetadataXML: idpMetadataXML(t, idp)}
		for _, o := range opts {
			o(t, idp, &cfg)
		}

		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal saml config: %v", err)
		}

		row.SAMLConfig = raw
	}

	log := newHandlerLogger(t)
	eval := auth.NewEvaluator(db, log, false)
	h := api.NewSAMLHandler(db, log, eval, []byte("test-hmac-key"), baseURL, &config.Config{BaseURL: baseURL})
	router := chi.NewRouter()
	h.RegisterPublic(router)

	env := &samlFlowEnv{t: t, db: db, router: router, idp: idp, row: row, slug: slug}

	// The IdP learns the SP from the SP's own metadata endpoint.
	rw := env.do(httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/saml/"+slug+"/metadata", nil))
	if rw.Code != http.StatusOK {
		t.Fatalf("sp metadata: %d %s", rw.Code, rw.Body.String())
	}

	spMeta, err := samlsp.ParseMetadata(rw.Body.Bytes())
	if err != nil {
		t.Fatalf("parse sp metadata: %v", err)
	}

	idp.ServiceProviderProvider = &staticSPProvider{md: spMeta}
	env.plain = plainTextIDP(idp)

	return env
}

func (e *samlFlowEnv) do(req *http.Request) *httptest.ResponseRecorder {
	rw := httptest.NewRecorder()
	e.router.ServeHTTP(rw, req)

	return rw
}

func (e *samlFlowEnv) trackerName() string { return "oa_saml_req_" + e.slug }

// login starts SP-initiated login; it returns the IdP redirect and the tracker cookie.
func (e *samlFlowEnv) login() (string, *http.Cookie) {
	e.t.Helper()

	rw := e.do(httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/saml/"+e.slug+"/login?redirect=/dash", nil))
	if rw.Code != http.StatusFound {
		e.t.Fatalf("login: expected 302, got %d (%s)", rw.Code, rw.Body.String())
	}

	for _, c := range rw.Result().Cookies() {
		if c.Name == e.trackerName() {
			return rw.Header().Get("Location"), c
		}
	}

	e.t.Fatalf("login did not set the %s tracker cookie", e.trackerName())

	return "", nil
}

var samlFormValueRe = regexp.MustCompile(`name="(SAMLResponse|RelayState)" value="([^"]*)"`)

// idpRespond has the test IdP answer the AuthnRequest in location.
func (e *samlFlowEnv) idpRespond(location string) (string, string) {
	e.t.Helper()

	return e.idpRespondWith(e.idp, location)
}

func (e *samlFlowEnv) idpRespondWith(idp *saml.IdentityProvider, location string) (string, string) {
	e.t.Helper()

	rw := httptest.NewRecorder()
	idp.ServeSSO(rw, httptest.NewRequestWithContext(context.Background(), "GET", location, nil))

	return e.parseIDPForm(rw)
}

// idpInitiated has idp issue an unsolicited response (an IdP app tile click).
func (e *samlFlowEnv) idpInitiated(idp *saml.IdentityProvider) string {
	e.t.Helper()

	rw := httptest.NewRecorder()
	idp.ServeIDPInitiated(rw, httptest.NewRequestWithContext(context.Background(), "GET", "https://idp.test/launch", nil), "sp", "")

	resp, _ := e.parseIDPForm(rw)

	return resp
}

func (e *samlFlowEnv) parseIDPForm(rw *httptest.ResponseRecorder) (string, string) {
	e.t.Helper()

	if rw.Code != http.StatusOK {
		e.t.Fatalf("idp: expected 200, got %d (%s)", rw.Code, rw.Body.String())
	}

	vals := map[string]string{}
	for _, m := range samlFormValueRe.FindAllStringSubmatch(rw.Body.String(), -1) {
		vals[m[1]] = html.UnescapeString(m[2])
	}

	if vals["SAMLResponse"] == "" {
		e.t.Fatalf("idp response form has no SAMLResponse: %s", rw.Body.String())
	}

	return vals["SAMLResponse"], vals["RelayState"]
}

func (e *samlFlowEnv) acs(samlResponse, relayState string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()

	form := url.Values{"SAMLResponse": {samlResponse}, "RelayState": {relayState}}
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/auth/sso/saml/"+e.slug+"/acs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}

	return e.do(req)
}

func hasCookie(rw *httptest.ResponseRecorder, name string) bool {
	for _, c := range rw.Result().Cookies() {
		if c.Name == name && c.Value != "" && c.MaxAge >= 0 {
			return true
		}
	}

	return false
}

func TestSAML_ACS_HappyPath_ThenReplayRejected(t *testing.T) {
	env := newSAMLFlowEnv(t, "flow")

	loc, tracker := env.login()
	resp, relay := env.idpRespond(loc)

	rw := env.acs(resp, relay, tracker)
	if rw.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d (%s)", rw.Code, rw.Body.String())
	}

	if got := rw.Header().Get("Location"); got != "/dash" {
		t.Errorf("expected redirect to /dash, got %q", got)
	}

	if !hasCookie(rw, auth.SessionCookieName) {
		t.Error("expected a session cookie after a valid SAML login")
	}

	if env.db.authUsersByName["alice"] == nil {
		t.Error("expected user alice to be provisioned")
	}

	// The tracker cookie is single-use: cleared on the response.
	cleared := false

	for _, c := range rw.Result().Cookies() {
		if c.Name == env.trackerName() && c.MaxAge < 0 {
			cleared = true
		}
	}

	if !cleared {
		t.Error("expected the tracker cookie to be cleared after the ACS")
	}

	// Replaying the very same response (attacker also holds the tracker cookie).
	rw = env.acs(resp, relay, tracker)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("replayed assertion must be rejected with 401, got %d (%s)", rw.Code, rw.Body.String())
	}

	if hasCookie(rw, auth.SessionCookieName) {
		t.Error("replayed assertion must not create a session")
	}
}

func TestSAML_ACS_InResponseToMismatchRejected(t *testing.T) {
	env := newSAMLFlowEnv(t, "mismatch")

	loc1, _ := env.login()
	_, tracker2 := env.login() // tracker now holds a different AuthnRequest ID

	resp, relay := env.idpRespond(loc1)

	rw := env.acs(resp, relay, tracker2)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("response to another request must be rejected with 400, got %d (%s)", rw.Code, rw.Body.String())
	}

	if hasCookie(rw, auth.SessionCookieName) || env.db.authUsersByName["alice"] != nil {
		t.Error("rejected response must not create a session or user")
	}

	if env.db.consumed != 0 {
		t.Error("assertion must not be consumed when InResponseTo does not match")
	}
}

func TestSAML_ACS_UnsolicitedResponseRejected(t *testing.T) {
	env := newSAMLFlowEnv(t, "unsolicited")

	loc, _ := env.login()
	resp, relay := env.idpRespond(loc)

	rw := env.acs(resp, relay) // no tracker cookie: IdP-initiated / cross-browser
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a response without a tracked request, got %d (%s)", rw.Code, rw.Body.String())
	}

	if env.db.authUsersByName["alice"] != nil {
		t.Error("unsolicited response must not provision a user")
	}
}

func TestSAML_ACS_ForgedTrackerCookieRejected(t *testing.T) {
	env := newSAMLFlowEnv(t, "forged")

	loc, tracker := env.login()
	resp, relay := env.idpRespond(loc)

	forged := &http.Cookie{Name: tracker.Name, Value: tracker.Value[:len(tracker.Value)-1] + "0"}
	if forged.Value == tracker.Value {
		forged.Value = tracker.Value[:len(tracker.Value)-1] + "1"
	}

	rw := env.acs(resp, relay, forged)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a tampered tracker cookie, got %d", rw.Code)
	}
}

func TestSAML_ACS_BadRelayState_NoSideEffects(t *testing.T) {
	env := newSAMLFlowEnv(t, "relay")

	loc, tracker := env.login()
	resp, _ := env.idpRespond(loc)

	rw := env.acs(resp, "not-a-signed-relay-state", tracker)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a bad RelayState, got %d (%s)", rw.Code, rw.Body.String())
	}

	if hasCookie(rw, auth.SessionCookieName) {
		t.Error("a rejected RelayState must not set a session cookie")
	}

	if env.db.authUsersByName["alice"] != nil {
		t.Error("a rejected RelayState must not provision a user")
	}

	if env.db.consumed != 0 {
		t.Error("a rejected RelayState must not consume the assertion")
	}
}

func TestSAML_DisabledProvider_Rejected(t *testing.T) {
	env := newSAMLFlowEnv(t, "disabled")

	loc, tracker := env.login()
	resp, relay := env.idpRespond(loc)

	env.row.Enabled = false

	for name, req := range map[string]*http.Request{
		"login":    httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/saml/disabled/login", nil),
		"metadata": httptest.NewRequestWithContext(context.Background(), "GET", "/api/v1/auth/sso/saml/disabled/metadata", nil),
	} {
		if rw := env.do(req); rw.Code != http.StatusNotFound {
			t.Errorf("%s: expected 404 for a disabled provider, got %d", name, rw.Code)
		}
	}

	rw := env.acs(resp, relay, tracker)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("acs: expected 404 for a disabled provider, got %d (%s)", rw.Code, rw.Body.String())
	}

	var body map[string]any

	_ = json.NewDecoder(rw.Body).Decode(&body)
	if body["code"] != "provider_not_found" {
		t.Errorf("disabled must be indistinguishable from unknown, got code=%v", body["code"])
	}

	if env.db.authUsersByName["alice"] != nil {
		t.Error("disabled provider must not provision users")
	}
}

// A replaced IdP certificate (same EntityID) must take effect immediately: a
// response signed by the old key is no longer accepted.
func TestSAML_IdPMetadataRotation_NotServedStale(t *testing.T) {
	env := newSAMLFlowEnv(t, "rotate")

	// Warm the provider cache with the original IdP key.
	loc, tracker := env.login()
	resp, relay := env.idpRespond(loc)

	if rw := env.acs(resp, relay, tracker); rw.Code != http.StatusFound {
		t.Fatalf("baseline login failed: %d (%s)", rw.Code, rw.Body.String())
	}

	// Admin replaces the IdP metadata with a new certificate.
	rotated := newTestSAMLIdP(t)

	cfg, err := json.Marshal(models.SAMLConfig{IDPMetadataXML: idpMetadataXML(t, rotated)})
	if err != nil {
		t.Fatalf("marshal saml config: %v", err)
	}

	env.row.SAMLConfig = cfg
	env.row.UpdatedAt = time.Now().Add(time.Second)

	// The old (now untrusted) IdP key signs a fresh response.
	loc, tracker = env.login()
	resp, relay = env.idpRespond(loc)

	rw := env.acs(resp, relay, tracker)
	if rw.Code == http.StatusFound {
		t.Fatal("a response signed by a replaced IdP certificate must be rejected")
	}

	if rw.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d (%s)", rw.Code, rw.Body.String())
	}
}
