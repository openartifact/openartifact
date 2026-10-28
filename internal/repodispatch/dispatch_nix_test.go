// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package repodispatch

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	formatnix "github.com/openartifact/openartifact/internal/format/nix"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/proxy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
	"github.com/openartifact/openartifact/pkg/nix"
)

const nixTestHash = "0123456789abcdfghijklmnpqrsvwxyz"

// nixDB serves the stored paths of the hosted nix repository.
type nixDB struct {
	*fakeDB

	paths map[string]*models.NixPath // by repository ID + store hash
}

func (d *nixDB) GetNixPathByHash(_ context.Context, repoID, hash string) (*models.NixPath, error) {
	return d.paths[repoID+"|"+hash], nil
}

// nixKeyFor answers a fixed key per repository name.
type nixKeyFor map[string]*nix.SecretKey

func (k nixKeyFor) SecretKeyForRepo(_ context.Context, repo *models.Repository) (*nix.SecretKey, error) {
	return k[repo.Name], nil
}

func newNixKey(t *testing.T, name string) *nix.SecretKey {
	t.Helper()

	key, err := nix.GenerateKey(name)
	if err != nil {
		t.Fatal(err)
	}

	return key
}

func verifiesNixSig(t *testing.T, publicKey, sig string, n *nix.NarInfo) bool {
	t.Helper()

	pkName, pkB64, _ := strings.Cut(publicKey, ":")
	sigName, sigB64, _ := strings.Cut(sig, ":")

	pub, err := base64.StdEncoding.DecodeString(pkB64)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatal(err)
	}

	fp, err := n.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}

	return pkName == sigName && ed25519.Verify(pub, []byte(fp), raw)
}

// A nix group over a hosted repository (the real hosted handler, behind the
// hosted router the dispatcher reads members through) signs the member's
// narinfo with the group's own key and publishes that key.
func TestNixGroupSignsHostedNarinfoThroughTheDispatcher(t *testing.T) {
	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	store, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	repo := func(id, name, typ string) *models.Repository {
		return &models.Repository{ID: id, Name: name, Format: "nix", RepoType: typ, Public: true}
	}

	var narHash nix.SHA256

	narHash[0] = 7

	db := &nixDB{
		fakeDB: &fakeDB{
			repos:   map[string]*models.Repository{"local": repo("1", "local", "hosted"), "all": repo("2", "all", "group")},
			links:   map[string][]*models.RepositoryGroupMember{"2": {{MemberRepoID: "1", Priority: 0}}},
			entries: map[string]*models.ProxyCacheEntry{},
		},
		paths: map[string]*models.NixPath{"1|" + nixTestHash: {
			ID: "p1", RepositoryID: "1", StoreHash: nixTestHash, Name: "hello-2.12", NarHash: narHash.String(), NarSize: 4000,
			NarFile: narHash.Base32() + ".nar.xz", Compression: "xz", SizeBytes: 1000, SHA256: strings.Repeat("ab", 32),
		}},
	}

	keys := nixKeyFor{"local": newNixKey(t, "local-1"), "all": newNixKey(t, "all-1")}

	hostedRouter := chi.NewRouter()
	formatnix.NewHTTPHandler(log, db, store, "http://oa.test").WithKeys(keys).RegisterRepoRoutes(hostedRouter)

	d := New(log, db, proxy.NewService(log, db, store), denyAuthorizer{}, map[string]http.Handler{"nix": hostedRouter}).WithNixKeys(keys)

	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) {
		r.Use(d.Middleware)
		r.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "format-handler") })
	})

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{IsAnonymous: true}))

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		return w
	}

	key := get("/repos/all/public.key")
	if key.Code != http.StatusOK || key.Body.String() != keys["all"].PublicKey()+"\n" {
		t.Fatalf("public.key = %d %q, want the group's key %q", key.Code, key.Body, keys["all"].PublicKey())
	}

	w := get("/repos/all/" + nixTestHash + ".narinfo")
	if w.Code != http.StatusOK {
		t.Fatalf("narinfo = %d %s", w.Code, w.Body)
	}

	n, err := nix.ParseNarInfo(w.Body.Bytes())
	if err != nil {
		t.Fatalf("%v\n%s", err, w.Body)
	}

	if len(n.Sigs) != 2 || !verifiesNixSig(t, keys["local"].PublicKey(), n.Sigs[0], n) ||
		!verifiesNixSig(t, keys["all"].PublicKey(), n.Sigs[1], n) {
		t.Errorf("signatures = %v, want the member's and the group's", n.Sigs)
	}

	if w := get("/repos/all/" + strings.Repeat("1", 32) + ".narinfo"); w.Code != http.StatusNotFound {
		t.Errorf("unknown narinfo = %d, want 404", w.Code)
	}

	if w := get("/repos/all/nix-cache-info"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "StoreDir: /nix/store") {
		t.Errorf("nix-cache-info = %d %q", w.Code, w.Body)
	}
}
