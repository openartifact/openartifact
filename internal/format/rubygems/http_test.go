// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rubygems

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/licensepolicy/policytest"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/rubymarshal"
)

var errCreateFailed = errors.New("create failed")

type gemsDB struct {
	*formattest.DB

	gems      []*models.RubyGem
	createErr error

	limit     int64 // 0 = unlimited
	used      int64
	artifacts int
}

func (d *gemsDB) CreateRubyGem(_ context.Context, g *models.RubyGem) error {
	if d.createErr != nil {
		return d.createErr
	}

	for _, e := range d.gems {
		if e.Name == g.Name && e.Version == g.Version && e.Platform == g.Platform {
			return database.ErrDuplicate
		}
	}

	g.ID = "gem-1"
	d.gems = append(d.gems, g)

	return nil
}

func (d *gemsDB) GetRubyGem(_ context.Context, _, name, version, platform string) (*models.RubyGem, error) {
	for _, g := range d.gems {
		if g.Name == name && g.Version == version && g.Platform == platform {
			return g, nil
		}
	}

	return nil, nil
}

func (d *gemsDB) ListRubyGems(_ context.Context, _ string) ([]*models.RubyGem, error) {
	return d.gems, nil
}

// ListRubyGemSpecs mirrors the real query: listed gems only.
func (d *gemsDB) ListRubyGemSpecs(_ context.Context, _ string) ([]*models.RubyGem, error) {
	var out []*models.RubyGem

	for _, g := range d.gems {
		if !g.Yanked {
			out = append(out, g)
		}
	}

	return out, nil
}

func (d *gemsDB) RubyGemsSpecsStamp(_ context.Context, _ string) (string, error) {
	var b strings.Builder

	for _, g := range d.gems {
		if !g.Yanked {
			b.WriteString(g.Name + "@" + g.Version + "@" + g.Platform + ";")
		}
	}

	return b.String(), nil
}

func (d *gemsDB) ListRubyGemVersions(_ context.Context, _, name string) ([]*models.RubyGem, error) {
	var out []*models.RubyGem

	for _, g := range d.gems {
		if g.Name == name {
			out = append(out, g)
		}
	}

	return out, nil
}

func (d *gemsDB) YankRubyGem(_ context.Context, _, name, version, _ string, yanked bool) error {
	for _, g := range d.gems {
		if g.Name == name && g.Version == version {
			g.Yanked = yanked
		}
	}

	return nil
}

func (d *gemsDB) ReserveStorageUsage(_ context.Context, _ string, size int64, count int) (bool, error) {
	if d.limit > 0 && d.used+size > d.limit {
		return false, nil
	}

	d.used += size
	d.artifacts += count

	return true, nil
}

func (d *gemsDB) DecrementStorageUsage(_ context.Context, _ string, size int64, count int) error {
	d.used -= size
	d.artifacts -= count

	return nil
}

func newTestHandler(t *testing.T) (*HTTPHandler, *gemsDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &gemsDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

func push(t *testing.T, h *HTTPHandler, name, version string) (int, []byte) {
	t.Helper()

	gem := createGemFile(t, map[string]any{"name": name, "version": version})
	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPost, "/repos/myrepo/api/v1/gems", bytes.NewReader(gem)))

	return w.Code, gem
}

func TestPushReservesQuotaStoresSizeAndEmitsEvent(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	code, gem := push(t, h, "demo", "1.0.0")
	if code != http.StatusCreated {
		t.Fatalf("push status = %d", code)
	}

	if db.used != int64(len(gem)) || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want %d/1", db.used, db.artifacts, len(gem))
	}

	if len(db.gems) != 1 || db.gems[0].Size != int64(len(gem)) {
		t.Fatalf("gem row size = %+v, want %d", db.gems, len(gem))
	}

	if _, ok := bs.Blobs[db.gems[0].BlobKey]; !ok {
		t.Error("blob not stored")
	}

	evs := pub.Events()
	if len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].RepoID != "repo-1" {
		t.Fatalf("events = %+v", evs)
	}

	if a := evs[0].Artifact(); a["name"] != "demo" || a["version"] != "1.0.0" || a["path"] != "gems/demo-1.0.0.gem" {
		t.Errorf("artifact = %v", a)
	}
}

func TestPushOverQuotaReturns507AndPersistsNothing(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	db.limit = 10

	if code, _ := push(t, h, "demo", "1.0.0"); code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", code)
	}

	if len(db.gems) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.used != 0 {
		t.Errorf("gems=%d blobs=%d events=%d used=%d; want nothing persisted", len(db.gems), len(bs.Blobs), len(pub.Events()), db.used)
	}
}

func TestPushDuplicateDoesNotConsumeQuota(t *testing.T) {
	h, db, _, pub := newTestHandler(t)

	code, gem := push(t, h, "demo", "1.0.0")
	if code != http.StatusCreated {
		t.Fatalf("first push status = %d", code)
	}

	if code, _ := push(t, h, "demo", "1.0.0"); code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409", code)
	}

	if db.used != int64(len(gem)) || db.artifacts != 1 || len(pub.Events()) != 1 {
		t.Errorf("used=%d artifacts=%d events=%d; want %d/1/1", db.used, db.artifacts, len(pub.Events()), len(gem))
	}
}

func TestPushCreateFailureReleasesQuotaAndBlob(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	db.createErr = errCreateFailed

	if code, _ := push(t, h, "demo", "1.0.0"); code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}

	if db.used != 0 || db.artifacts != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 {
		t.Errorf("used=%d artifacts=%d blobs=%d events=%d; want all clean", db.used, db.artifacts, len(bs.Blobs), len(pub.Events()))
	}
}

func TestPushRaceDuplicateReturns409AndReleasesQuota(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.createErr = database.ErrDuplicate

	if code, _ := push(t, h, "demo", "1.0.0"); code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}

	if db.used != 0 || db.artifacts != 0 || len(bs.Blobs) != 0 {
		t.Errorf("used=%d artifacts=%d blobs=%d; want all released", db.used, db.artifacts, len(bs.Blobs))
	}
}

func TestYankHidesGemAndEmitsDeletedOnce(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	code, gem := push(t, h, "demo", "1.0.0")
	if code != http.StatusCreated {
		t.Fatalf("push status = %d", code)
	}

	yank := func(version string) int {
		req := formattest.Request(http.MethodDelete, "/repos/myrepo/api/v1/gems/yank",
			strings.NewReader(url.Values{"gem_name": {"demo"}, "version": {version}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		return formattest.Do(h.RegisterRepoRoutes, req).Code
	}

	if c := yank("1.0.0"); c != http.StatusOK {
		t.Fatalf("yank status = %d", c)
	}

	if c := yank("1.0.0"); c != http.StatusNotFound {
		t.Errorf("repeated yank status = %d, want 404", c)
	}

	if c := yank("9.9.9"); c != http.StatusNotFound {
		t.Errorf("unknown version yank status = %d, want 404", c)
	}

	evs := pub.Events()
	if len(evs) != 2 || evs[1].Type != webhook.EventArtifactDeleted || evs[1].Artifact()["version"] != "1.0.0" {
		t.Fatalf("events = %+v", evs)
	}

	// The blob and quota stay: yank only hides the version.
	if len(bs.Blobs) != 1 || db.used != int64(len(gem)) {
		t.Errorf("blobs=%d used=%d after yank; want 1/%d", len(bs.Blobs), db.used, len(gem))
	}

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/gems/demo-1.0.0.gem", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("yanked gem download status = %d, want 404", w.Code)
	}
}

func TestDownloadQuarantine(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.gems = []*models.RubyGem{{ID: "gem-9", Name: "demo", Version: "1.0.0", Platform: "ruby", BlobKey: "abc"}}
	bs.Blobs["abc"] = []byte("xyz")

	get := func() int {
		return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/gems/demo-1.0.0.gem", nil)).Code
	}

	if c := get(); c != http.StatusOK {
		t.Fatalf("status = %d, want 200", c)
	}

	for _, key := range []string{"gem-9", "gems/demo-1.0.0.gem", "demo-1.0.0.gem"} {
		db.Keys = map[string]bool{key: true}

		if c := get(); c != http.StatusForbidden {
			t.Errorf("quarantined via %q: status = %d, want 403", key, c)
		}
	}
}

func gunzip(t *testing.T, w *httptest.ResponseRecorder) []byte {
	t.Helper()

	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}

	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	return data
}

func TestSpecsSeparatePrereleases(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.gems = []*models.RubyGem{
		{Name: "demo", Version: "1.0.0", Platform: "ruby"},
		{Name: "demo", Version: "2.0.0.beta1", Platform: "ruby"},
		{Name: "demo", Version: "0.9.0", Platform: "ruby"},
		{Name: "gone", Version: "3.0.0", Platform: "ruby", Yanked: true},
	}

	fetch := func(path string) []byte {
		w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/"+path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, w.Code)
		}

		return gunzip(t, w)
	}

	specs := fetch("specs.4.8.gz")
	if !bytes.Contains(specs, []byte("1.0.0")) || !bytes.Contains(specs, []byte("0.9.0")) || bytes.Contains(specs, []byte("beta1")) || bytes.Contains(specs, []byte("gone")) {
		t.Errorf("specs.4.8 must list released, non-yanked versions only: %q", specs)
	}

	latest := fetch("latest_specs.4.8.gz")
	if !bytes.Contains(latest, []byte("1.0.0")) || bytes.Contains(latest, []byte("0.9.0")) || bytes.Contains(latest, []byte("beta1")) {
		t.Errorf("latest_specs.4.8 must contain only the newest released version: %q", latest)
	}

	pre := fetch("prerelease_specs.4.8.gz")
	if !bytes.Contains(pre, []byte("2.0.0.beta1")) || bytes.Contains(pre, []byte("1.0.0")) {
		t.Errorf("prerelease_specs.4.8 must contain only prereleases: %q", pre)
	}
}

func TestIsPrerelease(t *testing.T) {
	for v, want := range map[string]bool{
		"1.0.0": false, "10.2": false, "1.0.0.pre": true, "2.0.0.rc1": true, "1.0.0.beta.2": true, "1.0.0.a": true,
	} {
		if got := isPrerelease(v); got != want {
			t.Errorf("isPrerelease(%q) = %v, want %v", v, got, want)
		}
	}
}

func pushWithLicenses(t *testing.T, db database.Database, bs *formattest.BlobStore, pub *formattest.Publisher, licenses []string) *httptest.ResponseRecorder {
	t.Helper()

	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	gem := createGemFileWithMarshal(t, rubymarshal.Specification{
		Name:     "demo",
		Version:  "1.0.0",
		Platform: "ruby",
		Licenses: licenses,
	})

	return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPost, "/repos/myrepo/api/v1/gems", bytes.NewReader(gem)))
}

func assertGemStored(t *testing.T, c policytest.Case, status int, db *gemsDB, bs *formattest.BlobStore, pub *formattest.Publisher) {
	t.Helper()

	if c.Stopped() {
		if len(db.gems) != 0 || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.used != 0 {
			t.Fatalf("rejected upload stored something: gems=%d blobs=%d events=%d used=%d", len(db.gems), len(bs.Blobs), len(pub.Events()), db.used)
		}

		return
	}

	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}

	if len(db.gems) != 1 || len(bs.Blobs) != 1 || len(pub.Events()) != 1 {
		t.Fatalf("admitted upload not stored: gems=%d blobs=%d events=%d", len(db.gems), len(bs.Blobs), len(pub.Events()))
	}
}

func TestPushLicensePolicy(t *testing.T) {
	for _, c := range policytest.Cases("GPL-3.0") {
		t.Run(c.Name, func(t *testing.T) {
			inner := &gemsDB{DB: formattest.NewDB()}
			bs := formattest.NewBlobStore()
			pub := &formattest.Publisher{}

			w := pushWithLicenses(t, c.DB(inner), bs, pub, []string{"GPL-3.0"})
			c.AssertResponse(t, w.Code, w.Body.Bytes())
			assertGemStored(t, c, w.Code, inner, bs, pub)
		})
	}
}

func TestPushLicensePolicyNoLicenseAdmitted(t *testing.T) {
	c := policytest.Case{
		Want:     policytest.Admitted,
		Policies: []*models.LicensePolicy{policytest.Policy("block", []string{"MIT"}, nil)},
	}
	inner := &gemsDB{DB: formattest.NewDB()}
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}

	w := pushWithLicenses(t, c.DB(inner), bs, pub, nil)
	c.AssertResponse(t, w.Code, w.Body.Bytes())
	assertGemStored(t, c, w.Code, inner, bs, pub)
}

func (d *gemsDB) SearchRubyGems(_ context.Context, _, query string) ([]*models.RubyGem, error) {
	var out []*models.RubyGem

	for _, g := range d.gems {
		if strings.Contains(g.Name, query) {
			out = append(out, g)
		}
	}

	return out, nil
}

func pushMeta(t *testing.T, h *HTTPHandler, meta map[string]any) int {
	t.Helper()

	gem := createGemFile(t, meta)

	return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPost, "/repos/myrepo/api/v1/gems", bytes.NewReader(gem))).Code
}

func getPath(h *HTTPHandler, path string) *httptest.ResponseRecorder {
	return formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/"+path, nil))
}

func TestPushRejectsHyphenAndNonNumericVersion(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)

	for _, v := range []string{"1.0-beta", "1-2", "beta", ".1"} {
		if code, _ := push(t, h, "demo", v); code != http.StatusBadRequest {
			t.Errorf("push version %q = %d, want 400", v, code)
		}
	}

	if len(db.gems) != 0 || len(bs.Blobs) != 0 {
		t.Fatalf("rejected push stored something: gems=%d blobs=%d", len(db.gems), len(bs.Blobs))
	}
}

// A writer cannot shadow another gem's download by choosing a version or
// platform that makes both files share one name.
func TestPushRejectsGemFileNameClash(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)

	// a-1-2.gem: gem "a" 1 with platform "2" ...
	if code := pushMeta(t, h, map[string]any{"name": "a", "version": "1", "platform": "2"}); code != http.StatusCreated {
		t.Fatalf("first push = %d, want 201", code)
	}

	// ... versus gem "a-1" 2 (platform ruby).
	if code := pushMeta(t, h, map[string]any{"name": "a-1", "version": "2"}); code != http.StatusConflict {
		t.Fatalf("clashing push = %d, want 409", code)
	}

	if len(db.gems) != 1 || len(bs.Blobs) != 1 || db.used != db.gems[0].Size {
		t.Fatalf("clashing push stored something: gems=%d blobs=%d used=%d", len(db.gems), len(bs.Blobs), db.used)
	}

	if w := getPath(h, "gems/a-1-2.gem"); w.Code != http.StatusOK {
		t.Fatalf("download of the original gem = %d, want 200", w.Code)
	}

	// The reverse order is refused too.
	h2, db2, _, _ := newTestHandler(t)

	if code := pushMeta(t, h2, map[string]any{"name": "a-1", "version": "2"}); code != http.StatusCreated {
		t.Fatalf("push = %d", code)
	}

	if code := pushMeta(t, h2, map[string]any{"name": "a", "version": "1", "platform": "2"}); code != http.StatusConflict {
		t.Fatalf("reverse clashing push = %d, want 409", code)
	}

	if len(db2.gems) != 1 {
		t.Fatalf("gems = %d, want 1", len(db2.gems))
	}
}

func TestDownloadAmbiguousGemFileIsConflict(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.gems = []*models.RubyGem{
		{Name: "a", Version: "1", Platform: "2", BlobKey: "k1"},
		{Name: "a-1", Version: "2", Platform: "ruby", BlobKey: "k2"},
	}
	bs.Blobs["k1"], bs.Blobs["k2"] = []byte("one"), []byte("two")

	for _, path := range []string{"gems/a-1-2.gem", "quick/Marshal.4.8/a-1-2.gemspec.rz"} {
		if w := getPath(h, path); w.Code != http.StatusConflict {
			t.Errorf("GET %s = %d, want 409", path, w.Code)
		}
	}

	// A yanked twin does not make the live gem ambiguous.
	db.gems[1].Yanked = true

	w := getPath(h, "gems/a-1-2.gem")
	if w.Code != http.StatusOK || w.Body.String() != "one" {
		t.Fatalf("download = %d %q, want the live gem", w.Code, w.Body.String())
	}
}

func TestDownloadContentDispositionIsEscaped(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.gems = []*models.RubyGem{{Name: `x";y=z`, Version: "1.0", Platform: "ruby", BlobKey: "k"}}
	bs.Blobs["k"] = []byte("g")

	req := formattest.Request(http.MethodGet, "/repos/myrepo/gems/x%22;y=z-1.0.gem", nil)

	w := formattest.Do(h.RegisterRepoRoutes, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	disp, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || disp != "attachment" || params["filename"] != `x";y=z-1.0.gem` || len(params) != 1 {
		t.Fatalf("Content-Disposition = %q (%v, %v, %v)", w.Header().Get("Content-Disposition"), disp, params, err)
	}
}

func TestInfoAndSearchHideYankedAndInternalFields(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.gems = []*models.RubyGem{
		{ID: "secret-id", RepositoryID: "secret-repo", Name: "demo", Version: "1.0.0", Platform: "ruby", Summary: "s", Authors: []string{"me"},
			BlobKey: "secret-blob-key", MetadataJSON: []byte(`{"internal":1}`), Dependencies: []byte(`[]`), ChecksumSHA256: "abc", DownloadCount: 3, Size: 7},
		{ID: "id-2", Name: "demo", Version: "0.9.0", Platform: "ruby", Yanked: true, BlobKey: "yanked-blob"},
	}

	for _, path := range []string{"api/v1/gems/demo.json", "api/v1/search.json?query=demo"} {
		w := getPath(h, path)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, w.Code)
		}

		body := w.Body.String()
		for _, leak := range []string{"secret-id", "secret-repo", "secret-blob-key", "yanked-blob", "0.9.0", "internal", "MetadataJSON", "BlobKey", "RepositoryID", "Yanked"} {
			if strings.Contains(body, leak) {
				t.Errorf("GET %s leaks %q: %s", path, leak, body)
			}
		}

		var doc struct {
			Versions []map[string]any `json:"versions"`
			Gems     []map[string]any `json:"gems"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}

		entries := append(doc.Versions, doc.Gems...)
		if len(entries) != 1 {
			t.Fatalf("GET %s: %d entries, want 1 (yanked dropped): %s", path, len(entries), body)
		}

		for key := range entries[0] {
			if key != strings.ToLower(key) {
				t.Errorf("GET %s: key %q is not snake_case", path, key)
			}
		}

		if entries[0]["version"] != "1.0.0" || entries[0]["sha256"] != "abc" || entries[0]["downloads"] != float64(3) {
			t.Errorf("GET %s entry = %v", path, entries[0])
		}
	}

	// A gem whose every version is yanked is gone.
	db.gems[0].Yanked = true

	if w := getPath(h, "api/v1/gems/demo.json"); w.Code != http.StatusNotFound {
		t.Errorf("info of fully yanked gem = %d, want 404", w.Code)
	}

	w := getPath(h, "api/v1/search.json?query=demo")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"gems":[]`) {
		t.Errorf("search of fully yanked gem = %d %s, want empty list", w.Code, w.Body.String())
	}
}

func TestYankHonoursImmutableReleases(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	for _, v := range []string{"1.0.0", "2.0.0.beta1"} {
		if code, _ := push(t, h, "demo", v); code != http.StatusCreated {
			t.Fatalf("push %s = %d", v, code)
		}
	}

	db.Repo.ImmutableReleases = true

	yank := func(version string) int {
		req := formattest.Request(http.MethodDelete, "/repos/myrepo/api/v1/gems/yank",
			strings.NewReader(url.Values{"gem_name": {"demo"}, "version": {version}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		return formattest.Do(h.RegisterRepoRoutes, req).Code
	}

	if c := yank("1.0.0"); c != http.StatusConflict {
		t.Fatalf("yank of immutable release = %d, want 409", c)
	}

	if db.gems[0].Yanked {
		t.Error("release was yanked despite immutable_releases")
	}

	if c := yank("2.0.0.beta1"); c != http.StatusOK {
		t.Fatalf("yank of prerelease = %d, want 200", c)
	}
}
