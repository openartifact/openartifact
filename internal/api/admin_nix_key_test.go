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
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/nixkeys"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/openartifact/openartifact/pkg/nix"
)

type nixKeyMockDB struct {
	repos map[string]*models.Repository
	keys  map[string]*models.NixSigningKey
}

func (m *nixKeyMockDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	return m.repos[name], nil
}

func (m *nixKeyMockDB) GetNixSigningKey(_ context.Context, id string) (*models.NixSigningKey, error) {
	return m.keys[id], nil
}

func (m *nixKeyMockDB) CreateNixSigningKeyIfAbsent(_ context.Context, k *models.NixSigningKey) (bool, error) {
	if _, ok := m.keys[k.RepositoryID]; ok {
		return false, nil
	}

	m.keys[k.RepositoryID] = k

	return true, nil
}

func (m *nixKeyMockDB) ReplaceNixSigningKey(_ context.Context, k *models.NixSigningKey) error {
	m.keys[k.RepositoryID] = k
	return nil
}

func nixKeyRequest(t *testing.T, db *nixKeyMockDB, principal *auth.Principal, method, repo, body string) *httptest.ResponseRecorder {
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
	NewNixKeyHandler(db, log, nil, nixkeys.New(db, log)).Register(r)

	req := httptest.NewRequestWithContext(context.Background(), method, "/api/v1/repositories/"+repo+"/nix-key", strings.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	return rec
}

func nixImportBody(t *testing.T, keyName, importName string) string {
	t.Helper()

	key, err := nix.GenerateKey(keyName)
	if err != nil {
		t.Fatal(err)
	}

	b, err := json.Marshal(map[string]string{"private_key": key.String(), "key_name": importName})
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func decodeNixKey(t *testing.T, rec *httptest.ResponseRecorder) nixKeyResponse {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var got nixKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if got.PublicKey == "" || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("incomplete response: %s", rec.Body)
	}

	for field := range mustFields(t, rec.Body.Bytes()) {
		if strings.Contains(field, "private") {
			t.Errorf("the response leaks %q", field)
		}
	}

	return got
}

func mustFields(t *testing.T, b []byte) map[string]json.RawMessage {
	t.Helper()

	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}

	return m
}

func newNixKeyDB() *nixKeyMockDB {
	return &nixKeyMockDB{
		repos: map[string]*models.Repository{
			"hosted": {ID: "h1", Name: "hosted", Format: "nix", RepoType: "hosted"},
			"group":  {ID: "g1", Name: "group", Format: "nix", RepoType: "group"},
			"proxy":  {ID: "p1", Name: "proxy", Format: "nix", RepoType: "proxy"},
			"debs":   {ID: "d1", Name: "debs", Format: "deb", RepoType: "hosted"},
		},
		keys: map[string]*models.NixSigningKey{},
	}
}

func TestNixKeyAPI(t *testing.T) {
	admin := &auth.Principal{IsLegacy: true}
	newDB := newNixKeyDB

	t.Run("without OPENARTIFACT_SECRET_KEY", func(t *testing.T) {
		rec := nixKeyRequest(t, newDB(), admin, http.MethodGet, "hosted", "")
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
			"proxy has no key":           {http.MethodGet, "proxy", "", http.StatusBadRequest},
			"other format":               {http.MethodGet, "debs", "", http.StatusBadRequest},
			"unknown repository":         {http.MethodGet, "nope", "", http.StatusNotFound},
			"key name without a key":     {http.MethodPost, "hosted", `{"key_name":"x-1"}`, http.StatusBadRequest},
			"invalid private key":        {http.MethodPost, "hosted", `{"private_key":"junk"}`, http.StatusBadRequest},
			"invalid json":               {http.MethodPost, "hosted", `{`, http.StatusBadRequest},
			"invalid key name on import": {http.MethodPost, "hosted", nixImportBody(t, "k-1", "../x"), http.StatusBadRequest},
		} {
			if rec := nixKeyRequest(t, db, admin, tc.method, tc.repo, tc.body); rec.Code != tc.status {
				t.Errorf("%s: status %d, want %d: %s", name, rec.Code, tc.status, rec.Body)
			}
		}

		if len(db.keys) != 0 {
			t.Error("rejected requests must store nothing")
		}
	})

	t.Run("anonymous callers are refused", func(t *testing.T) {
		rec := nixKeyRequest(t, newDB(), &auth.Principal{IsAnonymous: true}, http.MethodGet, "hosted", "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("status %d, want 403", rec.Code)
		}
	})

	t.Run("read generates, empty post rotates", func(t *testing.T) {
		db := newDB()

		first := decodeNixKey(t, nixKeyRequest(t, db, admin, http.MethodGet, "hosted", ""))
		if first.KeyName != "openartifact-hosted-1" {
			t.Errorf("first key = %+v", first)
		}

		if again := decodeNixKey(t, nixKeyRequest(t, db, admin, http.MethodGet, "hosted", "")); again.PublicKey != first.PublicKey {
			t.Error("a second read must return the same key")
		}

		rotated := decodeNixKey(t, nixKeyRequest(t, db, admin, http.MethodPost, "hosted", ""))
		if rotated.KeyName != "openartifact-hosted-2" || rotated.PublicKey == first.PublicKey {
			t.Errorf("rotated key = %+v", rotated)
		}
	})

	t.Run("import then read", func(t *testing.T) {
		db := newDB()

		got := decodeNixKey(t, nixKeyRequest(t, db, admin, http.MethodPost, "hosted", nixImportBody(t, "k-1", "ci.example.org-1")))
		if got.KeyName != "ci.example.org-1" || !strings.HasPrefix(got.PublicKey, "ci.example.org-1:") {
			t.Errorf("response = %+v", got)
		}

		if read := decodeNixKey(t, nixKeyRequest(t, db, admin, http.MethodGet, "hosted", "")); read.PublicKey != got.PublicKey {
			t.Errorf("GET after import = %+v", read)
		}
	})
}

func TestNixKeyAPIGroupHasAKeyOfItsOwn(t *testing.T) {
	if err := crypto.Init(strings.Repeat("s", 32)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = crypto.Init("") })

	admin := &auth.Principal{IsLegacy: true}
	db := newNixKeyDB()

	hosted := decodeNixKey(t, nixKeyRequest(t, db, admin, http.MethodGet, "hosted", ""))

	first := decodeNixKey(t, nixKeyRequest(t, db, admin, http.MethodGet, "group", ""))
	if first.KeyName != "openartifact-group-1" || first.PublicKey == hosted.PublicKey {
		t.Errorf("group key = %+v, hosted key = %+v", first, hosted)
	}

	rotated := decodeNixKey(t, nixKeyRequest(t, db, admin, http.MethodPost, "group", ""))
	if rotated.KeyName != "openartifact-group-2" || rotated.PublicKey == first.PublicKey {
		t.Errorf("rotated group key = %+v", rotated)
	}

	if db.keys["g1"] == nil || db.keys["h1"] == nil {
		t.Error("the group and the hosted repository must each store a key")
	}
}
