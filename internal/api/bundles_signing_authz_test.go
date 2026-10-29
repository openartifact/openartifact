// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/audit"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/internal/gpgkeys/gpgkeystest"
	"github.com/openartifact/openartifact/pkg/gpg"
)

// signerEnv is a draft bundle "b/1" owned by the non-admin user "ci", who may
// read its source repository, plus a stored key and the configuration key.
type signerEnv struct {
	db    *authzMockDB
	h     *BundlesHandler
	ci    authzCaller
	key   *models.GPGKey
	cfgFP string
}

func newSignerEnv(t *testing.T, signers ...string) *signerEnv {
	t.Helper()

	if err := crypto.Init("bundle-signer-authz-secret-0123456789"); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	cfgEntity, err := gpg.GenerateEntity("Config", "", "", gpg.AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	cfgSigner, err := gpg.NewLocalSignerFromEntity(cfgEntity)
	if err != nil {
		t.Fatal(err)
	}

	db := newAuthzDB()
	db.grant("ci-id", "src", "read")

	h := newAuthzHandler(t, db)
	svc := gpgkeys.New(gpgkeystest.New(), h.log, cfgSigner)

	key, err := svc.Generate(context.Background(), gpgkeys.GenerateInput{Name: "release", Algorithm: gpg.AlgorithmED25519})
	if err != nil {
		t.Fatal(err)
	}

	env := &signerEnv{db: db, h: h.WithGPGKeys(svc), ci: authzUser("ci"), key: key, cfgFP: cfgSigner.KeyFingerprint()}

	h.WithBundleSigners(env.expand(signers))

	expectStatus(t, "create", env.ci.do(t, h, http.MethodPost, "/api/v1/bundles", `{"name":"b","version":"1"}`), http.StatusCreated)
	expectStatus(t, "add", env.ci.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/items", authzItemBody(authzSrcID)), http.StatusCreated)

	return env
}

// expand replaces the placeholders KEYID and KEYFPR of grant entries.
func (e *signerEnv) expand(entries []string) []string {
	out := make([]string, len(entries))
	r := strings.NewReplacer("KEYID", e.key.ID, "KEYFPR", e.key.Fingerprint)

	for i, s := range entries {
		out[i] = r.Replace(s)
	}

	return out
}

func (e *signerEnv) seal(t *testing.T, who authzCaller, ref string) *httptest.ResponseRecorder {
	t.Helper()

	e.db.bundles["b/1"].State = models.BundleStateDraft

	return who.do(t, e.h, http.MethodPost, "/api/v1/bundles/b/1/seal", `{"gpg_key_id":"`+ref+`"}`)
}

func TestBundleSealSignerAuthorization(t *testing.T) {
	tests := []struct {
		name    string
		signers []string
		admin   bool
		ref     string // KEYID, KEYFPR, CONFIG, CONFIGFPR or a literal
		want    int
	}{
		{"reader without a grant is refused", nil, false, "KEYID", http.StatusForbidden},
		{"reader without a grant by fingerprint", nil, false, "KEYFPR", http.StatusForbidden},
		{"grant of another user", []string{"KEYID=somebody"}, false, "KEYID", http.StatusForbidden},
		{"grant for another key", []string{"0000000000000000000000000000000000000000=ci"}, false, "KEYID", http.StatusForbidden},
		{"grant by key id", []string{"KEYID=ci"}, false, "KEYID", http.StatusOK},
		{"grant by fingerprint, key by id", []string{"KEYFPR=ci"}, false, "KEYID", http.StatusOK},
		{"grant by id, key by lower-case fingerprint", []string{"KEYID=ci"}, false, "lower:KEYFPR", http.StatusOK},
		{"grant is not enough for the config key", []string{"KEYID=ci"}, false, "config", http.StatusForbidden},
		{
			"grant is not enough for an unknown key",
			[]string{"KEYID=ci"},
			false,
			"00000000-0000-4000-8000-00000000ffff",
			http.StatusForbidden,
		},
		{"administrator signs with any stored key", nil, true, "KEYID", http.StatusOK},
		{"administrator may not use the config key", nil, true, "config", http.StatusForbidden},
		{"administrator may not use the config key by fingerprint", nil, true, "CONFIGFPR", http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newSignerEnv(t, tc.signers...)

			who := env.ci
			if tc.admin {
				who = authzAdmin()
			}

			ref := strings.NewReplacer("lower:KEYFPR", strings.ToLower(env.key.Fingerprint), "KEYID", env.key.ID,
				"KEYFPR", env.key.Fingerprint, "CONFIGFPR", env.cfgFP).Replace(tc.ref)

			rw := env.seal(t, who, ref)
			if rw.Code != tc.want {
				t.Fatalf("seal with %q: status %d, want %d (%s)", ref, rw.Code, tc.want, rw.Body.String())
			}

			sealed := env.db.bundles["b/1"].State == models.BundleStateSealed
			if sealed != (tc.want == http.StatusOK) {
				t.Errorf("bundle sealed = %v with status %d", sealed, rw.Code)
			}
		})
	}
}

func TestBundleSealWithoutKeyStaysOpenToTheOwner(t *testing.T) {
	env := newSignerEnv(t)
	env.db.bundles["b/1"].State = models.BundleStateDraft

	rw := env.ci.do(t, env.h, http.MethodPost, "/api/v1/bundles/b/1/seal", "")
	expectStatus(t, "unsigned seal by the owner", rw, http.StatusOK)
}

func TestBundleItemsMustBeStoredInTheSourceRepository(t *testing.T) {
	sha := strings.Repeat("a", 64)

	tests := []struct {
		name   string
		stored func(repoID, sha, path string) bool
		format string
		want   int
	}{
		{
			"stored under that path",
			func(r, s, p string) bool { return r == authzSrcID && s == sha && p == "x.bin" },
			"",
			http.StatusCreated,
		},
		{"digest stored under another path", func(_, _, p string) bool { return p != "x.bin" }, "", http.StatusBadRequest},
		{"digest not stored in the repository", func(string, string, string) bool { return false }, "", http.StatusBadRequest},
		{"format of another kind", nil, "npm", http.StatusBadRequest},
		{"format of the repository", nil, "raw", http.StatusCreated},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newAuthzDB()
			db.grant("ci-id", "src", "read")
			db.stored = tc.stored
			h := newAuthzHandler(t, db)
			ci := authzUser("ci")

			expectStatus(t, "create", ci.do(t, h, http.MethodPost, "/api/v1/bundles", `{"name":"b","version":"1"}`), http.StatusCreated)

			body := `{"items":[{"source_repo_id":"` + authzSrcID + `","sha256":"` + sha + `","artifact_path":"x.bin","format":"` + tc.format + `"}]}`
			rw := ci.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/items", body)

			if rw.Code != tc.want {
				t.Fatalf("status %d, want %d (%s)", rw.Code, tc.want, rw.Body.String())
			}

			if stored := len(db.items[db.bundles["b/1"].ID]); (stored == 1) != (tc.want == http.StatusCreated) {
				t.Errorf("%d items stored with status %d", stored, rw.Code)
			}
		})
	}
}

// TestBundleOwnerIsDecidedByUserID shows that a new account reusing the
// username of the bundle's creator does not inherit the bundle.
func TestBundleOwnerIsDecidedByUserID(t *testing.T) {
	db := newAuthzDB()
	db.grant("ci-id", "src", "read")
	db.grant("ci-new-id", "src", "read")
	h := newAuthzHandler(t, db)

	original := authzUser("ci")
	expectStatus(t, "create", original.do(t, h, http.MethodPost, "/api/v1/bundles", `{"name":"b","version":"1"}`), http.StatusCreated)
	expectStatus(t, "add", original.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/items", authzItemBody(authzSrcID)), http.StatusCreated)

	reused := authzUser("ci")
	id := "ci-new-id"
	reused.p.UserID = &id

	expectStatus(
		t,
		"add by a user with the same name",
		reused.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/items", authzItemBody(authzSrcID)),
		http.StatusForbidden,
	)
	expectStatus(
		t,
		"seal by a user with the same name",
		reused.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/seal", ""),
		http.StatusForbidden,
	)
	expectStatus(t, "seal by the creator", original.do(t, h, http.MethodPost, "/api/v1/bundles/b/1/seal", ""), http.StatusOK)
}

// auditSink records audit rows.
type auditSink struct {
	mu   sync.Mutex
	rows []*models.AuditLog
}

func (s *auditSink) InsertAuditLog(_ context.Context, e *models.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rows = append(s.rows, e)

	return nil
}

func TestBundleLifecycleIsAudited(t *testing.T) {
	sink := &auditSink{}
	l := audit.NewLogger(sink, newFixTestLogger(t), nil, 64)
	audit.SetDefault(l)

	t.Cleanup(func() { audit.SetDefault(nil) })

	env := newSignerEnv(t, "KEYID=ci")

	expectStatus(t, "seal", env.seal(t, env.ci, env.key.ID), http.StatusOK)
	expectStatus(t, "archive", env.ci.do(t, env.h, http.MethodPost, "/api/v1/bundles/b/1/archive", ""), http.StatusOK)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := l.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()

	details := map[string]string{}
	for _, r := range sink.rows {
		details[r.Action] = string(r.DetailsJSON)
	}

	for _, action := range []string{audit.ActionBundleCreated, audit.ActionBundleSealed, audit.ActionBundleArchived} {
		if _, ok := details[action]; !ok {
			t.Errorf("no %s audit event; got %v", action, details)
		}
	}

	if !strings.Contains(details[audit.ActionBundleSealed], env.key.Fingerprint) {
		t.Errorf("sealed audit event lacks the signing fingerprint: %s", details[audit.ActionBundleSealed])
	}
}
