// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
)

// memDB is an in-memory nuget_packages table with the semantics of the real
// queries: lookups match the full (id, version) key, creating a key twice
// yields database.ErrDuplicate, and a search pages over distinct package ids
// returning every version of the packages on the page.
type memDB struct {
	*formattest.DB
	quotaFake

	pkgs []*models.NuGetPackage
	// raceCreate makes GetNuGetPackage report "absent" even when the row
	// exists, to exercise the unique-index backstop of a concurrent push.
	raceCreate bool
	// groupWrites counts SetNuGetDependencyGroups calls that stored a value.
	groupWrites int
}

func (d *memDB) find(id, version string) *models.NuGetPackage {
	for _, p := range d.pkgs {
		if p.PackageID == id && p.Version == version {
			return p
		}
	}

	return nil
}

func (d *memDB) CreateNuGetPackage(_ context.Context, p *models.NuGetPackage) error {
	if d.find(p.PackageID, p.Version) != nil {
		return database.ErrDuplicate
	}

	p.ID = fmt.Sprintf("nuget-%d", len(d.pkgs)+1)
	p.CreatedAt = time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(len(d.pkgs)) * time.Minute)
	d.pkgs = append(d.pkgs, p)

	return nil
}

func (d *memDB) GetNuGetPackage(_ context.Context, _, id, version string) (*models.NuGetPackage, error) {
	if d.raceCreate {
		return nil, nil
	}

	return d.find(id, version), nil
}

// SetNuGetDependencyGroups fills only a row that has none, like the real query.
func (d *memDB) SetNuGetDependencyGroups(_ context.Context, _, id, version string, groups []byte) error {
	if p := d.find(id, version); p != nil && p.DependencyGroups == nil {
		p.DependencyGroups = groups
		d.groupWrites++
	}

	return nil
}

func (d *memDB) DeleteNuGetPackage(_ context.Context, _, id, version string) error {
	d.pkgs = slices.DeleteFunc(d.pkgs, func(p *models.NuGetPackage) bool { return p.PackageID == id && p.Version == version })
	return nil
}

func (d *memDB) ListNuGetPackageVersions(_ context.Context, _, id string) ([]*models.NuGetPackage, error) {
	var out []*models.NuGetPackage

	for _, p := range d.pkgs {
		if p.PackageID == id {
			out = append(out, p)
		}
	}

	return out, nil
}

func (d *memDB) IncrementNuGetDownloadCount(_ context.Context, _, id, version string) error {
	if p := d.find(id, version); p != nil {
		p.DownloadCount++
	}

	return nil
}

func (d *memDB) BlobReferenceCount(_ context.Context, sha string) (int, error) {
	n := 0

	for _, p := range d.pkgs {
		if p.BlobKey == sha {
			n++
		}
	}

	return n, nil
}

func (d *memDB) SearchNuGetPackages(_ context.Context, _, query string, skip, take int) ([]*models.NuGetPackage, int, error) {
	var ids []string

	for _, p := range d.pkgs {
		if strings.Contains(strings.ToLower(p.PackageID+" "+p.Title+" "+p.Description+" "+p.Tags), strings.ToLower(query)) && !slices.Contains(ids, p.PackageID) {
			ids = append(ids, p.PackageID)
		}
	}

	slices.Sort(ids)

	total := len(ids)
	ids = ids[min(skip, total):min(skip+take, total)]

	var out []*models.NuGetPackage

	for _, id := range ids {
		for _, p := range d.pkgs {
			if p.PackageID == id {
				out = append(out, p)
			}
		}
	}

	return out, total, nil
}

func newV3Handler(t *testing.T) (*HTTPHandler, *memDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &memDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

// newHandlerOnly returns a handler for tests that need none of its fakes.
func newHandlerOnly(t *testing.T) *HTTPHandler {
	t.Helper()

	return NewHTTPHandler(formattest.NewLogger(t), &memDB{DB: formattest.NewDB()}, formattest.NewBlobStore(), "http://localhost")
}

func nuspecFor(id, version, description string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<package>
  <metadata>
    <id>%s</id>
    <version>%s</version>
    <authors>Contoso</authors>
    <description>%s</description>
    <dependencies>
      <group targetFramework="net8.0">
        <dependency id="Other.Lib" version="[1.0.0, )" />
      </group>
    </dependencies>
  </metadata>
</package>`, id, version, description)
}

func nupkgFor(t *testing.T, id, version, description string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, err := zw.Create(id + ".nuspec")
	if err != nil {
		t.Fatal(err)
	}

	_, _ = w.Write([]byte(nuspecFor(id, version, description)))

	// A payload so the .nupkg differs clearly from its nuspec.
	pw, _ := zw.Create("lib/net8.0/" + id + ".dll")
	_, _ = pw.Write([]byte("MZ-" + version))
	_ = zw.Close()

	return buf.Bytes()
}

func push(t *testing.T, h *HTTPHandler, id, version, description string) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("package", "x.nupkg")
	_, _ = fw.Write(nupkgFor(t, id, version, description))
	_ = mw.Close()

	req := formattest.Request(http.MethodPut, "/repos/myrepo/v3/package", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return formattest.Do(register(h), req)
}

func mustPush(t *testing.T, h *HTTPHandler, id, version string) {
	t.Helper()

	if w := push(t, h, id, version, "desc "+id); w.Code != http.StatusCreated {
		t.Fatalf("push %s %s = %d: %s", id, version, w.Code, w.Body.String())
	}
}

// get issues a GET against an absolute URL the server advertised (the host is
// dropped: the handler is mounted on a test router).
func get(h *HTTPHandler, rawURL string) *httptest.ResponseRecorder {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}

	return formattest.Do(register(h), formattest.Request(http.MethodGet, u.RequestURI(), nil))
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("not JSON: %v: %s", err, w.Body.String())
	}
}

// resource returns the @id of the first service index resource of a type.
func resource(t *testing.T, h *HTTPHandler, typePrefix string) string {
	t.Helper()

	var index struct {
		Resources []struct {
			ID   string `json:"@id"`
			Type string `json:"@type"`
		} `json:"resources"`
	}

	decode(t, get(h, "http://localhost/repos/myrepo/v3/index.json"), &index)

	for _, r := range index.Resources {
		if strings.HasPrefix(r.Type, typePrefix) {
			return r.ID
		}
	}

	t.Fatalf("service index has no %s resource", typePrefix)

	return ""
}

// NU-1 and NU-3: `dotnet restore` reads the service index, lists versions at
// {PackageBaseAddress}/{id}/index.json and downloads
// {PackageBaseAddress}/{id}/{normalized version}/{id}.{normalized version}.nupkg
// (all lower case). Before the fix the version list was a 404, and a package
// pushed as 1.0.0.0 was stored under that spelling so the download URL built
// from the listed version never matched.
func TestRestoreSequence(t *testing.T) {
	h := newHandlerOnly(t)

	// Spellings a nuspec may use; each normalizes to the version on the right.
	pushed := map[string]string{
		"1.0.0.0":      "1.0.0",
		"1.5.0+meta":   "1.5.0",
		"2.0-Beta":     "2.0.0-beta",
		"01.2.3":       "1.2.3",
		"3.0.0.1-RC.1": "3.0.0.1-rc.1",
	}

	for v := range pushed {
		mustPush(t, h, "Contoso.Lib", v)
	}

	base := resource(t, h, "PackageBaseAddress/3.0.0")

	// 1. Version list (case-insensitive id).
	for _, id := range []string{"contoso.lib", "Contoso.Lib"} {
		var list struct {
			Versions []string `json:"versions"`
		}

		decode(t, get(h, base+id+"/index.json"), &list)

		want := []string{"1.0.0", "1.2.3", "1.5.0", "2.0.0-beta", "3.0.0.1-rc.1"}
		if !slices.Equal(list.Versions, want) {
			t.Fatalf("%s: versions = %v, want %v", id, list.Versions, want)
		}

		// 2. Download every listed version at the URL the client builds.
		for _, v := range list.Versions {
			u := fmt.Sprintf("%scontoso.lib/%s/contoso.lib.%s.nupkg", base, v, v)

			w := get(h, u)
			if w.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", u, w.Code)
			}

			if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); err != nil {
				t.Errorf("GET %s did not return the nupkg: %v", u, err)
			}

			if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
				t.Errorf("content type = %q", ct)
			}
		}
	}

	// A client may also ask with the spelling it was told in a dependency range.
	for _, u := range []string{
		base + "contoso.lib/1.0.0.0/contoso.lib.1.0.0.0.nupkg",
		base + "Contoso.Lib/2.0.0-Beta/Contoso.Lib.2.0.0-Beta.nupkg",
	} {
		if w := get(h, u); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", u, w.Code)
		}
	}

	// 3. The nuspec endpoint (used by some restore flows and tools).
	w := get(h, base+"contoso.lib/1.0.0/contoso.lib.nuspec")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<id>Contoso.Lib</id>") {
		t.Errorf("nuspec = %d %q", w.Code, w.Body.String())
	}

	// Unknown package, version and file name.
	for _, u := range []string{
		base + "nope/index.json",
		base + "contoso.lib/9.9.9/contoso.lib.9.9.9.nupkg",
		base + "contoso.lib/1.0.0/other.1.0.0.nupkg",
		base + "contoso.lib/1.0.0/contoso.lib.zip",
	} {
		if w := get(h, u); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", u, w.Code)
		}
	}
}

func TestPushStoresNormalizedVersion(t *testing.T) {
	h, db, _, pub := newV3Handler(t)

	mustPush(t, h, "Contoso.Lib", "1.0.0.0-Beta+abc")

	p := db.pkgs[0]
	if p.PackageID != "contoso.lib" || p.Version != "1.0.0-beta" {
		t.Fatalf("stored %q %q, want contoso.lib 1.0.0-beta", p.PackageID, p.Version)
	}

	if a := pub.Events()[0].Artifact(); a["version"] != "1.0.0-beta" || a["path"] != "contoso.lib.1.0.0-beta.nupkg" {
		t.Errorf("artifact = %v", a)
	}

	// The same version spelled differently is the same version.
	if w := push(t, h, "Contoso.Lib", "1.0-BETA", "other"); w.Code != http.StatusConflict {
		t.Errorf("re-push as 1.0-BETA = %d, want 409", w.Code)
	}

	// Delete by any spelling.
	w := formattest.Do(register(h), formattest.Request(http.MethodDelete, "/repos/myrepo/v3/package/Contoso.Lib/1.0.0.0-Beta", nil))
	if w.Code != http.StatusNoContent || len(db.pkgs) != 0 {
		t.Errorf("delete = %d, rows = %d", w.Code, len(db.pkgs))
	}
}

func TestPushRejectsInvalidVersion(t *testing.T) {
	h, db, bs, _ := newV3Handler(t)

	for _, v := range []string{"not a version", "1.0.0.0.0", "1..0"} {
		if w := push(t, h, "Contoso.Lib", v, "d"); w.Code != http.StatusBadRequest {
			t.Errorf("push version %q = %d, want 400", v, w.Code)
		}
	}

	if len(db.pkgs) != 0 || len(bs.Blobs) != 0 {
		t.Errorf("rejected push stored rows=%d blobs=%d", len(db.pkgs), len(bs.Blobs))
	}
}

// NU-4: a second push of an existing id/version answered 201 while the row
// kept the first content (ON CONFLICT DO UPDATE kept it but the new blob was
// stored and referenced by nothing). It is now 409 and leaves everything as it
// was.
func TestDuplicatePushConflict(t *testing.T) {
	h, db, bs, pub := newV3Handler(t)

	mustPush(t, h, "Contoso.Lib", "1.0.0")

	first := db.pkgs[0]
	blobs := len(bs.Blobs)

	// Different content, same coordinates.
	if w := push(t, h, "Contoso.Lib", "1.0.0", "a different description"); w.Code != http.StatusConflict {
		t.Fatalf("duplicate push = %d, want 409", w.Code)
	}

	// Identical content.
	if w := push(t, h, "Contoso.Lib", "1.0.0", "desc Contoso.Lib"); w.Code != http.StatusConflict {
		t.Fatalf("identical duplicate push = %d, want 409", w.Code)
	}

	if len(db.pkgs) != 1 || db.pkgs[0] != first {
		t.Error("duplicate push changed the stored package")
	}

	if len(bs.Blobs) != blobs {
		t.Errorf("blobs = %d, want %d: the duplicate's content was stored", len(bs.Blobs), blobs)
	}

	if _, ok := bs.Blobs[first.BlobKey]; !ok {
		t.Error("the original blob is gone")
	}

	if len(pub.Events()) != 1 {
		t.Errorf("events = %d, want 1", len(pub.Events()))
	}
}

// Two pushes of the same version racing past the existence check: the unique
// index rejects the loser, which gets 409 and must not leave its blob behind
// (the winner has a different blob).
func TestConcurrentDuplicatePushConflict(t *testing.T) {
	h, db, bs, pub := newV3Handler(t)

	mustPush(t, h, "Contoso.Lib", "1.0.0")

	winnerBlob := db.pkgs[0].BlobKey
	db.raceCreate = true

	if w := push(t, h, "Contoso.Lib", "1.0.0", "the loser"); w.Code != http.StatusConflict {
		t.Fatalf("racing duplicate push = %d, want 409", w.Code)
	}

	if _, ok := bs.Blobs[winnerBlob]; !ok || len(bs.Blobs) != 1 {
		t.Errorf("blobs = %d (winner present: %v), want only the winner's", len(bs.Blobs), ok)
	}

	if len(pub.Events()) != 1 {
		t.Errorf("events = %d, want 1", len(pub.Events()))
	}
}

type registrationDoc struct {
	ID    string             `json:"@id"`
	Count int                `json:"count"`
	Items []registrationPage `json:"items"`
}

type registrationPage struct {
	ID     string             `json:"@id"`
	Count  int                `json:"count"`
	Lower  string             `json:"lower"`
	Upper  string             `json:"upper"`
	Parent string             `json:"parent"`
	Items  []registrationLeaf `json:"items"`
}

type registrationLeaf struct {
	ID           string            `json:"@id"`
	Registration string            `json:"registration"`
	Content      string            `json:"packageContent"` //nolint:tagliatelle // NuGet V3 wire format is camelCase
	Entry        registrationEntry `json:"catalogEntry"`   //nolint:tagliatelle // NuGet V3 wire format is camelCase
}

type registrationEntry struct {
	ID        string `json:"@id"`
	Version   string `json:"version"`
	Listed    *bool  `json:"listed"`
	Published string `json:"published"`
	Content   string `json:"packageContent"` //nolint:tagliatelle // NuGet V3 wire format is camelCase
	Deps      []struct {
		TargetFramework string `json:"targetFramework"` //nolint:tagliatelle // NuGet V3 wire format is camelCase
	} `json:"dependencyGroups"` //nolint:tagliatelle // NuGet V3 wire format is camelCase
}

// checkRegistrationLeaf verifies one registration item and returns its version.
func checkRegistrationLeaf(t *testing.T, leaf registrationLeaf, indexURL string) string {
	t.Helper()

	wantLeaf := "http://localhost/repos/myrepo/v3/registration/contoso.lib/" + leaf.Entry.Version + ".json"
	if leaf.ID != wantLeaf || leaf.Entry.ID != wantLeaf {
		t.Errorf("leaf @id = %q, catalogEntry @id = %q, want %q", leaf.ID, leaf.Entry.ID, wantLeaf)
	}

	wantContent := fmt.Sprintf("http://localhost/repos/myrepo/v3/package/contoso.lib/%[1]s/contoso.lib.%[1]s.nupkg", leaf.Entry.Version)
	if leaf.Content != wantContent || leaf.Entry.Content != wantContent {
		t.Errorf("packageContent = %q / %q, want %q", leaf.Content, leaf.Entry.Content, wantContent)
	}

	if leaf.Registration != indexURL {
		t.Errorf("registration = %q", leaf.Registration)
	}

	if leaf.Entry.Listed == nil || !*leaf.Entry.Listed {
		t.Errorf("catalogEntry.listed = %v, want true", leaf.Entry.Listed)
	}

	if _, err := time.Parse(time.RFC3339, leaf.Entry.Published); err != nil {
		t.Errorf("catalogEntry.published = %q", leaf.Entry.Published)
	}

	if len(leaf.Entry.Deps) != 1 || leaf.Entry.Deps[0].TargetFramework != "net8.0" {
		t.Errorf("dependencyGroups = %+v", leaf.Entry.Deps)
	}

	return leaf.Entry.Version
}

// checkLeafDocument follows a leaf @id and verifies the leaf document.
func checkLeafDocument(t *testing.T, h *HTTPHandler, item registrationLeaf, indexURL string) {
	t.Helper()

	var leaf struct {
		ID           string `json:"@id"`
		Type         string `json:"@type"`
		CatalogEntry string `json:"catalogEntry"` //nolint:tagliatelle // NuGet V3 wire format is camelCase
		Listed       bool   `json:"listed"`
		Content      string `json:"packageContent"` //nolint:tagliatelle // NuGet V3 wire format is camelCase
		Registration string `json:"registration"`
	}

	decode(t, get(h, item.ID), &leaf)

	if leaf.ID != item.ID || leaf.Type != "Package" || leaf.CatalogEntry == "" || !leaf.Listed ||
		leaf.Content != item.Content || leaf.Registration != indexURL {
		t.Errorf("leaf = %+v", leaf)
	}
}

// NU-2: NuGet clients read @id/lower/upper on pages and @id, listed and
// catalogEntry.@id on leaves, and follow the leaf @id to {version}.json.
func TestRegistrationIndexShape(t *testing.T) {
	h := newHandlerOnly(t)

	for _, v := range []string{"2.0.0", "1.0.0", "1.5.0-beta"} {
		mustPush(t, h, "Contoso.Lib", v)
	}

	reg := resource(t, h, "RegistrationsBaseUrl")

	var doc registrationDoc

	decode(t, get(h, reg+"contoso.lib/index.json"), &doc)

	const indexURL = "http://localhost/repos/myrepo/v3/registration/contoso.lib/index.json"
	if doc.ID != indexURL || doc.Count != 1 || len(doc.Items) != 1 {
		t.Fatalf("index = %+v", doc)
	}

	page := doc.Items[0]
	if page.ID != indexURL+"#page/1.0.0/2.0.0" || page.Lower != "1.0.0" || page.Upper != "2.0.0" || page.Count != 3 || page.Parent != indexURL {
		t.Errorf("page = id %q lower %q upper %q count %d parent %q", page.ID, page.Lower, page.Upper, page.Count, page.Parent)
	}

	var versions []string

	for _, leaf := range page.Items {
		versions = append(versions, checkRegistrationLeaf(t, leaf, indexURL))
	}

	if !slices.Equal(versions, []string{"1.0.0", "1.5.0-beta", "2.0.0"}) {
		t.Errorf("versions = %v, want ascending semver order", versions)
	}

	checkLeafDocument(t, h, page.Items[1], indexURL)

	// The leaf route also accepts other spellings of the version.
	if w := get(h, reg+"Contoso.Lib/1.0.0.0.json"); w.Code != http.StatusOK {
		t.Errorf("leaf 1.0.0.0.json = %d", w.Code)
	}

	for _, u := range []string{reg + "contoso.lib/9.9.9.json", reg + "contoso.lib/1.0.0", reg + "nope/index.json"} {
		if w := get(h, u); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", u, w.Code)
		}
	}
}

// Registration pages hold at most registrationPageSize versions; each page
// carries its own bounds.
func TestRegistrationPaging(t *testing.T) {
	h := newHandlerOnly(t)

	const total = registrationPageSize + 6

	for i := range total {
		mustPush(t, h, "Contoso.Lib", fmt.Sprintf("1.%d.0", i))
	}

	var doc registrationDoc

	decode(t, get(h, "http://localhost/repos/myrepo/v3/registration/contoso.lib/index.json"), &doc)

	if doc.Count != 2 || len(doc.Items) != 2 {
		t.Fatalf("pages = %d", doc.Count)
	}

	first, second := doc.Items[0], doc.Items[1]
	if first.Count != registrationPageSize || first.Lower != "1.0.0" || first.Upper != fmt.Sprintf("1.%d.0", registrationPageSize-1) {
		t.Errorf("first page = count %d %s..%s", first.Count, first.Lower, first.Upper)
	}

	if second.Count != 6 || second.Lower != fmt.Sprintf("1.%d.0", registrationPageSize) || second.Upper != fmt.Sprintf("1.%d.0", total-1) {
		t.Errorf("second page = count %d %s..%s", second.Count, second.Lower, second.Upper)
	}

	if first.ID == second.ID {
		t.Errorf("pages share @id %q", first.ID)
	}
}

type searchDoc struct {
	TotalHits int         `json:"totalHits"` //nolint:tagliatelle // NuGet V3 wire format is camelCase
	Data      []searchHit `json:"data"`
}

type searchHit struct {
	ID             string `json:"@id"`
	Registration   string `json:"registration"`
	PackageID      string `json:"id"`
	Version        string `json:"version"`
	TotalDownloads int    `json:"totalDownloads"` //nolint:tagliatelle // NuGet V3 wire format is camelCase
	Versions       []struct {
		ID        string `json:"@id"`
		Version   string `json:"version"`
		Downloads int    `json:"downloads"`
	} `json:"versions"`
}

// checkContosoEntry verifies the search entry of contoso.lib (1.0.0 downloaded
// twice, 1.5.0 once).
func checkContosoEntry(t *testing.T, contoso searchHit) {
	t.Helper()

	// Latest is the highest stable version, not the highest prerelease.
	if contoso.Version != "1.5.0" {
		t.Errorf("version = %q, want 1.5.0", contoso.Version)
	}

	if len(contoso.Versions) != 3 {
		t.Fatalf("versions = %+v, want 3", contoso.Versions)
	}

	var got []string
	for _, v := range contoso.Versions {
		got = append(got, v.Version)
	}

	if !slices.Equal(got, []string{"1.0.0", "1.5.0", "2.0.0-beta"}) {
		t.Errorf("versions = %v", got)
	}

	if contoso.Versions[0].Downloads != 2 || contoso.TotalDownloads != 3 {
		t.Errorf("downloads = %d (1.0.0), total %d; want 2 and 3", contoso.Versions[0].Downloads, contoso.TotalDownloads)
	}

	if contoso.Registration != "http://localhost/repos/myrepo/v3/registration/contoso.lib/index.json" ||
		contoso.Versions[1].ID != "http://localhost/repos/myrepo/v3/registration/contoso.lib/1.5.0.json" {
		t.Errorf("registration = %q, version @id = %q", contoso.Registration, contoso.Versions[1].ID)
	}
}

// NU-5: search returned one row per version and totalHits counted versions;
// clients expect one entry per package with a versions array.
func TestSearchGroupsVersionsPerPackage(t *testing.T) {
	h := newHandlerOnly(t)

	for _, v := range []string{"1.0.0", "1.5.0", "2.0.0-beta"} {
		mustPush(t, h, "Contoso.Lib", v)
	}

	mustPush(t, h, "Fabrikam.Core", "4.2.0")

	// Count some downloads.
	for _, p := range []string{
		"contoso.lib/1.0.0/contoso.lib.1.0.0.nupkg", "contoso.lib/1.0.0/contoso.lib.1.0.0.nupkg", "contoso.lib/1.5.0/contoso.lib.1.5.0.nupkg",
	} {
		if w := get(h, "http://localhost/repos/myrepo/v3/package/"+p); w.Code != http.StatusOK {
			t.Fatalf("download %s = %d", p, w.Code)
		}
	}

	query := resource(t, h, "SearchQueryService")

	var doc searchDoc

	decode(t, get(h, query+"?q="), &doc)

	if doc.TotalHits != 2 || len(doc.Data) != 2 {
		t.Fatalf("totalHits = %d, entries = %d; want 2 packages", doc.TotalHits, len(doc.Data))
	}

	contoso := doc.Data[0]
	if contoso.PackageID != "contoso.lib" {
		t.Fatalf("first entry = %q", contoso.PackageID)
	}

	checkContosoEntry(t, contoso)

	// Paging counts packages: take=1 yields one entry holding all its versions.
	doc = searchDoc{}
	decode(t, get(h, query+"?q=&take=1&skip=1"), &doc)

	if doc.TotalHits != 2 || len(doc.Data) != 1 || doc.Data[0].PackageID != "fabrikam.core" || len(doc.Data[0].Versions) != 1 {
		t.Errorf("page 2 = %+v", doc)
	}

	// A prerelease-only package reports its highest prerelease.
	mustPush(t, h, "Preview.Only", "0.1.0-alpha")
	mustPush(t, h, "Preview.Only", "0.2.0-alpha")

	doc = searchDoc{}
	decode(t, get(h, query+"?q=preview"), &doc)

	if len(doc.Data) != 1 || doc.Data[0].Version != "0.2.0-alpha" {
		t.Errorf("prerelease-only search = %+v", doc)
	}
}

func TestAutocompleteListsPackagesOnce(t *testing.T) {
	h := newHandlerOnly(t)

	for _, v := range []string{"1.0.0", "1.5.0", "2.0.0"} {
		mustPush(t, h, "Contoso.Lib", v)
	}

	mustPush(t, h, "Fabrikam.Core", "4.2.0")

	var doc struct {
		TotalHits int      `json:"totalHits"` //nolint:tagliatelle // NuGet V3 wire format is camelCase
		Data      []string `json:"data"`
	}

	decode(t, get(h, resource(t, h, "SearchAutocompleteService")+"?q="), &doc)

	if doc.TotalHits != 2 || !slices.Equal(doc.Data, []string{"contoso.lib", "fabrikam.core"}) {
		t.Errorf("autocomplete = %+v", doc)
	}
}
