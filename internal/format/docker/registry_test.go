// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/middleware"
)

var errDuplicateKey = errors.New("duplicate key value violates unique constraint")

// regFakeDB is an in-memory multi-repository registry database. Like the real
// tables, blobs are scoped per repository and manifests/tags per
// (repository, image); CreateDockerBlob fails on a duplicate (the handler must
// not insert one) while manifest create/tag upsert are idempotent like the
// real ON CONFLICT queries.
type regFakeDB struct {
	database.BaseMockDB
	formattest.Quarantine

	repos     map[string]*models.Repository
	blobs     map[string]*models.DockerBlob // repoID|digest
	manifests map[string]*models.DockerManifest
	tags      map[string]*models.DockerTag
	uploads   map[string]*models.DockerUpload
	links     map[string]bool // repoID|image|digest, like docker_blob_images
	resolved  []string        // repository names looked up, in order

	// ledger meters the quota reservations (see quota_test.go).
	ledger quotaLedger
	// upsertBlobs makes CreateDockerBlob refresh an existing row like the real
	// ON CONFLICT query instead of failing on a duplicate.
	upsertBlobs bool
	// createManifestErr fails CreateDockerManifest.
	createManifestErr error
	// insertedOverride, when set, is what CreateDockerManifest reports as
	// Inserted instead of whether the row was new.
	insertedOverride *bool
}

func newRegFakeDB(repoNames ...string) *regFakeDB {
	d := &regFakeDB{
		repos:     map[string]*models.Repository{},
		blobs:     map[string]*models.DockerBlob{},
		manifests: map[string]*models.DockerManifest{},
		tags:      map[string]*models.DockerTag{},
		uploads:   map[string]*models.DockerUpload{},
		links:     map[string]bool{},
	}
	d.Keys = map[string]bool{}

	for _, n := range repoNames {
		d.repos[n] = &models.Repository{ID: "id-" + n, Name: n, Format: "docker"}
	}

	return d
}

func (d *regFakeDB) GetRepositoryByName(_ context.Context, name string) (*models.Repository, error) {
	d.resolved = append(d.resolved, name)
	return d.repos[name], nil
}

func (d *regFakeDB) GetDockerBlob(_ context.Context, repoID, digest string) (*models.DockerBlob, error) {
	return d.blobs[scoped(repoID, digest)], nil
}

func (d *regFakeDB) CreateDockerBlob(_ context.Context, b *models.DockerBlob) error {
	k := scoped(b.RepositoryID, b.Digest)

	_, dup := d.blobs[k]
	if dup && !d.upsertBlobs {
		return errDuplicateKey
	}

	if !dup {
		d.blobs[k] = b
	}

	b.Inserted = !dup

	if b.Image != nil {
		d.links[scoped(b.RepositoryID, scoped(*b.Image, b.Digest))] = true
	}

	return nil
}

func (d *regFakeDB) DeleteDockerBlob(_ context.Context, repoID, digest string) error {
	k := scoped(repoID, digest)
	if _, ok := d.blobs[k]; !ok {
		return database.ErrDockerRowNotFound
	}

	delete(d.blobs, k)

	return nil
}

// DockerBlobVisible and LinkDockerBlob mirror docker_blob_images: a blob is
// visible to an image it is linked to, or to every image while it has no link.
func (d *regFakeDB) DockerBlobVisible(_ context.Context, repoID, image, digest string) (bool, error) {
	if d.links[scoped(repoID, scoped(image, digest))] {
		return true, nil
	}

	for k := range d.links {
		if strings.HasPrefix(k, repoID+"|") && strings.HasSuffix(k, "|"+digest) {
			return false, nil
		}
	}

	return true, nil
}

func (d *regFakeDB) LinkDockerBlob(_ context.Context, repoID, image, digest string) error {
	d.links[scoped(repoID, scoped(image, digest))] = true
	return nil
}

func (d *regFakeDB) CreateDockerUpload(_ context.Context, u *models.DockerUpload) error {
	d.uploads[scoped(u.RepositoryID, u.UploadUUID)] = u
	return nil
}

func (d *regFakeDB) GetDockerUpload(_ context.Context, repoID, uploadUUID string) (*models.DockerUpload, error) {
	return d.uploads[scoped(repoID, uploadUUID)], nil
}

func (d *regFakeDB) UpdateDockerUploadOffset(_ context.Context, repoID, uploadUUID string, oldOffset, newOffset int64) (bool, error) {
	u := d.uploads[scoped(repoID, uploadUUID)]
	if u == nil || u.Offset != oldOffset {
		return false, nil
	}

	u.Offset = newOffset

	return true, nil
}

func (d *regFakeDB) DeleteDockerUpload(_ context.Context, repoID, uploadUUID string) error {
	delete(d.uploads, scoped(repoID, uploadUUID))
	return nil
}

func (d *regFakeDB) EnsureBlob(_ context.Context, sha256, _ string, _ int64) (string, error) {
	return "blob-" + sha256, nil
}

func (d *regFakeDB) CreateDockerManifest(_ context.Context, m *models.DockerManifest) error {
	if d.createManifestErr != nil {
		return d.createManifestErr
	}

	k := scoped(m.RepositoryID, scoped(m.Image, m.Digest))
	_, existed := d.manifests[k]
	d.manifests[k] = m

	m.Inserted = !existed
	if d.insertedOverride != nil {
		m.Inserted = *d.insertedOverride
	}

	return nil
}

func (d *regFakeDB) GetDockerManifest(_ context.Context, repoID, image, digest string) (*models.DockerManifest, error) {
	return d.manifests[scoped(repoID, scoped(image, digest))], nil
}

func (d *regFakeDB) DeleteDockerManifest(_ context.Context, repoID, image, digest string) error {
	k := scoped(repoID, scoped(image, digest))
	if _, ok := d.manifests[k]; !ok {
		return database.ErrDockerRowNotFound
	}

	delete(d.manifests, k)

	return nil
}

func (d *regFakeDB) UpsertDockerTag(_ context.Context, t *models.DockerTag) error {
	d.tags[scoped(t.RepositoryID, scoped(t.Image, t.Name))] = t
	return nil
}

func (d *regFakeDB) GetDockerTag(_ context.Context, repoID, image, name string) (*models.DockerTag, error) {
	return d.tags[scoped(repoID, scoped(image, name))], nil
}

func (d *regFakeDB) DeleteDockerTag(_ context.Context, repoID, image, name string) error {
	delete(d.tags, scoped(repoID, scoped(image, name)))
	return nil
}

func (d *regFakeDB) DeleteDockerTagsByDigest(_ context.Context, repoID, image, digest string) error {
	for k, t := range d.tags {
		if t.RepositoryID == repoID && t.Image == image && t.ManifestDigest == digest {
			delete(d.tags, k)
		}
	}

	return nil
}

func (d *regFakeDB) ListDockerTags(_ context.Context, repoID, image, last string, n int) ([]*models.DockerTag, error) {
	var out []*models.DockerTag

	for _, t := range d.tags {
		if t.RepositoryID == repoID && t.Image == image && t.Name > last {
			out = append(out, t)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	if len(out) > n {
		out = out[:n]
	}

	return out, nil
}

func (d *regFakeDB) ListDockerManifestsBySubject(_ context.Context, repoID, image, subject string) ([]*models.DockerManifest, error) {
	var out []*models.DockerManifest

	for _, m := range d.manifests {
		if m.RepositoryID == repoID && m.Image == image && m.SubjectDigest == subject {
			out = append(out, m)
		}
	}

	return out, nil
}

func (d *regFakeDB) BlobReferenceCount(_ context.Context, sha string) (int, error) {
	for _, m := range d.manifests {
		if manifestBlobKey(m.Digest) == sha {
			return 1, nil
		}
	}

	return 0, nil
}

// allowAll / denyAll are mount-source authorizers.
type fakeAuthz struct {
	allow bool
	calls []string // "repo:path:action"
}

func (a *fakeAuthz) Evaluate(_ context.Context, _ auth.Principal, repoName, path, action string) (bool, error) {
	a.calls = append(a.calls, repoName+":"+path+":"+action)
	return a.allow, nil
}

type registry struct {
	t      *testing.T
	h      *HTTPHandler
	router chi.Router
}

// newRegistry serves h over a real chi router, so every request goes through
// the same /v2/* routing production uses.
func newRegistry(t *testing.T, db database.Database) *registry {
	t.Helper()

	h, _ := newTestHandler(t, db)
	r := chi.NewRouter()
	h.RegisterRoutes(r)

	return &registry{t: t, h: h, router: r}
}

func (g *registry) do(method, target, body string, headers ...string) *httptest.ResponseRecorder {
	g.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	req = req.WithContext(middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "tester"}))

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	rec := httptest.NewRecorder()
	g.router.ServeHTTP(rec, req)

	return rec
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

const ociManifestType = "application/vnd.oci.image.manifest.v1+json"

// pushBlob runs the POST + PUT upload flow and returns the completing response.
func (g *registry) pushBlob(name string, content []byte) *httptest.ResponseRecorder {
	g.t.Helper()

	start := g.do(http.MethodPost, "/v2/"+name+"/blobs/uploads/", "")
	if start.Code != http.StatusAccepted {
		g.t.Fatalf("upload start status = %d, want 202; body=%s", start.Code, start.Body.String())
	}

	loc := start.Header().Get("Location")
	if want := "/v2/" + name + "/blobs/uploads/"; !strings.HasPrefix(loc, want) {
		g.t.Fatalf("upload Location = %q, want prefix %q", loc, want)
	}

	return g.do(http.MethodPut, loc+"?digest="+digestOf(content), string(content))
}

func (g *registry) putManifest(name, ref, body string) *httptest.ResponseRecorder {
	g.t.Helper()
	return g.do(http.MethodPut, "/v2/"+name+"/manifests/"+ref, body, "Content-Type", ociManifestType)
}

func TestRegistry_MultiSegmentNamesRouteToRepoAndImage(t *testing.T) {
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

	manifest := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `"}`
	digest := digestOf([]byte(manifest))
	layer := []byte("layer-bytes")

	// Every endpoint the spec defines, addressed with a three-segment image name.
	if rec := g.putManifest("reg/org/team/app", "1", manifest); rec.Code != http.StatusCreated {
		t.Fatalf("PUT manifest = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	if got := db.tags[scoped("id-reg", scoped("org/team/app", "1"))]; got == nil || got.ManifestDigest != digest {
		t.Fatalf("tag not stored under image org/team/app: %+v", db.tags)
	}

	if rec := g.pushBlob("reg/org/team/app", layer); rec.Code != http.StatusCreated {
		t.Fatalf("blob upload = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	tests := []struct {
		name, method, path string
		want               int
	}{
		{"GET manifest by tag", http.MethodGet, "/v2/reg/org/team/app/manifests/1", http.StatusOK},
		{"HEAD manifest by digest", http.MethodHead, "/v2/reg/org/team/app/manifests/" + digest, http.StatusOK},
		{"HEAD blob", http.MethodHead, "/v2/reg/org/team/app/blobs/" + digestOf(layer), http.StatusOK},
		{"GET blob", http.MethodGet, "/v2/reg/org/team/app/blobs/" + digestOf(layer), http.StatusOK},
		{"tags list", http.MethodGet, "/v2/reg/org/team/app/tags/list", http.StatusOK},
		{"referrers", http.MethodGet, "/v2/reg/org/team/app/referrers/" + digest, http.StatusOK},
		{"manifest of other image is unknown", http.MethodGet, "/v2/reg/org/other/manifests/1", http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rec := g.do(tc.method, tc.path, ""); rec.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d; body=%s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
			}
		})
	}

	// Only the first name segment is an OpenArtifact repository.
	for _, name := range db.resolved {
		if name != "reg" {
			t.Fatalf("resolved repository %q, want only %q (first segment)", name, "reg")
		}
	}
}

func TestRegistry_TagsAreScopedPerImage(t *testing.T) {
	g := newRegistry(t, newRegFakeDB("reg"))

	a := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `","annotations":{"n":"a"}}`
	b := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `","annotations":{"n":"b"}}`

	for _, p := range []struct{ name, body string }{
		{"reg/org/a", a}, {"reg/org/b", b}, {"reg", a}, {"reg/solo", b},
	} {
		if rec := g.putManifest(p.name, "latest", p.body); rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s = %d; body=%s", p.name, rec.Code, rec.Body.String())
		}
	}

	for name, want := range map[string]string{"reg/org/a": a, "reg/org/b": b, "reg": a, "reg/solo": b} {
		rec := g.do(http.MethodGet, "/v2/"+name+"/manifests/latest", "")
		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Fatalf("GET %s latest = %d %q, want 200 %q", name, rec.Code, rec.Body.String(), want)
		}
	}

	// tags/list of one image lists only that image's tags.
	if rec := g.putManifest("reg/org/a", "v2", a); rec.Code != http.StatusCreated {
		t.Fatalf("PUT v2 = %d", rec.Code)
	}

	for name, want := range map[string][]string{"reg/org/a": {"latest", "v2"}, "reg/org/b": {"latest"}, "reg": {"latest"}} {
		rec := g.do(http.MethodGet, "/v2/"+name+"/tags/list", "")

		var resp struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}

		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s tags: %v", name, err)
		}

		if resp.Name != name || strings.Join(resp.Tags, ",") != strings.Join(want, ",") {
			t.Fatalf("tags/list %s = %+v, want name=%s tags=%v", name, resp, name, want)
		}
	}
}

func TestRegistry_NameValidationAndRouting(t *testing.T) {
	g := newRegistry(t, newRegFakeDB("reg"))

	tests := []struct {
		name, method, path string
		want               int
	}{
		{"uppercase image", http.MethodGet, "/v2/reg/Org/app/manifests/1", http.StatusBadRequest},
		{"leading dash component", http.MethodGet, "/v2/reg/-org/manifests/1", http.StatusBadRequest},
		{"empty component", http.MethodGet, "/v2/reg//app/manifests/1", http.StatusBadRequest},
		{"unknown repository", http.MethodGet, "/v2/nope/org/app/manifests/1", http.StatusNotFound},
		{"unknown endpoint", http.MethodGet, "/v2/reg/org/app/unknown/1", http.StatusNotFound},
		{"method not allowed", http.MethodPost, "/v2/reg/org/app/manifests/1", http.StatusMethodNotAllowed},
		{"version check", http.MethodGet, "/v2/", http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rec := g.do(tc.method, tc.path, ""); rec.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d; body=%s", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
			}
		})
	}

	rec := g.do(http.MethodPost, "/v2/reg/org/app/manifests/1", "")
	if allow := rec.Header().Get("Allow"); allow != "DELETE, GET, HEAD, PUT" {
		t.Fatalf("Allow = %q, want DELETE, GET, HEAD, PUT", allow)
	}
}

func TestRegistry_RepushBlobSucceeds(t *testing.T) {
	for _, name := range []string{"reg", "reg/org/team/app"} {
		t.Run(name, func(t *testing.T) {
			db := newRegFakeDB("reg")
			g := newRegistry(t, db)
			layer := []byte("a layer that is pushed twice")

			for i := 1; i <= 2; i++ {
				rec := g.pushBlob(name, layer)
				if rec.Code != http.StatusCreated {
					t.Fatalf("push #%d = %d, want 201; body=%s", i, rec.Code, rec.Body.String())
				}

				if got := rec.Header().Get("Docker-Content-Digest"); got != digestOf(layer) {
					t.Fatalf("push #%d Docker-Content-Digest = %q", i, got)
				}
			}

			if n := len(db.blobs); n != 1 {
				t.Fatalf("blob rows = %d, want 1", n)
			}

			// The blob still downloads intact after the second push.
			rec := g.do(http.MethodGet, "/v2/"+name+"/blobs/"+digestOf(layer), "")
			if rec.Code != http.StatusOK || rec.Body.String() != string(layer) {
				t.Fatalf("GET blob = %d %q", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRegistry_RepushManifest(t *testing.T) {
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

	v1 := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `","annotations":{"v":"1"}}`
	v2 := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `","annotations":{"v":"2"}}`

	const name = "reg/org/team/app"

	steps := []struct {
		desc, ref, body, wantTagDigest string
	}{
		{"first push", "1", v1, digestOf([]byte(v1))},
		{"same tag, same manifest", "1", v1, digestOf([]byte(v1))},
		{"other tag, same manifest", "2", v1, digestOf([]byte(v1))},
		{"same tag, new manifest moves the tag", "1", v2, digestOf([]byte(v2))},
		{"by digest, already stored", digestOf([]byte(v2)), v2, ""},
	}

	for _, st := range steps {
		rec := g.putManifest(name, st.ref, st.body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: status = %d, want 201; body=%s", st.desc, rec.Code, rec.Body.String())
		}

		if got := rec.Header().Get("Docker-Content-Digest"); got != digestOf([]byte(st.body)) {
			t.Fatalf("%s: Docker-Content-Digest = %q", st.desc, got)
		}

		if st.wantTagDigest == "" {
			continue
		}

		if tag := db.tags[scoped("id-reg", scoped("org/team/app", st.ref))]; tag == nil || tag.ManifestDigest != st.wantTagDigest {
			t.Fatalf("%s: tag %s = %+v, want digest %s", st.desc, st.ref, tag, st.wantTagDigest)
		}
	}

	if got := g.do(http.MethodGet, "/v2/"+name+"/manifests/1", "").Body.String(); got != v2 {
		t.Fatalf("GET tag 1 = %q, want the re-pushed manifest", got)
	}

	if got := g.do(http.MethodGet, "/v2/"+name+"/manifests/2", "").Body.String(); got != v1 {
		t.Fatalf("GET tag 2 = %q, want the original manifest", got)
	}
}

func TestRegistry_PutManifestSetsLocation(t *testing.T) {
	g := newRegistry(t, newRegFakeDB("reg"))
	body := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `"}`

	rec := g.putManifest("reg/org/app", "1", body)
	if want := "/v2/reg/org/app/manifests/" + digestOf([]byte(body)); rec.Header().Get("Location") != want {
		t.Fatalf("Location = %q, want %q", rec.Header().Get("Location"), want)
	}
}

func TestRegistry_DeleteManifestSemantics(t *testing.T) {
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

	body := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `"}`
	digest := digestOf([]byte(body))

	const name = "reg/org/app"

	for _, tag := range []string{"a", "b"} {
		if rec := g.putManifest(name, tag, body); rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s = %d", tag, rec.Code)
		}
	}

	// Another image holds the same manifest and tag.
	if rec := g.putManifest("reg/org/other", "a", body); rec.Code != http.StatusCreated {
		t.Fatalf("PUT other = %d", rec.Code)
	}

	// Deleting a tag removes only the tag.
	if rec := g.do(http.MethodDelete, "/v2/"+name+"/manifests/a", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("DELETE tag = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	if rec := g.do(http.MethodGet, "/v2/"+name+"/manifests/a", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET deleted tag = %d, want 404", rec.Code)
	}

	for _, ref := range []string{"b", digest} {
		if rec := g.do(http.MethodGet, "/v2/"+name+"/manifests/"+ref, ""); rec.Code != http.StatusOK {
			t.Fatalf("GET %s after tag delete = %d, want 200", ref, rec.Code)
		}
	}

	if rec := g.do(http.MethodDelete, "/v2/"+name+"/manifests/a", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE missing tag = %d, want 404", rec.Code)
	}

	// Deleting by digest removes the manifest and every tag that pointed at it.
	if rec := g.do(http.MethodDelete, "/v2/"+name+"/manifests/"+digest, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("DELETE digest = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	for _, ref := range []string{"b", digest} {
		if rec := g.do(http.MethodGet, "/v2/"+name+"/manifests/"+ref, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s after digest delete = %d, want 404 (no dangling tag)", ref, rec.Code)
		}
	}

	if rec := g.do(http.MethodDelete, "/v2/"+name+"/manifests/"+digest, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE missing digest = %d, want 404", rec.Code)
	}

	// The other image's copy is untouched, and so is the shared content blob.
	if rec := g.do(http.MethodGet, "/v2/reg/org/other/manifests/a", ""); rec.Code != http.StatusOK || rec.Body.String() != body {
		t.Fatalf("other image manifest = %d %q, want 200 intact", rec.Code, rec.Body.String())
	}
}

func TestRegistry_ImmutableReleasesGuardTags(t *testing.T) {
	db := newRegFakeDB("reg")
	db.repos["reg"].ImmutableReleases = true
	g := newRegistry(t, db)

	v1 := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `","annotations":{"v":"1"}}`
	v2 := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `","annotations":{"v":"2"}}`

	const name = "reg/app"

	if rec := g.putManifest(name, "1", v1); rec.Code != http.StatusCreated {
		t.Fatalf("first push = %d", rec.Code)
	}

	// Re-pushing the same digest under the same tag is idempotent.
	if rec := g.putManifest(name, "1", v1); rec.Code != http.StatusCreated {
		t.Fatalf("idempotent re-push = %d, want 201", rec.Code)
	}

	// Re-pointing the tag to another digest is refused.
	if rec := g.putManifest(name, "1", v2); rec.Code != http.StatusConflict {
		t.Fatalf("retag = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	if tag := db.tags[scoped("id-reg", scoped("app", "1"))]; tag == nil || tag.ManifestDigest != digestOf([]byte(v1)) {
		t.Fatalf("tag moved despite 409: %+v", tag)
	}

	// A new tag is not an overwrite.
	if rec := g.putManifest(name, "2", v2); rec.Code != http.StatusCreated {
		t.Fatalf("new tag = %d, want 201", rec.Code)
	}

	// Deleting a tag or a manifest is refused too.
	if rec := g.do(http.MethodDelete, "/v2/"+name+"/manifests/1", ""); rec.Code != http.StatusConflict {
		t.Fatalf("delete tag = %d, want 409", rec.Code)
	}

	if rec := g.do(http.MethodDelete, "/v2/"+name+"/manifests/"+digestOf([]byte(v1)), ""); rec.Code != http.StatusConflict {
		t.Fatalf("delete manifest = %d, want 409", rec.Code)
	}

	if rec := g.do(http.MethodGet, "/v2/"+name+"/manifests/1", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET after refused delete = %d, want 200", rec.Code)
	}
}

func TestRegistry_MountBlob(t *testing.T) {
	layer := []byte("mountable layer")
	digest := digestOf(layer)

	type setup struct {
		desc      string
		authz     *fakeAuthz // nil = no authorizer configured
		query     string
		quarantnd bool
		wantCode  int
		wantLink  bool // blob row linked into the target repo
	}

	tests := []setup{
		{"cross-repo, readable source", &fakeAuthz{allow: true}, "?mount=" + digest + "&from=src/lib/base", false, http.StatusCreated, true},
		{"cross-repo, source unreadable falls back to upload", &fakeAuthz{allow: false}, "?mount=" + digest + "&from=src/lib/base", false, http.StatusAccepted, false},
		{"cross-repo, no authorizer configured", nil, "?mount=" + digest + "&from=src/lib/base", false, http.StatusAccepted, false},
		{"cross-repo, quarantined source", &fakeAuthz{allow: true}, "?mount=" + digest + "&from=src/lib/base", true, http.StatusAccepted, false},
		{"cross-repo, source lacks the blob", &fakeAuthz{allow: true}, "?mount=" + digestOf([]byte("other")) + "&from=src/lib/base", false, http.StatusAccepted, false},
		{"cross-repo, unknown source repo", &fakeAuthz{allow: true}, "?mount=" + digest + "&from=ghost/x", false, http.StatusAccepted, false},
		{"cross-repo, invalid source image", &fakeAuthz{allow: true}, "?mount=" + digest + "&from=src/Bad", false, http.StatusAccepted, false},
		{"mount without from", &fakeAuthz{allow: true}, "?mount=" + digest, false, http.StatusAccepted, false},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			db := newRegFakeDB("dst", "src")
			db.blobs[scoped("id-src", digest)] = &models.DockerBlob{
				ID: "b1", RepositoryID: "id-src", Digest: digest, MediaType: "application/octet-stream",
				Size: int64(len(layer)), BlobSHA256: strings.TrimPrefix(digest, "sha256:"),
			}

			if tc.quarantnd {
				db.Keys[digest] = true
			}

			g := newRegistry(t, db)
			if tc.authz != nil {
				g.h.SetAuthorizer(tc.authz)
			}

			rec := g.do(http.MethodPost, "/v2/dst/team/app/blobs/uploads/"+tc.query, "")
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.wantCode, rec.Body.String())
			}

			linked := db.blobs[scoped("id-dst", digest)] != nil
			if linked != tc.wantLink {
				t.Fatalf("blob linked into target = %v, want %v", linked, tc.wantLink)
			}

			if tc.wantCode == http.StatusCreated {
				if want := "/v2/dst/team/app/blobs/" + digest; rec.Header().Get("Location") != want {
					t.Fatalf("Location = %q, want %q", rec.Header().Get("Location"), want)
				}
			} else if rec.Header().Get("Docker-Upload-Uuid") == "" {
				t.Fatal("fallback must open an upload session")
			}
		})
	}
}

func TestRegistry_MountAuthorizesSourceImagePath(t *testing.T) {
	layer := []byte("layer")
	digest := digestOf(layer)
	db := newRegFakeDB("dst", "src")
	db.blobs[scoped("id-src", digest)] = &models.DockerBlob{ID: "b1", RepositoryID: "id-src", Digest: digest, BlobSHA256: strings.TrimPrefix(digest, "sha256:")}

	authz := &fakeAuthz{allow: true}
	g := newRegistry(t, db)
	g.h.SetAuthorizer(authz)

	g.do(http.MethodPost, "/v2/dst/blobs/uploads/?mount="+digest+"&from=src/lib/base", "")

	if want := "src:lib/base/blobs/" + digest + ":read"; len(authz.calls) != 1 || authz.calls[0] != want {
		t.Fatalf("authorizer calls = %v, want [%s]", authz.calls, want)
	}
}

func TestRegistry_MountOfBlobAlreadyInTargetImageNeedsNoSource(t *testing.T) {
	layer := []byte("layer")
	digest := digestOf(layer)
	db := newRegFakeDB("dst")
	g := newRegistry(t, db)

	if rec := g.pushBlob("dst/team/app", layer); rec.Code != http.StatusCreated {
		t.Fatalf("seed push = %d", rec.Code)
	}

	// The image already holds the blob: no source is consulted.
	rec := g.do(http.MethodPost, "/v2/dst/team/app/blobs/uploads/?mount="+digest+"&from=elsewhere/x", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("mount = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}

// RBAC path_pattern scopes a permission to one image, so a blob another image
// of the same repository holds must not be readable, referenceable, deletable
// or mountable through this image without proof the caller may read it there.
func TestRegistry_BlobsAreScopedToTheirImage(t *testing.T) {
	layer := []byte("secret layer of team-a")
	digest := digestOf(layer)
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

	if rec := g.pushBlob("reg/team-a/app", layer); rec.Code != http.StatusCreated {
		t.Fatalf("push to team-a = %d; body=%s", rec.Code, rec.Body.String())
	}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		if rec := g.do(method, "/v2/reg/team-a/app/blobs/"+digest, ""); rec.Code != http.StatusOK {
			t.Fatalf("%s via owning image = %d, want 200", method, rec.Code)
		}

		if rec := g.do(method, "/v2/reg/team-b/app/blobs/"+digest, ""); rec.Code != http.StatusNotFound {
			t.Fatalf("%s via other image = %d, want 404", method, rec.Code)
		}
	}

	// A manifest of the other image may not reference the blob.
	manifest := `{"schemaVersion":2,"mediaType":"` + ociManifestType + `","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + digest + `","size":22},"layers":[]}`
	if rec := g.putManifest("reg/team-b/app", "1", manifest); rec.Code != http.StatusBadRequest {
		t.Fatalf("manifest PUT referencing another image's blob = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	if rec := g.do(http.MethodDelete, "/v2/reg/team-b/app/blobs/"+digest, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE via other image = %d, want 404", rec.Code)
	}

	// A mount without a source the caller can read does not link it.
	if rec := g.do(http.MethodPost, "/v2/reg/team-b/app/blobs/uploads/?mount="+digest+"&from=reg/team-a/app", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("mount without authorizer = %d, want 202 (fallback upload)", rec.Code)
	}

	// With read access to the source image the mount links it.
	g.h.SetAuthorizer(&fakeAuthz{allow: true})

	if rec := g.do(http.MethodPost, "/v2/reg/team-b/app/blobs/uploads/?mount="+digest+"&from=reg/team-a/app", ""); rec.Code != http.StatusCreated {
		t.Fatalf("authorised same-repo mount = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	if rec := g.do(http.MethodGet, "/v2/reg/team-b/app/blobs/"+digest, ""); rec.Code != http.StatusOK {
		t.Fatalf("GET after mount = %d, want 200", rec.Code)
	}

	// Pushing the same bytes proves possession and links the image too.
	if rec := g.pushBlob("reg/team-c/app", layer); rec.Code != http.StatusCreated {
		t.Fatalf("push of existing digest = %d, want 201", rec.Code)
	}

	if rec := g.do(http.MethodGet, "/v2/reg/team-c/app/blobs/"+digest, ""); rec.Code != http.StatusOK {
		t.Fatalf("GET after push = %d, want 200", rec.Code)
	}
}

// The shared blobs row keeps the first writer's size and media type; the
// registry answers from the verified size and a fixed content type.
func TestRegistry_BlobHeadersIgnoreSharedRow(t *testing.T) {
	layer := []byte("0123456789")
	db := newRegFakeDB("reg")
	g := newRegistry(t, db)

	if rec := g.pushBlob("reg/app", layer); rec.Code != http.StatusCreated {
		t.Fatalf("push = %d", rec.Code)
	}

	// Another tenant stored the same bytes first as text/html with size 0.
	db.blobs[scoped("id-reg", digestOf(layer))].MediaType = "text/html"
	db.blobs[scoped("id-reg", digestOf(layer))].Size = 0

	rec := g.do(http.MethodGet, "/v2/reg/app/blobs/"+digestOf(layer), "")
	if rec.Code != http.StatusOK || rec.Body.String() != string(layer) {
		t.Fatalf("GET = %d body %q", rec.Code, rec.Body.String())
	}

	if got := rec.Header().Get("Content-Length"); got != "10" {
		t.Errorf("Content-Length = %q, want 10", got)
	}

	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
}
