// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/backup"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/gpg"
	"github.com/openartifact/openartifact/pkg/logger"
)

// wantAudit fails unless the rows hold exactly the wanted actions, in order, and
// returns them.
func wantAudit(t *testing.T, rows []*models.AuditLog, actions ...string) []*models.AuditLog {
	t.Helper()

	if got := auditActions(rows); !equalStrings(got, actions) {
		t.Fatalf("audit actions = %v, want %v", got, actions)
	}

	return rows
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// wantMessage fails unless the row's details contain every fragment.
func wantMessage(t *testing.T, row *models.AuditLog, fragments ...string) {
	t.Helper()

	msg := auditMessage(t, row)
	for _, f := range fragments {
		if !strings.Contains(msg, f) {
			t.Errorf("%s details = %q, want it to contain %q", row.Action, msg, f)
		}
	}
}

// bundleCreateDB adds the one write the signing fixture does not mock.
type bundleCreateDB struct {
	*signingDB
}

func (db *bundleCreateDB) CreateReleaseBundle(_ context.Context, b *models.ReleaseBundle) error {
	b.ID = "66666666-6666-4666-8666-666666666666"
	return nil
}

// Creating, sealing and archiving a bundle each leave an explicit audit event;
// a signed seal names the key that signed the manifest.
func TestBundleLifecycleIsAudited(t *testing.T) {
	rows := recordAudit(t)

	db, _, svc := signingFixture(t)
	log, ev := newBundlesScansEnv(t, &db.bundlesScansMockDB)
	h := NewBundlesHandler(&bundleCreateDB{signingDB: db}, log, ev).WithGPGKeys(svc)

	key, err := svc.Generate(context.Background(), gpgkeys.GenerateInput{Name: "release", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	steps := []struct{ path, body string }{
		{"/api/v1/bundles", `{"name":"rel","version":"1.0"}`},
		{"/api/v1/bundles/rel/1.0/seal", `{"gpg_key_id":"` + key.ID + `"}`},
		{"/api/v1/bundles/rel/1.0/archive", ``},
	}

	for _, s := range steps {
		if rw := serveAs(h.RegisterAuthed, http.MethodPost, s.path, s.body); rw.Code >= http.StatusBadRequest {
			t.Fatalf("POST %s: %d %s", s.path, rw.Code, rw.Body.String())
		}
	}

	got := wantAudit(t, rows(), audit.ActionBundleCreated, audit.ActionBundleSealed, audit.ActionBundleArchived)
	wantMessage(t, got[0], "bundle rel 1.0 created")
	wantMessage(t, got[1], "sealed", "signed with GPG key "+key.Fingerprint)
	wantMessage(t, got[2], "archived")

	if got[1].ResourceID == nil || *got[1].ResourceID != db.bundle.ID {
		t.Errorf("seal resource id = %v, want the bundle %s", got[1].ResourceID, db.bundle.ID)
	}
}

func TestBundleSealWithoutKeyIsAuditedAsUnsigned(t *testing.T) {
	rows := recordAudit(t)

	_, h, _ := signingFixture(t)

	if rw := serveAs(h.RegisterAuthed, http.MethodPost, "/api/v1/bundles/rel/1.0/seal", `{}`); rw.Code != http.StatusOK {
		t.Fatalf("seal: %d %s", rw.Code, rw.Body.String())
	}

	wantMessage(t, wantAudit(t, rows(), audit.ActionBundleSealed)[0], "unsigned")
}

// revokeTokenDB finds every token and remembers the revocation.
type revokeTokenDB struct {
	tokenLookupMockDBStore

	revoked string
}

func (db *revokeTokenDB) GetTokenByID(_ context.Context, id string) (*models.Token, error) {
	return &models.Token{ID: id, UserID: "6f1c2f0e-3b52-4c1e-9a8e-0a7d5d3e2b11", Name: "ci"}, nil
}

func (db *revokeTokenDB) RevokeToken(_ context.Context, id string) error {
	db.revoked = id
	return nil
}

// DELETE /admin/tokens/{tokenId} leaves an auth.token_revoked event naming the
// token and its owner.
func TestAdminTokenRevokeIsAudited(t *testing.T) {
	rows := recordAudit(t)

	db := &revokeTokenDB{}
	log := newFixTestLogger(t)
	h := NewAdminTokensHandler(db, log, auth.NewEvaluator(db, log, false))

	const tokenID = "0b5f4a60-5d0a-4a9c-8f1e-3a1f2b9c7d11"

	if rw := serveAs(h.Register, http.MethodDelete, "/api/v1/admin/tokens/"+tokenID, ""); rw.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rw.Code, rw.Body.String())
	}

	got := wantAudit(t, rows(), audit.ActionAuthTokenRevoked)
	wantMessage(t, got[0], "token ci", "6f1c2f0e-3b52-4c1e-9a8e-0a7d5d3e2b11", "revoked")

	if got[0].ResourceID == nil || *got[0].ResourceID != tokenID {
		t.Errorf("resource id = %v, want %s", got[0].ResourceID, tokenID)
	}
}

// A Dependency-Track settings write is audited without the API key; a per
// repository write and delete also carry the repository.
func TestDependencyTrackConfigChangesAreAudited(t *testing.T) {
	rows := recordAudit(t)

	f := newDTFixture(t)

	f.do(t, http.MethodPut, dtGlobal, `{"enabled":true,"base_url":"`+f.dt.URL+`","api_key":"`+dtSecretKey+`"}`, true)
	f.do(t, http.MethodPut, dtRepo, `{"enabled":true,"api_key":"odt_override_secret"}`, true)
	f.do(t, http.MethodDelete, dtRepo, "", true)

	got := wantAudit(t, rows(),
		audit.ActionDependencyTrackConfigChanged, audit.ActionDependencyTrackConfigChanged, audit.ActionDependencyTrackConfigChanged)
	wantMessage(t, got[0], "global", "enabled=true", "api key replaced")
	wantMessage(t, got[1], "override of libs", "api key replaced")
	wantMessage(t, got[2], "override of libs", "deleted")

	for _, row := range got {
		if strings.Contains(string(row.DetailsJSON), "secret") || strings.Contains(string(row.DetailsJSON), f.dt.URL) {
			t.Errorf("details leak a secret or the URL: %s", row.DetailsJSON)
		}
	}

	if got[0].RepositoryID != nil || got[1].RepositoryID == nil {
		t.Errorf("repository ids = %v, %v: only the per-repository events carry one", got[0].RepositoryID, got[1].RepositoryID)
	}
}

// Cleanup policy changes and manual runs name the policy and the repository.
func TestCleanupPolicyChangesAreAudited(t *testing.T) {
	rows := recordAudit(t)

	db := &cleanupMockDB{}
	runner := &fakeRunner{}
	base := "/api/v1/repositories/r/cleanup-policies"

	create := cleanupRequest(t, db, runner, http.MethodPost, base, `{"name":"keep5","keep_last_n":5}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}

	one := base + "/" + db.policy.ID

	for _, step := range []struct{ method, path, body string }{
		{http.MethodPut, one, `{"enabled":false}`},
		{http.MethodPost, one + "/run", ""},
	} {
		if rw := cleanupRequest(t, db, runner, step.method, step.path, step.body); rw.Code >= http.StatusBadRequest {
			t.Fatalf("%s %s: %d %s", step.method, step.path, rw.Code, rw.Body.String())
		}
	}

	got := wantAudit(t, rows(), audit.ActionCleanupPolicyChanged, audit.ActionCleanupPolicyChanged, audit.ActionCleanupRunStarted)
	wantMessage(t, got[0], "keep5", "created")
	wantMessage(t, got[1], "keep5", "updated")
	wantMessage(t, got[2], "run of cleanup policy keep5 of r started")

	for _, row := range got {
		if row.RepositoryID == nil || *row.RepositoryID != "repo-1" {
			t.Errorf("%s repository id = %v, want repo-1", row.Action, row.RepositoryID)
		}
	}
}

// License policy updates and deletes are audited, naming the policy.
func TestLicensePolicyChangesAreAudited(t *testing.T) {
	rows := recordAudit(t)

	repoID := testRepoID
	db := &bundlesScansMockDB{policy: &models.LicensePolicy{ID: testPolicyID, RepositoryID: &repoID, Name: "no-gpl", Action: "warn"}}
	log, ev := newBundlesScansEnv(t, db)
	h := NewLicenseHandler(db, log, ev, nil, nil)

	path := "/api/v1/repositories/repo/license/policies/" + testPolicyID

	if rw := serveAs(h.RegisterRoutes, http.MethodPut, path, `{"action":"block"}`); rw.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rw.Code, rw.Body.String())
	}

	if rw := serveAs(h.RegisterRoutes, http.MethodDelete, path, ""); rw.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rw.Code, rw.Body.String())
	}

	got := wantAudit(t, rows(), audit.ActionLicensePolicyChanged, audit.ActionLicensePolicyChanged)
	wantMessage(t, got[0], "no-gpl", "updated")
	wantMessage(t, got[1], "no-gpl", "deleted")

	if got[0].RepositoryID == nil || *got[0].RepositoryID != repoID {
		t.Errorf("repository id = %v, want %s", got[0].RepositoryID, repoID)
	}
}

// Accepting a restore archive is audited before the restore runs.
func TestBackupRestoreIsAudited(t *testing.T) {
	rows := recordAudit(t)

	db := &backupMockDB{}
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	NewBackupHandler(db, log, auth.NewEvaluator(db, log, false), &backup.Engine{}).RegisterAuthed(r)

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	part, err := mw.CreateFormFile("file", "nightly.tar.gz")
	if err != nil {
		t.Fatal(err)
	}

	_, _ = part.Write([]byte("not a backup"))

	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/admin/restore", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "admin", IsLegacy: true}))

	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, req)

	if rw.Code != http.StatusAccepted {
		t.Fatalf("restore: %d %s", rw.Code, rw.Body.String())
	}

	wantMessage(t, wantAudit(t, rows(), audit.ActionBackupRestoreStarted)[0], "nightly.tar.gz", "started")
}
