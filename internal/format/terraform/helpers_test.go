// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/pkg/gpg"
)

// quotaFake counts storage usage like the real reservation: limit 0 means
// unlimited, a reservation that does not fit changes nothing.
type quotaFake struct {
	limit     int64
	used      int64
	artifacts int
}

func (q *quotaFake) ReserveStorageUsage(_ context.Context, _ string, size int64, count int) (bool, error) {
	if q.limit > 0 && q.used+size > q.limit {
		return false, nil
	}

	q.used += size
	q.artifacts += count

	return true, nil
}

func (q *quotaFake) DecrementStorageUsage(_ context.Context, _ string, size int64, count int) error {
	q.used -= size
	q.artifacts -= count

	return nil
}

// tfDB is an in-memory terraform_modules and terraform_providers table with
// the semantics of the real ones: one live row per coordinates (a second one is
// a unique violation), oldest first listings and stamps that change with the
// set of rows.
type tfDB struct {
	*formattest.DB
	quotaFake

	mu    sync.Mutex
	mods  []*models.TerraformModule
	provs []*models.TerraformProvider
	next  int
	clock time.Time
}

func newTFDB() *tfDB {
	d := &tfDB{DB: formattest.NewDB(), clock: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	d.Repo.Format = formatName

	return d
}

func (d *tfDB) stamp(ids []string) string {
	if len(ids) == 0 {
		return "0:0"
	}

	return fmt.Sprintf("%d:%s", len(ids), strings.Join(ids, ","))
}

func (d *tfDB) nextRow(created time.Time) (string, time.Time) {
	d.next++
	d.clock = d.clock.Add(time.Second)

	if created.IsZero() {
		created = d.clock
	}

	return fmt.Sprintf("row-%d", d.next), created
}

func (d *tfDB) GetRepositoryByID(_ context.Context, id string) (*models.Repository, error) {
	if d.Repo != nil && d.Repo.ID == id {
		return d.Repo, nil
	}

	return nil, nil
}

func (d *tfDB) CreateTerraformModule(_ context.Context, m *models.TerraformModule) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, row := range d.mods {
		if row.RepositoryID == m.RepositoryID && row.Name == m.Name && row.System == m.System && row.Version == m.Version {
			return fmt.Errorf("create: %w", database.ErrDuplicate)
		}
	}

	m.ID, m.CreatedAt = d.nextRow(m.CreatedAt)
	m.UpdatedAt = d.clock
	cp := *m
	d.mods = append(d.mods, &cp)

	return nil
}

func (d *tfDB) GetTerraformModule(_ context.Context, repoID, name, system, version string) (*models.TerraformModule, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, row := range d.mods {
		if row.RepositoryID == repoID && row.Name == name && row.System == system && row.Version == version {
			cp := *row

			return &cp, nil
		}
	}

	return nil, nil
}

func (d *tfDB) ListTerraformModuleVersions(_ context.Context, repoID, name, system string) ([]*models.TerraformModule, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []*models.TerraformModule

	for _, row := range d.mods {
		if row.RepositoryID == repoID && row.Name == name && row.System == system {
			cp := *row
			out = append(out, &cp)
		}
	}

	return out, nil
}

func (d *tfDB) ListTerraformModules(_ context.Context, repoID string) ([]*models.TerraformModule, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []*models.TerraformModule

	for _, row := range d.mods {
		if row.RepositoryID == repoID {
			cp := *row
			out = append(out, &cp)
		}
	}

	return out, nil
}

func (d *tfDB) DeleteTerraformModule(_ context.Context, repoID, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i, row := range d.mods {
		if row.RepositoryID == repoID && row.ID == id {
			d.mods = append(d.mods[:i], d.mods[i+1:]...)

			return nil
		}
	}

	return nil
}

func (d *tfDB) IncrementTerraformModuleDownloadCount(_ context.Context, repoID, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, row := range d.mods {
		if row.RepositoryID == repoID && row.ID == id {
			row.DownloadCount++
		}
	}

	return nil
}

func (d *tfDB) TerraformModuleStamp(_ context.Context, repoID, name, system string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var ids []string

	for _, row := range d.mods {
		if row.RepositoryID == repoID && row.Name == name && row.System == system {
			ids = append(ids, row.ID)
		}
	}

	return d.stamp(ids), nil
}

func (d *tfDB) CreateTerraformProvider(_ context.Context, p *models.TerraformProvider) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, row := range d.provs {
		if row.RepositoryID == p.RepositoryID && row.Type == p.Type && row.Version == p.Version && row.OS == p.OS && row.Arch == p.Arch {
			return fmt.Errorf("create: %w", database.ErrDuplicate)
		}
	}

	p.ID, p.CreatedAt = d.nextRow(p.CreatedAt)
	p.UpdatedAt = d.clock
	cp := *p
	d.provs = append(d.provs, &cp)

	return nil
}

func (d *tfDB) GetTerraformProvider(_ context.Context, repoID, typ, version, osName, arch string) (*models.TerraformProvider, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, row := range d.provs {
		if row.RepositoryID == repoID && row.Type == typ && row.Version == version && row.OS == osName && row.Arch == arch {
			cp := *row

			return &cp, nil
		}
	}

	return nil, nil
}

func (d *tfDB) listProviders(repoID string, keep func(*models.TerraformProvider) bool) []*models.TerraformProvider {
	d.mu.Lock()
	defer d.mu.Unlock()

	var out []*models.TerraformProvider

	for _, row := range d.provs {
		if row.RepositoryID == repoID && keep(row) {
			cp := *row
			out = append(out, &cp)
		}
	}

	return out
}

func (d *tfDB) ListTerraformProviderPlatforms(_ context.Context, repoID, typ string) ([]*models.TerraformProvider, error) {
	return d.listProviders(repoID, func(p *models.TerraformProvider) bool { return p.Type == typ }), nil
}

func (d *tfDB) ListTerraformProviderVersionPlatforms(_ context.Context, repoID, typ, version string) ([]*models.TerraformProvider, error) {
	return d.listProviders(repoID, func(p *models.TerraformProvider) bool { return p.Type == typ && p.Version == version }), nil
}

func (d *tfDB) ListTerraformProviders(_ context.Context, repoID string) ([]*models.TerraformProvider, error) {
	return d.listProviders(repoID, func(*models.TerraformProvider) bool { return true }), nil
}

func (d *tfDB) DeleteTerraformProvider(_ context.Context, repoID, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for i, row := range d.provs {
		if row.RepositoryID == repoID && row.ID == id {
			d.provs = append(d.provs[:i], d.provs[i+1:]...)

			return nil
		}
	}

	return nil
}

func (d *tfDB) IncrementTerraformProviderDownloadCount(_ context.Context, repoID, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, row := range d.provs {
		if row.RepositoryID == repoID && row.ID == id {
			row.DownloadCount++
		}
	}

	return nil
}

func (d *tfDB) TerraformProviderStamp(_ context.Context, repoID, typ string) (string, error) {
	var ids []string

	for _, p := range d.listProviders(repoID, func(p *models.TerraformProvider) bool { return p.Type == typ }) {
		ids = append(ids, p.ID)
	}

	return d.stamp(ids), nil
}

// ---- archives ----

// zipOf builds a zip from name/content pairs; a name ending in "/" is a directory.
func zipOf(tb testing.TB, entries ...[2]string) []byte {
	tb.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, e := range entries {
		w, err := zw.Create(e[0])
		if err != nil {
			tb.Fatal(err)
		}

		if _, err := io.WriteString(w, e[1]); err != nil {
			tb.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

// tarEntry is one entry of tarGzOf.
type tarEntry struct {
	name     string
	body     string
	typeflag byte
	link     string
}

func tarGzOf(tb testing.TB, entries ...tarEntry) []byte {
	tb.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for _, e := range entries {
		flag := e.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}

		hdr := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: flag, Linkname: e.link}
		if flag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}

		if err := tw.WriteHeader(hdr); err != nil {
			tb.Fatal(err)
		}

		if flag == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				tb.Fatal(err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		tb.Fatal(err)
	}

	if err := gz.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

func moduleZip(tb testing.TB) []byte {
	tb.Helper()

	return zipOf(tb, [2]string{"main.tf", `resource "null_resource" "x" {}`}, [2]string{"README.md", "# vpc"})
}

func providerZip(tb testing.TB, typ, version string, extra ...[2]string) []byte {
	tb.Helper()

	entries := [][2]string{{"terraform-provider-" + typ + "_v" + version, "\x7fELF-binary"}, {"LICENSE", "MPL"}}

	return zipOf(tb, append(entries, extra...)...)
}

// ---- environment ----

type env struct {
	t   *testing.T
	h   *HTTPHandler
	db  *tfDB
	bs  *formattest.BlobStore
	pub *formattest.Publisher
	now time.Time
}

const testSecret = "0123456789abcdef" + "0123456789abcdef"

func newEnv(t *testing.T) *env {
	t.Helper()

	db := newTFDB()
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	e := &env{t: t, db: db, bs: bs, pub: pub, now: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)}
	e.h = NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://registry.test").WithTicketKey([]byte(testSecret))
	e.h.now = func() time.Time { return e.now }
	e.h.SetPublisher(pub)

	return e
}

// router mounts the handler the way the server does: /repos/{name} with the
// latest guard, and the public routes on the bare router.
func (e *env) router() chi.Router {
	root := chi.NewRouter()
	e.h.RegisterPublicRoutes(root)
	root.Route("/repos/{name}", func(r chi.Router) {
		r.Use(e.h.LatestGuard)
		e.h.RegisterRepoRoutes(r)
	})

	return root
}

// do sends a request as an authenticated (non-anonymous) principal.
func (e *env) do(method, target string, body io.Reader, headers ...string) *httptest.ResponseRecorder {
	return e.doAs(&auth.Principal{Username: "ci", Credential: auth.CredentialNone}, method, target, body, headers...)
}

func (e *env) doAs(p *auth.Principal, method, target string, body io.Reader, headers ...string) *httptest.ResponseRecorder {
	req := formattest.Request(method, target, body)
	req = req.WithContext(middleware.WithPrincipal(req.Context(), p))

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	w := httptest.NewRecorder()
	e.router().ServeHTTP(w, req)

	return w
}

func (e *env) putModule(name, system, version string, archive []byte) *httptest.ResponseRecorder {
	return e.do(http.MethodPut, "/repos/myrepo/_api/modules/"+name+"/"+system+"/"+version, bytes.NewReader(archive))
}

func (e *env) putProvider(typ, version, osName, arch string, archive []byte, query string, headers ...string) *httptest.ResponseRecorder {
	target := "/repos/myrepo/_api/providers/" + typ + "/" + version + "/" + osName + "/" + arch + query

	return e.do(http.MethodPut, target, bytes.NewReader(archive), headers...)
}

// signerResolver returns one fixed signer.
type signerResolver struct{ signer gpg.Signer }

func (s signerResolver) SignerForRepo(context.Context, string) (gpg.Signer, error) {
	return s.signer, nil
}

// fataler is the part of testing.TB newSigner needs. Fuzz targets pass their
// *testing.F, and the OSS-Fuzz Go build substitutes its own testing.F, which
// does not implement the full testing.TB of newer toolchains.
type fataler interface {
	Helper()
	Fatal(args ...any)
}

func newSigner(tb fataler) *gpg.LocalSigner {
	tb.Helper()

	entity, err := gpg.GenerateEntity("registry", "", "registry@example.com", gpg.AlgorithmED25519)
	if err != nil {
		tb.Fatal(err)
	}

	signer, err := gpg.NewLocalSignerFromEntity(entity)
	if err != nil {
		tb.Fatal(err)
	}

	return signer
}
