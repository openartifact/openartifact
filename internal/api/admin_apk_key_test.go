// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/apkkeys"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/apk"
	"github.com/openartifact/openartifact/pkg/logger"
)

type apkKeyMockDB struct {
	databasetest.BaseMockDB

	repos map[string]*models.Repository
	keys  map[string]*models.ApkSigningKey
}

func (m *apkKeyMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return m.repos[name], nil
}

func (m *apkKeyMockDB) GetApkSigningKey(_ context.Context, id string) (*models.ApkSigningKey, error) {
	return m.keys[id], nil
}

func (m *apkKeyMockDB) CreateApkSigningKeyIfAbsent(_ context.Context, k *models.ApkSigningKey) (bool, error) {
	if _, ok := m.keys[k.RepositoryID]; ok {
		return false, nil
	}

	m.keys[k.RepositoryID] = k

	return true, nil
}

func (m *apkKeyMockDB) ReplaceApkSigningKey(_ context.Context, k *models.ApkSigningKey) error {
	m.keys[k.RepositoryID] = k
	return nil
}

func apkKeyRequest(t *testing.T, db *apkKeyMockDB, principal *auth.Principal, method, repo, body string) *httptest.ResponseRecorder {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(middleware.WithPrincipal(req.Context(), principal)))
		})
	})
	NewApkKeyHandler(db, log, nil, apkkeys.New(db, log)).Register(r)

	req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/repositories/"+repo+"/apk-key", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	return rec
}

func TestApkKeyAPI(t *testing.T) {
	admin := &auth.Principal{IsLegacy: true}
	newDB := func() *apkKeyMockDB {
		return &apkKeyMockDB{
			repos: map[string]*models.Repository{
				"hosted": {ID: "h1", Name: "hosted", Format: "apk", RepoType: "hosted"},
				"group":  {ID: "g1", Name: "group", Format: "apk", RepoType: "group"},
				"proxy":  {ID: "p1", Name: "proxy", Format: "apk", RepoType: "proxy"},
				"debs":   {ID: "d1", Name: "debs", Format: "deb", RepoType: "hosted"},
			},
			keys: map[string]*models.ApkSigningKey{},
		}
	}

	t.Run("without OPENARTIFACT_SECRET_KEY", func(t *testing.T) {
		rec := apkKeyRequest(t, newDB(), admin, http.MethodGet, "hosted", "")
		if rec.Code != http.StatusConflict || errorCode(t, rec) != "secret_key_required" {
			t.Fatalf("status %d code %q: %s", rec.Code, errorCode(t, rec), rec.Body)
		}
	})

	if err := crypto.Init(strings.Repeat("s", 32)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	t.Run("rejections", func(t *testing.T) {
		db := newDB()

		for name, tc := range map[string]struct {
			method, repo, body string
			status             int
		}{
			"proxy has no key":            {http.MethodGet, "proxy", "", http.StatusBadRequest},
			"other format":                {http.MethodGet, "debs", "", http.StatusBadRequest},
			"unknown repository":          {http.MethodGet, "nope", "", http.StatusNotFound},
			"key name without a key":      {http.MethodPost, "hosted", `{"key_name":"x.rsa.pub"}`, http.StatusBadRequest},
			"invalid private key":         {http.MethodPost, "hosted", `{"private_key":"junk"}`, http.StatusBadRequest},
			"invalid json":                {http.MethodPost, "hosted", `{`, http.StatusBadRequest},
			"invalid key name on import":  {http.MethodPost, "hosted", importBody(t, "../x.rsa.pub"), http.StatusBadRequest},
			"group repositories have one": {http.MethodGet, "group", "", http.StatusOK},
		} {
			if rec := apkKeyRequest(t, db, admin, tc.method, tc.repo, tc.body); rec.Code != tc.status {
				t.Errorf("%s: status %d, want %d: %s", name, rec.Code, tc.status, rec.Body)
			}
		}
	})

	t.Run("anonymous callers are refused", func(t *testing.T) {
		rec := apkKeyRequest(t, newDB(), &auth.Principal{IsAnonymous: true}, http.MethodGet, "hosted", "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("status %d, want 403", rec.Code)
		}
	})

	t.Run("import then read", func(t *testing.T) {
		db := newDB()

		rec := apkKeyRequest(t, db, admin, http.MethodPost, "hosted", importBody(t, "ci@example.rsa.pub"))
		if rec.Code != http.StatusOK {
			t.Fatalf("import: status %d: %s", rec.Code, rec.Body)
		}

		if strings.Contains(rec.Body.String(), "PRIVATE") {
			t.Fatal("the response leaks the private key")
		}

		var got apkKeyResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}

		if got.KeyName != "ci@example.rsa.pub" || got.Fingerprint == "" || !strings.Contains(got.PublicKey, "BEGIN PUBLIC KEY") {
			t.Errorf("response = %+v", got)
		}

		again := apkKeyRequest(t, db, admin, http.MethodGet, "hosted", "")

		var read apkKeyResponse
		if err := json.Unmarshal(again.Body.Bytes(), &read); err != nil || read.Fingerprint != got.Fingerprint {
			t.Errorf("GET after import = %s (%v)", again.Body, err)
		}
	})
}

func importBody(t *testing.T, keyName string) string {
	t.Helper()

	key, err := apk.GenerateKey(2048)
	if err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(map[string]string{"private_key": string(apk.MarshalPrivateKeyPEM(key)), "key_name": keyName})
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}
