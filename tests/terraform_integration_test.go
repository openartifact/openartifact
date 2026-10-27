// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/format/terraform"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/gpg"
)

// tfSigner resolves one fixed signing key for every repository.
type tfSigner struct{ signer gpg.Signer }

func (s tfSigner) SignerForRepo(context.Context, string) (gpg.Signer, error) { return s.signer, nil }

func tfZip(t *testing.T, entries ...[2]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// tfServer is the terraform handler over a real database, mounted the way
// cmd/server does: the repository routes under /repos/{name}, discovery and
// the ticketed download on the root.
type tfServer struct {
	t      *testing.T
	router chi.Router
}

func (s *tfServer) do(method, target string, body []byte) *httptest.ResponseRecorder {
	s.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, target, bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	return w
}

func (s *tfServer) must(method, target string, body []byte, want int) *httptest.ResponseRecorder {
	s.t.Helper()

	w := s.do(method, target, body)
	if w.Code != want {
		s.t.Fatalf("%s %s = %d, want %d: %s", method, target, w.Code, want, w.Body.String())
	}

	return w
}

func newTerraformServer(t *testing.T, pg *database.PostgresDB) *tfServer {
	t.Helper()

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	entity, err := gpg.GenerateEntity("registry", "", "registry@example.com", gpg.AlgorithmED25519)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := gpg.NewLocalSignerFromEntity(entity)
	if err != nil {
		t.Fatal(err)
	}

	h := terraform.NewHTTPHandler(newTestLogger(t), pg, store, "http://localhost").
		WithSigner(tfSigner{signer}).
		WithTicketKey([]byte("integration-test-secret-0123456789"))

	router := chi.NewRouter()
	h.RegisterPublicRoutes(router)
	router.Route("/repos/{name}", func(r chi.Router) {
		r.Use(h.LatestGuard)
		h.RegisterRepoRoutes(r)
	})

	return &tfServer{t: t, router: router}
}

// TestTerraformModuleAndProviderRoundTrip drives the registry the way a
// Terraform client and a publisher do against the real queries: publish a
// module and a provider, list, resolve, download through the signed URLs and
// delete.
func TestTerraformModuleAndProviderRoundTrip(t *testing.T) {
	pg := freshPostgres(t, 5)
	repoID := newRepo(t, pg, "tf", "terraform")
	srv := newTerraformServer(t, pg)

	module := tfZip(t, [2]string{"main.tf", `variable "x" {}`})

	srv.must(http.MethodPut, "/repos/tf/_api/modules/vpc/aws/1.0.0", module, http.StatusCreated)
	srv.must(http.MethodPut, "/repos/tf/_api/modules/vpc/aws/1.0.0", module, http.StatusConflict)
	srv.must(http.MethodPut, "/repos/tf/_api/modules/vpc/aws/1.1.0", tfZip(t, [2]string{"main.tf", "# 1.1"}), http.StatusCreated)

	if got := srv.must(http.MethodGet, "/repos/tf/vpc/aws/versions", nil, http.StatusOK).Body.String(); !strings.Contains(got, `"1.0.0"`) ||
		!strings.Contains(got, `"1.1.0"`) {
		t.Fatalf("versions = %s", got)
	}

	if got := srv.must(http.MethodGet, "/repos/tf/vpc/aws", nil, http.StatusOK).Body.String(); !strings.Contains(got, `"version":"1.1.0"`) {
		t.Fatalf("latest = %s", got)
	}

	mint := srv.must(http.MethodGet, "/repos/tf/vpc/aws/1.0.0/download", nil, http.StatusNoContent)

	link := strings.TrimPrefix(mint.Header().Get("X-Terraform-Get"), "http://localhost")
	if got := srv.must(http.MethodGet, link, nil, http.StatusOK).Body.Bytes(); !bytes.Equal(got, module) {
		t.Fatalf("ticket download returned %d bytes, want %d", len(got), len(module))
	}

	srv.must(http.MethodDelete, "/repos/tf/_api/modules/vpc/aws/1.0.0", nil, http.StatusOK)
	srv.must(http.MethodGet, "/repos/tf/vpc/aws/1.0.0", nil, http.StatusNotFound)
	srv.must(http.MethodGet, link, nil, http.StatusNotFound)

	provider := tfZip(t, [2]string{"terraform-provider-aws_v1.2.3", "binary"})
	sum := sha256.Sum256(provider)

	srv.must(http.MethodPut, "/repos/tf/_api/providers/aws/1.2.3/linux/amd64?protocols=5.0,6.0", provider, http.StatusCreated)
	srv.must(http.MethodPut, "/repos/tf/_api/providers/aws/1.2.3/linux/amd64", provider, http.StatusConflict)
	srv.must(
		http.MethodPut,
		"/repos/tf/_api/providers/aws/1.2.3/darwin/arm64",
		tfZip(t, [2]string{"terraform-provider-aws_v1.2.3", "b2"}),
		http.StatusCreated,
	)

	versions := srv.must(http.MethodGet, "/repos/tf/aws/versions", nil, http.StatusOK)
	if !strings.Contains(versions.Body.String(), `"darwin"`) || !strings.Contains(versions.Body.String(), `"6.0"`) {
		t.Fatalf("provider versions = %s", versions.Body)
	}

	var pkg struct {
		Shasum string `json:"shasum"`
		Sums   string `json:"shasums_url"`
		Sig    string `json:"shasums_signature_url"`
	}

	desc := srv.must(http.MethodGet, "/repos/tf/aws/1.2.3/download/linux/amd64", nil, http.StatusOK)
	if err := json.Unmarshal(desc.Body.Bytes(), &pkg); err != nil || pkg.Shasum != hex.EncodeToString(sum[:]) {
		t.Fatalf("package description = %s (%v)", desc.Body, err)
	}

	sums := srv.must(http.MethodGet, strings.TrimPrefix(pkg.Sums, "http://localhost"), nil, http.StatusOK)
	if strings.Count(sums.Body.String(), "\n") != 2 ||
		!strings.Contains(sums.Body.String(), hex.EncodeToString(sum[:])+"  terraform-provider-aws_1.2.3_linux_amd64.zip") {
		t.Fatalf("SHA256SUMS = %q", sums.Body)
	}

	srv.must(http.MethodGet, strings.TrimPrefix(pkg.Sig, "http://localhost"), nil, http.StatusOK)

	srv.must(http.MethodDelete, "/repos/tf/_api/providers/aws/1.2.3/darwin/arm64", nil, http.StatusOK)
	srv.must(http.MethodDelete, "/repos/tf/_api/providers/aws/1.2.3", nil, http.StatusOK)
	srv.must(http.MethodGet, "/repos/tf/aws/1.2.3/download/linux/amd64", nil, http.StatusNotFound)

	if mods, _ := pg.ListTerraformModuleVersions(context.Background(), repoID, "vpc", "aws"); len(mods) != 1 {
		t.Errorf("module rows after deleting 1.0.0 = %d, want 1 (1.1.0)", len(mods))
	}
}
