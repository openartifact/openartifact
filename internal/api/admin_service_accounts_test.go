// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
)

const (
	saID     = "aaaaaaaa-0000-4000-8000-000000000001"
	policyID = "bbbbbbbb-0000-4000-8000-000000000001"
)

// saFakeDB is an in-memory store of service accounts and trust policies, and
// the audit sink.
type saFakeDB struct {
	mu       sync.Mutex
	accounts map[string]*models.User
	policies map[string]*models.OIDCTrustPolicy
	audits   []*models.AuditLog
	nextID   int
}

func newSAFakeDB() *saFakeDB {
	return &saFakeDB{accounts: map[string]*models.User{}, policies: map[string]*models.OIDCTrustPolicy{}}
}

func (d *saFakeDB) newID(prefix string) string {
	d.nextID++

	return prefix + "-0000-4000-8000-00000000000" + string(rune('0'+d.nextID))
}

func (d *saFakeDB) CreateServiceAccount(_ context.Context, name string, description *string, disabled bool) (*models.User, error) {
	for _, u := range d.accounts {
		if u.Username == name {
			return nil, &pgconn.PgError{Code: "23505"}
		}
	}

	u := &models.User{
		ID: d.newID("aaaaaaaa"), Username: name, FullName: description, Disabled: disabled,
		Kind: models.UserKindServiceAccount, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	d.accounts[u.ID] = u

	return u, nil
}

func (d *saFakeDB) GetServiceAccount(_ context.Context, id string) (*models.User, error) {
	return d.accounts[id], nil
}

func (d *saFakeDB) ListServiceAccounts(_ context.Context, _, _ int) ([]*models.User, int, error) {
	var out []*models.User
	for _, u := range d.accounts {
		out = append(out, u)
	}

	return out, len(out), nil
}

func (d *saFakeDB) UpdateServiceAccount(_ context.Context, id string, description *string, disabled *bool) (*models.User, error) {
	u := d.accounts[id]
	if u == nil {
		return nil, nil
	}

	if description != nil {
		u.FullName = description
	}

	if disabled != nil {
		u.Disabled = *disabled
	}

	return u, nil
}

func (d *saFakeDB) DeleteServiceAccount(_ context.Context, id string) (bool, error) {
	_, ok := d.accounts[id]
	delete(d.accounts, id)

	return ok, nil
}

func (d *saFakeDB) CreateOIDCPolicy(_ context.Context, p *models.OIDCTrustPolicy) (*models.OIDCTrustPolicy, error) {
	for _, e := range d.policies {
		if e.Name == p.Name {
			return nil, &pgconn.PgError{Code: "23505"}
		}
	}

	saved := *p
	saved.ID = d.newID("bbbbbbbb")
	saved.CreatedAt, saved.UpdatedAt = time.Now(), time.Now()

	if sa := d.accounts[p.ServiceAccountID]; sa != nil {
		saved.ServiceAccountName = sa.Username
	}

	d.policies[saved.ID] = &saved

	return &saved, nil
}

func (d *saFakeDB) GetOIDCPolicy(_ context.Context, id string) (*models.OIDCTrustPolicy, error) {
	if p := d.policies[id]; p != nil {
		c := *p
		return &c, nil
	}

	return nil, nil
}

func (d *saFakeDB) ListOIDCPolicies(context.Context) ([]*models.OIDCTrustPolicy, error) {
	var out []*models.OIDCTrustPolicy
	for _, p := range d.policies {
		out = append(out, p)
	}

	return out, nil
}

func (d *saFakeDB) UpdateOIDCPolicy(_ context.Context, p *models.OIDCTrustPolicy) (*models.OIDCTrustPolicy, error) {
	if d.policies[p.ID] == nil {
		return nil, nil
	}

	c := *p
	d.policies[p.ID] = &c

	return &c, nil
}

func (d *saFakeDB) DeleteOIDCPolicy(_ context.Context, id string) (bool, error) {
	_, ok := d.policies[id]
	delete(d.policies, id)

	return ok, nil
}

func (d *saFakeDB) InsertAuditLog(_ context.Context, event *models.AuditLog) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.audits = append(d.audits, event)

	return nil
}

type saRig struct {
	t  *testing.T
	db *saFakeDB
	r  chi.Router
}

func newSARig(t *testing.T) *saRig {
	t.Helper()

	db := newSAFakeDB()
	log := newFixTestLogger(t)
	ev := auth.NewEvaluator(nil, log, false)

	r := chi.NewRouter()
	NewAdminServiceAccountsHandler(db, log, ev).Register(r)
	NewAdminOIDCPoliciesHandler(db, log, ev).Register(r)

	return &saRig{t: t, db: db, r: r}
}

func (s *saRig) do(admin bool, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	p := &auth.Principal{Username: "admin", IsLegacy: true}
	if !admin {
		p = &auth.Principal{IsAnonymous: true}
	}

	req = req.WithContext(middleware.WithPrincipal(req.Context(), p))

	rw := httptest.NewRecorder()
	s.r.ServeHTTP(rw, req)

	return rw
}

// audited drains the audit logger and returns the recorded actions.
func (s *saRig) auditedActions(drain func() []*models.AuditLog) []string {
	var actions []string
	for _, row := range drain() {
		actions = append(actions, row.Action)
	}

	return actions
}

func (s *saRig) capture() func() []*models.AuditLog {
	s.t.Helper()

	l := audit.NewLogger(s.db, newFixTestLogger(s.t), nil, 16)
	audit.SetDefault(l)

	s.t.Cleanup(func() { audit.SetDefault(nil) })

	return func() []*models.AuditLog {
		audit.SetDefault(nil)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := l.Shutdown(ctx); err != nil {
			s.t.Fatalf("audit shutdown: %v", err)
		}

		s.db.mu.Lock()
		defer s.db.mu.Unlock()

		return append([]*models.AuditLog(nil), s.db.audits...)
	}
}

func TestServiceAccountsRequireAdmin(t *testing.T) {
	rig := newSARig(t)
	rig.db.accounts[saID] = &models.User{ID: saID, Username: "ci", Kind: models.UserKindServiceAccount}
	rig.db.policies[policyID] = &models.OIDCTrustPolicy{ID: policyID, Name: "p"}

	for _, c := range []struct{ method, target, body string }{
		{http.MethodGet, "/api/v1/service-accounts", ""},
		{http.MethodPost, "/api/v1/service-accounts", `{"name":"another"}`},
		{http.MethodGet, "/api/v1/service-accounts/" + saID, ""},
		{http.MethodPatch, "/api/v1/service-accounts/" + saID, `{"disabled":true}`},
		{http.MethodDelete, "/api/v1/service-accounts/" + saID, ""},
		{http.MethodGet, "/api/v1/oidc-trust-policies", ""},
		{http.MethodPost, "/api/v1/oidc-trust-policies", `{}`},
		{http.MethodGet, "/api/v1/oidc-trust-policies/" + policyID, ""},
		{http.MethodPatch, "/api/v1/oidc-trust-policies/" + policyID, `{"enabled":false}`},
		{http.MethodDelete, "/api/v1/oidc-trust-policies/" + policyID, ""},
	} {
		if rw := rig.do(false, c.method, c.target, c.body); rw.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", c.method, c.target, rw.Code)
		}
	}

	if len(rig.db.accounts) != 1 || len(rig.db.policies) != 1 || rig.db.accounts[saID].Disabled {
		t.Error("a refused request changed state")
	}
}

func TestServiceAccountsLifecycleIsAudited(t *testing.T) {
	rig := newSARig(t)
	drain := rig.capture()

	rw := rig.do(true, http.MethodPost, "/api/v1/service-accounts", `{"name":"ci-publisher","description":"publishes releases"}`)
	if rw.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rw.Code, rw.Body.String())
	}

	var created serviceAccountResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	if created.Name != "ci-publisher" || created.Description != "publishes releases" || created.Disabled || created.ID == "" {
		t.Fatalf("created = %+v", created)
	}

	if rw := rig.do(true, http.MethodPost, "/api/v1/service-accounts", `{"name":"ci-publisher"}`); rw.Code != http.StatusConflict {
		t.Errorf("duplicate name = %d, want 409", rw.Code)
	}

	rw = rig.do(true, http.MethodPatch, "/api/v1/service-accounts/"+created.ID, `{"disabled":true,"description":"frozen"}`)
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), `"disabled":true`) || !strings.Contains(rw.Body.String(), "frozen") {
		t.Fatalf("patch = %d: %s", rw.Code, rw.Body.String())
	}

	rw = rig.do(true, http.MethodGet, "/api/v1/service-accounts", "")
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), `"total":1`) {
		t.Fatalf("list = %d: %s", rw.Code, rw.Body.String())
	}

	if rw := rig.do(true, http.MethodDelete, "/api/v1/service-accounts/"+created.ID, ""); rw.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rw.Code, rw.Body.String())
	}

	if rw := rig.do(true, http.MethodGet, "/api/v1/service-accounts/"+created.ID, ""); rw.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rw.Code)
	}

	if rw := rig.do(true, http.MethodDelete, "/api/v1/service-accounts/"+created.ID, ""); rw.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", rw.Code)
	}

	got := rig.auditedActions(drain)
	want := []string{audit.ActionServiceAccountCreated, audit.ActionServiceAccountUpdated, audit.ActionServiceAccountDeleted}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audited %v, want %v", got, want)
	}
}

func TestServiceAccountsValidation(t *testing.T) {
	rig := newSARig(t)

	for name, body := range map[string]string{
		"name too short":       `{"name":"ab"}`,
		"name with a space":    `{"name":"ci publisher"}`,
		"name with a slash":    `{"name":"ci/publisher"}`,
		"description too long": `{"name":"ci-ok","description":"` + strings.Repeat("x", 256) + `"}`,
		"control character":    `{"name":"ci-ok","description":"a\u0000b"}`,
		"not json":             `name=ci`,
	} {
		if rw := rig.do(true, http.MethodPost, "/api/v1/service-accounts", body); rw.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400: %s", name, rw.Code, rw.Body.String())
		}
	}

	if len(rig.db.accounts) != 0 {
		t.Error("an invalid request created an account")
	}

	if rw := rig.do(true, http.MethodPatch, "/api/v1/service-accounts/"+saID, `{"disabled":true}`); rw.Code != http.StatusNotFound {
		t.Errorf("patch of an unknown account = %d, want 404", rw.Code)
	}

	if rw := rig.do(true, http.MethodGet, "/api/v1/service-accounts/not-a-uuid", ""); rw.Code != http.StatusNotFound {
		t.Errorf("get with a malformed id = %d, want 404", rw.Code)
	}
}

func TestOIDCPoliciesLifecycleIsAudited(t *testing.T) {
	rig := newSARig(t)
	rig.db.accounts[saID] = &models.User{ID: saID, Username: "ci-publisher", Kind: models.UserKindServiceAccount}
	drain := rig.capture()

	// A public address: the issuer must pass the outbound-request check.
	create := `{"name":"gh-release","issuer":"https://8.8.8.8","audience":"https://oa.example",` +
		`"conditions":[{"claim":"repository","patterns":["acme/*"]},{"claim":"ref","patterns":["refs/tags/v*"]}],` +
		`"service_account_id":"` + saID + `"}`

	rw := rig.do(true, http.MethodPost, "/api/v1/oidc-trust-policies", create)
	if rw.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rw.Code, rw.Body.String())
	}

	var created oidcPolicyResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	if created.TokenTTLSeconds != auth.OIDCPolicyDefaultTTL || !created.Enabled || created.ServiceAccount != "ci-publisher" ||
		len(created.Conditions) != 2 || created.CreatedBy != "admin" {
		t.Fatalf("created = %+v", created)
	}

	if rw := rig.do(true, http.MethodPost, "/api/v1/oidc-trust-policies", create); rw.Code != http.StatusConflict {
		t.Errorf("duplicate name = %d, want 409", rw.Code)
	}

	rw = rig.do(true, http.MethodPatch, "/api/v1/oidc-trust-policies/"+created.ID, `{"token_ttl_seconds":3600,"enabled":false}`)
	if rw.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rw.Code, rw.Body.String())
	}

	stored := rig.db.policies[created.ID]
	if stored.TokenTTLSeconds != 3600 || stored.Enabled || stored.Name != "gh-release" || len(stored.Conditions) != 2 {
		t.Errorf("patched policy = %+v (omitted fields must keep their value)", stored)
	}

	if rw := rig.do(
		true,
		http.MethodGet,
		"/api/v1/oidc-trust-policies",
		"",
	); rw.Code != http.StatusOK ||
		!strings.Contains(rw.Body.String(), "gh-release") {
		t.Errorf("list = %d: %s", rw.Code, rw.Body.String())
	}

	if rw := rig.do(true, http.MethodDelete, "/api/v1/oidc-trust-policies/"+created.ID, ""); rw.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rw.Code)
	}

	if rw := rig.do(true, http.MethodGet, "/api/v1/oidc-trust-policies/"+created.ID, ""); rw.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", rw.Code)
	}

	got := rig.auditedActions(drain)
	want := []string{audit.ActionOIDCPolicyCreated, audit.ActionOIDCPolicyUpdated, audit.ActionOIDCPolicyDeleted}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audited %v, want %v", got, want)
	}
}

func TestOIDCPoliciesValidation(t *testing.T) {
	rig := newSARig(t)
	rig.db.accounts[saID] = &models.User{ID: saID, Username: "ci-publisher", Kind: models.UserKindServiceAccount}

	policy := func(mutate string) string {
		base := map[string]any{
			"name": "p", "issuer": "https://8.8.8.8", "audience": "https://oa.example", "service_account_id": saID,
			"conditions": []map[string]any{{"claim": "repository", "patterns": []string{"acme/*"}}},
		}

		var extra map[string]any
		if mutate != "" {
			_ = json.Unmarshal([]byte(mutate), &extra)
		}

		maps.Copy(base, extra)

		b, _ := json.Marshal(base)

		return string(b)
	}

	tests := []struct {
		name string
		body string
	}{
		{"not json", `x`},
		{"fields missing", `{"name":"p"}`},
		{"ttl above an hour", policy(`{"token_ttl_seconds":3601}`)},
		{"ttl below a minute", policy(`{"token_ttl_seconds":10}`)},
		{"no conditions", policy(`{"conditions":[]}`)},
		{"wildcard-only pattern", policy(`{"conditions":[{"claim":"repository","patterns":["*"]}]}`)},
		{"shared issuer without identity condition", policy(
			`{"issuer":"https://token.actions.githubusercontent.com","conditions":[{"claim":"ref","patterns":["refs/heads/main"]}]}`)},
		{"issuer over plain http to a public host", policy(`{"issuer":"http://8.8.8.8"}`)},
		{"issuer on loopback", policy(`{"issuer":"https://127.0.0.1"}`)},
		{"issuer on a private network", policy(`{"issuer":"https://10.0.0.5"}`)},
		{"issuer is not a url", policy(`{"issuer":"github"}`)},
		{"service account does not exist", policy(`{"service_account_id":"cccccccc-0000-4000-8000-000000000009"}`)},
		{"service account id is not a uuid", policy(`{"service_account_id":"ci-publisher"}`)},
		{"bad name", policy(`{"name":"has space"}`)},
		{"no audience", policy(`{"audience":""}`)},
	}

	for _, tt := range tests {
		if rw := rig.do(true, http.MethodPost, "/api/v1/oidc-trust-policies", tt.body); rw.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400: %s", tt.name, rw.Code, rw.Body.String())
		}
	}

	if len(rig.db.policies) != 0 {
		t.Error("an invalid request created a policy")
	}
}

// A human account is not a valid target: only service accounts can be mapped.
func TestOIDCPolicyRejectsHumanTarget(t *testing.T) {
	rig := newSARig(t)

	// The store's GetServiceAccount returns nothing for a human id.
	body := `{"name":"p","issuer":"https://8.8.8.8","audience":"a","service_account_id":"` + idAdminUser +
		`","conditions":[{"claim":"repository","patterns":["acme/*"]}]}`

	if rw := rig.do(true, http.MethodPost, "/api/v1/oidc-trust-policies", body); rw.Code != http.StatusBadRequest {
		t.Fatalf("human target = %d, want 400: %s", rw.Code, rw.Body.String())
	}
}

// Editing a policy does not re-run the outbound check on an issuer that did not
// change, so a policy stays editable while its issuer is down or its DNS moved.
func TestOIDCPolicyPatchKeepsIssuerWithoutRecheck(t *testing.T) {
	rig := newSARig(t)
	rig.db.accounts[saID] = &models.User{ID: saID, Username: "ci-publisher", Kind: models.UserKindServiceAccount}
	rig.db.policies[policyID] = &models.OIDCTrustPolicy{
		ID:               policyID,
		Name:             "legacy",
		Issuer:           "https://127.0.0.1",
		Audience:         "a",
		ServiceAccountID: saID,
		TokenTTLSeconds:  900,
		Enabled:          true,
		Conditions:       []models.OIDCCondition{{Claim: "repository", Patterns: []string{"acme/*"}}},
	}

	if rw := rig.do(true, http.MethodPatch, "/api/v1/oidc-trust-policies/"+policyID, `{"enabled":false}`); rw.Code != http.StatusOK {
		t.Fatalf("patch without issuer change = %d: %s", rw.Code, rw.Body.String())
	}

	if rw := rig.do(
		true,
		http.MethodPatch,
		"/api/v1/oidc-trust-policies/"+policyID,
		`{"issuer":"https://10.0.0.9"}`,
	); rw.Code != http.StatusBadRequest {
		t.Fatalf("patch to a private issuer = %d, want 400", rw.Code)
	}
}
