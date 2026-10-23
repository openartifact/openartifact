// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/gpgkeys"
	"github.com/openartifact/openartifact/internal/gpgkeys/gpgkeystest"
	"github.com/openartifact/openartifact/pkg/gpg"
)

type gpgAPIEnv struct {
	router chi.Router
	admin  *auth.Principal
	user   *auth.Principal
	store  *gpgkeystest.Store
	db     *MockDatabase
}

func setupGPGAPI(t *testing.T, withSecret bool) *gpgAPIEnv {
	t.Helper()

	if withSecret {
		if err := crypto.Init("gpg-api-test-secret-0123456789abcd"); err != nil {
			t.Fatalf("crypto.Init: %v", err)
		}

		t.Cleanup(func() { _ = crypto.Init("") })
	}

	db := newMockDatabase()
	log := newHandlerLogger(t)
	store := gpgkeystest.New()
	svc := gpgkeys.New(store, log, nil)

	router := chi.NewRouter()
	api.NewAdminGPGHandler(db, log, auth.NewEvaluator(db, log, false), svc).Register(router)

	ctx := context.Background()
	now := time.Now()

	mkUser := func(id, name string) *auth.Principal {
		if _, err := db.CreateUser(ctx, &models.User{ID: id, Username: name, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}

		uid := id

		return &auth.Principal{UserID: &uid, Username: name}
	}

	admin := mkUser("gpg-admin-1", "gpgadmin")
	if err := db.CreatePermissionAssignment(ctx, &models.PermissionAssignment{
		PermissionTargetID: "981948bd-de8f-5c65-b90b-23d72cf37aab", PrincipalType: "user", PrincipalID: *admin.UserID,
	}); err != nil {
		t.Fatalf("CreatePermissionAssignment: %v", err)
	}

	return &gpgAPIEnv{router: router, admin: admin, user: mkUser("gpg-user-1", "normaluser"), store: store, db: db}
}

func (e *gpgAPIEnv) do(t *testing.T, p *auth.Principal, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}

	req := httptest.NewRequestWithContext(context.Background(), method, path, rdr)
	req.Header.Set("Content-Type", "application/json")

	if p != nil {
		req = withPrincipal(req, p)
	}

	rw := httptest.NewRecorder()
	e.router.ServeHTTP(rw, req)

	return rw
}

func decodeBody[T any](t *testing.T, rw *httptest.ResponseRecorder) T {
	t.Helper()

	var v T
	if err := json.Unmarshal(rw.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rw.Body.String(), err)
	}

	return v
}

func TestAdminGPG_RBAC(t *testing.T) {
	e := setupGPGAPI(t, true)

	const id = "00000000-0000-4000-8000-000000000001"

	routes := []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/gpg/keys", ""},
		{"POST", "/api/v1/admin/gpg/keys", `{"private_key":"x"}`},
		{"POST", "/api/v1/admin/gpg/keys/generate", `{}`},
		{"DELETE", "/api/v1/admin/gpg/keys/" + id, ""},
		{"PUT", "/api/v1/admin/gpg/keys/" + id + "/activate", ""},
		{"DELETE", "/api/v1/admin/gpg/keys/" + id + "/activate", ""},
		{"GET", "/api/v1/admin/gpg/keys/" + id + "/export", ""},
	}

	for _, rt := range routes {
		for who, p := range map[string]*auth.Principal{"regular user": e.user, "anonymous": {IsAnonymous: true}} {
			rw := e.do(t, p, rt.method, rt.path, rt.body)
			if rw.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s: status %d, want 403 (%s)", rt.method, rt.path, who, rw.Code, rw.Body.String())
			}
		}
	}

	if e.store.Count() != 0 {
		t.Error("a forbidden request created a key")
	}
}

func TestAdminGPG_ListEmpty(t *testing.T) {
	e := setupGPGAPI(t, true)

	rw := e.do(t, e.admin, "GET", "/api/v1/admin/gpg/keys", "")
	if rw.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rw.Code, rw.Body.String())
	}

	resp := decodeBody[struct {
		Keys         []map[string]any `json:"keys"`
		CanStoreKeys bool             `json:"can_store_keys"`
	}](t, rw)

	if len(resp.Keys) != 0 || !resp.CanStoreKeys {
		t.Errorf("resp = %+v", resp)
	}
}

type gpgKeyResp struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Fingerprint  string   `json:"fingerprint"`
	Algorithm    string   `json:"algorithm"`
	Source       string   `json:"source"`
	ActiveGlobal bool     `json:"active_global"`
	Repositories []string `json:"repositories"`
}

func TestAdminGPG_Lifecycle(t *testing.T) {
	e := setupGPGAPI(t, true)

	rw := e.do(t, e.admin, "POST", "/api/v1/admin/gpg/keys/generate", `{"name":"release","algorithm":"ed25519","email":"rel@example.com"}`)
	if rw.Code != http.StatusCreated {
		t.Fatalf("generate: %d %s", rw.Code, rw.Body.String())
	}

	first := decodeBody[gpgKeyResp](t, rw)
	if first.Name != "release" || first.Algorithm != "ed25519" || first.Source != "generated" || len(first.Fingerprint) != 40 {
		t.Errorf("generated key = %+v", first)
	}

	if !first.ActiveGlobal {
		t.Error("the first key must become the global signer when nothing signs yet")
	}

	for _, secret := range []string{"PRIVATE KEY", "private_key_enc", "enc:v1:"} {
		if strings.Contains(rw.Body.String(), secret) {
			t.Errorf("generate response leaks %q: %s", secret, rw.Body.String())
		}
	}

	// A second key does not steal the global slot unless asked to.
	rw = e.do(t, e.admin, "POST", "/api/v1/admin/gpg/keys/generate", `{"name":"second"}`)
	second := decodeBody[gpgKeyResp](t, rw)

	if rw.Code != http.StatusCreated || second.ActiveGlobal {
		t.Fatalf("second generate: %d %+v", rw.Code, second)
	}

	// Name clash
	if rw = e.do(t, e.admin, "POST", "/api/v1/admin/gpg/keys/generate", `{"name":"second"}`); rw.Code != http.StatusConflict {
		t.Errorf("duplicate name: %d, want 409", rw.Code)
	}

	imported := importGPGKey(t, e)

	// Explicit activation moved the global slot.
	rw = e.do(t, e.admin, "GET", "/api/v1/admin/gpg/keys", "")

	list := decodeBody[struct {
		Keys []gpgKeyResp `json:"keys"`
	}](t, rw)

	active := 0

	for _, k := range list.Keys {
		if k.ActiveGlobal {
			active++
		}
	}

	if len(list.Keys) != 3 || active != 1 {
		t.Errorf("list = %+v (active=%d)", list.Keys, active)
	}

	if strings.Contains(rw.Body.String(), "PRIVATE KEY") || strings.Contains(rw.Body.String(), "enc:v1:") {
		t.Error("list leaks private key material")
	}

	checkGPGExportAndDelete(t, e, first, imported)
}

// importGPGKey imports a key generated elsewhere and activates it.
func importGPGKey(t *testing.T, e *gpgAPIEnv) gpgKeyResp {
	t.Helper()

	ent, err := gpg.GenerateEntity("Elsewhere", "", "else@example.com", gpg.AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	signer, _ := gpg.NewLocalSignerFromEntity(ent)
	priv, _ := signer.ExportPrivateKey()

	importBody, _ := json.Marshal(map[string]any{"name": "imported", "private_key": string(priv), "activate": true})

	rw := e.do(t, e.admin, "POST", "/api/v1/admin/gpg/keys", string(importBody))
	if rw.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", rw.Code, rw.Body.String())
	}

	imported := decodeBody[gpgKeyResp](t, rw)
	if imported.Fingerprint != signer.KeyFingerprint() || !imported.ActiveGlobal || imported.Source != "imported" {
		t.Errorf("imported = %+v", imported)
	}

	if bytes.Contains(rw.Body.Bytes(), []byte("PRIVATE KEY")) {
		t.Error("import response leaks the private key")
	}

	return imported
}

func checkGPGExportAndDelete(t *testing.T, e *gpgAPIEnv, first, imported gpgKeyResp) {
	t.Helper()

	// Export is the armored public key and is audited as a download.
	rw := e.do(t, e.admin, "GET", "/api/v1/admin/gpg/keys/"+imported.ID+"/export", "")
	if rw.Code != http.StatusOK || !strings.HasPrefix(rw.Header().Get("Content-Type"), "application/pgp-keys") ||
		!strings.Contains(rw.Body.String(), "BEGIN PGP PUBLIC KEY BLOCK") || strings.Contains(rw.Body.String(), "PRIVATE") {
		t.Errorf("export: %d %s %.60q", rw.Code, rw.Header().Get("Content-Type"), rw.Body.String())
	}

	// Deleting an active key needs force.
	rw = e.do(t, e.admin, "DELETE", "/api/v1/admin/gpg/keys/"+imported.ID, "")
	if rw.Code != http.StatusConflict || !strings.Contains(rw.Body.String(), "key_in_use") {
		t.Errorf("delete active: %d %s", rw.Code, rw.Body.String())
	}

	if rw = e.do(t, e.admin, "DELETE", "/api/v1/admin/gpg/keys/"+imported.ID+"/activate", ""); rw.Code != http.StatusNoContent {
		t.Errorf("deactivate: %d %s", rw.Code, rw.Body.String())
	}

	if rw = e.do(t, e.admin, "DELETE", "/api/v1/admin/gpg/keys/"+imported.ID, ""); rw.Code != http.StatusNoContent {
		t.Errorf("delete inactive: %d %s", rw.Code, rw.Body.String())
	}

	if rw = e.do(t, e.admin, "DELETE", "/api/v1/admin/gpg/keys/"+imported.ID, ""); rw.Code != http.StatusNotFound {
		t.Errorf("delete twice: %d, want 404", rw.Code)
	}

	if rw = e.do(t, e.admin, "DELETE", "/api/v1/admin/gpg/keys/not-a-uuid", ""); rw.Code != http.StatusNotFound {
		t.Errorf("bad id: %d, want 404", rw.Code)
	}

	if rw = e.do(t, e.admin, "DELETE", "/api/v1/admin/gpg/keys/"+first.ID+"?force=true", ""); rw.Code != http.StatusNoContent {
		t.Errorf("force delete: %d %s", rw.Code, rw.Body.String())
	}
}

func TestAdminGPG_RepositoryScope(t *testing.T) {
	e := setupGPGAPI(t, true)
	ctx := context.Background()

	for name, format := range map[string]string{"apt-hosted": "deb", "yum-hosted": "rpm", "maven-hosted": "maven"} {
		repo := &models.Repository{ID: "repo-" + name, Name: name, Format: format, RepoType: "hosted"}
		if err := e.db.CreateRepository(ctx, repo); err != nil {
			t.Fatal(err)
		}

		e.store.AddRepository(repo.ID, name, format)
	}

	proxy := &models.Repository{ID: "repo-apt-proxy", Name: "apt-proxy", Format: "deb", RepoType: "proxy"}
	if err := e.db.CreateRepository(ctx, proxy); err != nil {
		t.Fatal(err)
	}

	rw := e.do(t, e.admin, "POST", "/api/v1/admin/gpg/keys/generate", `{"name":"scoped","activate":false}`)
	if rw.Code != http.StatusCreated {
		t.Fatalf("generate: %d %s", rw.Code, rw.Body.String())
	}

	id := decodeBody[struct {
		ID string `json:"id"`
	}](t, rw).ID
	activate := "/api/v1/admin/gpg/keys/" + id + "/activate"

	for _, bad := range []string{"maven-hosted", "apt-proxy"} {
		if rw = e.do(t, e.admin, "PUT", activate, `{"repository":"`+bad+`"}`); rw.Code != http.StatusBadRequest {
			t.Errorf("activate for %s: %d, want 400 (%s)", bad, rw.Code, rw.Body.String())
		}
	}

	if rw = e.do(t, e.admin, "PUT", activate, `{"repository":"ghost"}`); rw.Code != http.StatusNotFound {
		t.Errorf("unknown repository: %d, want 404", rw.Code)
	}

	if rw = e.do(t, e.admin, "PUT", activate, `{"repository":"apt-hosted"}`); rw.Code != http.StatusOK {
		t.Fatalf("activate for a deb repository: %d %s", rw.Code, rw.Body.String())
	}

	assigned := decodeBody[struct {
		Repositories []string `json:"repositories"`
		ActiveGlobal bool     `json:"active_global"`
	}](t, rw)
	if len(assigned.Repositories) != 1 || assigned.Repositories[0] != "apt-hosted" || assigned.ActiveGlobal {
		t.Errorf("after repository activation: %+v", assigned)
	}

	if rw = e.do(t, e.admin, "DELETE", "/api/v1/admin/gpg/keys/"+id, ""); rw.Code != http.StatusConflict {
		t.Errorf("delete of an assigned key: %d, want 409", rw.Code)
	}

	if rw = e.do(t, e.admin, "DELETE", activate+"?repository=yum-hosted", ""); rw.Code != http.StatusConflict {
		t.Errorf("deactivate where it is not active: %d, want 409", rw.Code)
	}

	if rw = e.do(t, e.admin, "DELETE", activate+"?repository=apt-hosted", ""); rw.Code != http.StatusNoContent {
		t.Errorf("deactivate: %d %s", rw.Code, rw.Body.String())
	}
}

func TestAdminGPG_RequiresSecretKeyToStore(t *testing.T) {
	e := setupGPGAPI(t, false)

	rw := e.do(t, e.admin, "GET", "/api/v1/admin/gpg/keys", "")
	if !strings.Contains(rw.Body.String(), `"can_store_keys":false`) {
		t.Errorf("list must report can_store_keys=false: %s", rw.Body.String())
	}

	for _, rt := range []struct{ path, body string }{
		{"/api/v1/admin/gpg/keys/generate", `{}`},
		{"/api/v1/admin/gpg/keys", `{"private_key":"-----BEGIN PGP PRIVATE KEY BLOCK-----\nx\n-----END PGP PRIVATE KEY BLOCK-----"}`},
	} {
		rw = e.do(t, e.admin, "POST", rt.path, rt.body)
		if rw.Code != http.StatusConflict || !strings.Contains(rw.Body.String(), "secret_key_required") {
			t.Errorf("POST %s: %d %s", rt.path, rw.Code, rw.Body.String())
		}
	}

	if e.store.Count() != 0 {
		t.Error("a key was stored without OPENARTIFACT_SECRET_KEY")
	}
}

func TestAdminGPG_ImportValidation(t *testing.T) {
	e := setupGPGAPI(t, true)

	ent, err := gpg.GenerateEntity("Prot", "", "", gpg.AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	if err := ent.PrivateKey.Encrypt([]byte("pw")); err != nil {
		t.Fatal(err)
	}

	for _, sub := range ent.Subkeys {
		_ = sub.PrivateKey.Encrypt([]byte("pw"))
	}

	var buf bytes.Buffer
	armored := armorPrivate(t, &buf, ent)

	cases := []struct {
		name, body, code string
		status           int
	}{
		{"empty key", `{"private_key":"  "}`, "validation_failed", 400},
		{"not JSON", `nope`, "bad_request", 400},
		{"garbage", `{"private_key":"garbage"}`, "validation_failed", 400},
		{"needs passphrase", gpgJSON(map[string]any{"private_key": armored}), "passphrase_required", 400},
		{"wrong passphrase", gpgJSON(map[string]any{"private_key": armored, "passphrase": "x"}), "validation_failed", 400},
		{"name too long", `{"private_key":"x","name":"` + strings.Repeat("n", 300) + `"}`, "validation_failed", 400},
	}

	for _, c := range cases {
		rw := e.do(t, e.admin, "POST", "/api/v1/admin/gpg/keys", c.body)
		if rw.Code != c.status || !strings.Contains(rw.Body.String(), c.code) {
			t.Errorf("%s: %d %s", c.name, rw.Code, rw.Body.String())
		}

		if strings.Contains(rw.Body.String(), "BEGIN PGP PRIVATE") {
			t.Errorf("%s: error echoes the private key", c.name)
		}
	}

	if rw := e.do(t, e.admin, "POST", "/api/v1/admin/gpg/keys", gpgJSON(map[string]any{"private_key": armored, "passphrase": "pw"})); rw.Code != http.StatusCreated {
		t.Errorf("right passphrase: %d %s", rw.Code, rw.Body.String())
	}

	for name, body := range map[string]string{
		"algorithm":  `{"algorithm":"dsa"}`,
		"user_name":  `{"user_name":"a<b"}`,
		"email":      `{"email":"x\u0000y"}`,
		"name":       `{"name":"` + strings.Repeat("n", 300) + `"}`,
		"user field": `{"comment":"(nested)"}`,
	} {
		if rw := e.do(t, e.admin, "POST", "/api/v1/admin/gpg/keys/generate", body); rw.Code != http.StatusBadRequest {
			t.Errorf("generate with bad %s: %d %s", name, rw.Code, rw.Body.String())
		}
	}
}

func gpgJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	return string(b)
}

// armorPrivate serialises a (possibly passphrase-protected) entity without re-signing it.
func armorPrivate(t *testing.T, buf *bytes.Buffer, e interface {
	SerializePrivateWithoutSigning(w io.Writer, cfg *packet.Config) error
}) string {
	t.Helper()

	w, err := armor.Encode(buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := e.SerializePrivateWithoutSigning(w, nil); err != nil {
		t.Fatal(err)
	}

	_ = w.Close()

	return buf.String()
}
