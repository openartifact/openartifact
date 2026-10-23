// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/worker"
)

const (
	ociManifestType = "application/vnd.oci.image.manifest.v1+json"
	ociIndexType    = "application/vnd.oci.image.index.v1+json"

	gcGrace = 24 * time.Hour
)

// gcFixture is a docker registry (handler + Postgres + filesystem store) with
// the registry GC worker and its admin endpoint mounted on the same router.
type gcFixture struct {
	*dockerRegistry

	store  storage.BlobStore
	gc     *worker.DockerGCWorker
	repoID string
	repo   *models.Repository
}

func newGCFixture(t *testing.T) *gcFixture {
	t.Helper()

	pg := freshPostgres(t, 8)

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	log := newTestLogger(t)
	gc := worker.NewDockerGCWorker(pg, store, log, 0, gcGrace)

	r := chi.NewRouter()
	formatdocker.NewHTTPHandler(log, pg, store, "http://localhost").RegisterRoutes(r)
	api.NewDockerGCHandler(pg, log, auth.NewEvaluator(pg, log, false), gc).RegisterAuthed(r)

	repoID := newRepo(t, pg, "reg", "docker")

	repo, err := pg.GetRepositoryByName(context.Background(), "reg")
	if err != nil || repo == nil {
		t.Fatalf("GetRepositoryByName: %v", err)
	}

	return &gcFixture{
		dockerRegistry: &dockerRegistry{t: t, pg: pg, router: r},
		store:          store,
		gc:             gc,
		repoID:         repoID,
		repo:           repo,
	}
}

func ociDescriptor(mediaType string, content []byte) map[string]any {
	return map[string]any{"mediaType": mediaType, "digest": ociDigest(content), "size": len(content)}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return string(b)
}

// imageManifest returns an OCI image manifest referencing config and layers.
func imageManifest(t *testing.T, config string, layers ...string) string {
	t.Helper()

	descs := make([]map[string]any, len(layers))
	for i, l := range layers {
		descs[i] = ociDescriptor("application/vnd.oci.image.layer.v1.tar", []byte(l))
	}

	return mustJSON(t, map[string]any{
		"schemaVersion": 2,
		"mediaType":     ociManifestType,
		"config":        ociDescriptor("application/vnd.oci.image.config.v1+json", []byte(config)),
		"layers":        descs,
	})
}

// pushImage uploads config and layers to name and puts the manifest under ref
// (a tag, or a digest for an untagged push). It returns the manifest and its digest.
func (f *gcFixture) pushImage(name, ref, config string, layers ...string) (string, string) {
	f.t.Helper()

	for _, content := range append([]string{config}, layers...) {
		if rec := f.pushBlob(name, []byte(content)); rec.Code != http.StatusCreated {
			f.t.Fatalf("push blob to %s = %d; body=%s", name, rec.Code, rec.Body.String())
		}
	}

	manifest := imageManifest(f.t, config, layers...)
	digest := ociDigest([]byte(manifest))

	if ref == "" {
		ref = digest
	}

	if rec := f.putManifest(name, ref, manifest); rec.Code != http.StatusCreated {
		f.t.Fatalf("PUT %s:%s = %d; body=%s", name, ref, rec.Code, rec.Body.String())
	}

	return manifest, digest
}

func (f *gcFixture) putTyped(name, ref, body, mediaType string) {
	f.t.Helper()

	if rec := f.do(http.MethodPut, "/v2/"+name+"/manifests/"+ref, body, "Content-Type", mediaType); rec.Code != http.StatusCreated {
		f.t.Fatalf("PUT %s:%s = %d; body=%s", name, ref, rec.Code, rec.Body.String())
	}
}

func (f *gcFixture) deleteTag(name, tag string) {
	f.t.Helper()

	if rec := f.do(http.MethodDelete, "/v2/"+name+"/manifests/"+tag, ""); rec.Code != http.StatusAccepted {
		f.t.Fatalf("DELETE %s:%s = %d; body=%s", name, tag, rec.Code, rec.Body.String())
	}
}

func (f *gcFixture) status(method, target string) int {
	f.t.Helper()

	return f.do(method, target, "").Code
}

// age makes everything pushed so far look d old. updated_at is maintained by a
// trigger, so it is disabled for the rewrite.
func (f *gcFixture) age(d time.Duration) {
	f.t.Helper()

	ctx := context.Background()

	tx, err := f.pg.GetDB().BeginTx(ctx, nil)
	if err != nil {
		f.t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	for _, stmt := range []string{
		`ALTER TABLE docker_manifests DISABLE TRIGGER USER`,
		`UPDATE docker_manifests SET updated_at = now() - make_interval(secs => ` + secs(d) + `)`,
		`ALTER TABLE docker_manifests ENABLE TRIGGER USER`,
		`UPDATE docker_blobs SET last_used_at = now() - make_interval(secs => ` + secs(d) + `)`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			f.t.Fatalf("%s: %v", stmt, err)
		}
	}

	if err := tx.Commit(); err != nil {
		f.t.Fatalf("commit: %v", err)
	}
}

func secs(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}

func (f *gcFixture) count(query string, args ...any) int {
	f.t.Helper()

	var n int
	if err := f.pg.GetDB().QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", query, err)
	}

	return n
}

func (f *gcFixture) manifests() int {
	return f.count(`SELECT count(*) FROM docker_manifests WHERE repository_id = $1`, f.repoID)
}

func (f *gcFixture) blobRecords() int {
	return f.count(`SELECT count(*) FROM docker_blobs WHERE repository_id = $1`, f.repoID)
}

func (f *gcFixture) inStore(digest string) bool {
	f.t.Helper()

	sweepQueuedBlobs(f.t, f.pg, f.store)

	_, ok, err := f.store.ExistsBySHA256(context.Background(), strings.TrimPrefix(digest, "sha256:"))
	if err != nil {
		f.t.Fatalf("ExistsBySHA256: %v", err)
	}

	return ok
}

func (f *gcFixture) run(opts worker.DockerGCOptions) *worker.DockerGCReport {
	f.t.Helper()

	rep, err := f.gc.RunRepo(context.Background(), f.repo, opts)
	if err != nil {
		f.t.Fatalf("RunRepo: %v", err)
	}

	return rep
}

// expect checks the headline numbers of a report.
func expect(t *testing.T, rep *worker.DockerGCReport, manifests, blobRefs, blobsFreed int) {
	t.Helper()

	if rep.ManifestsDeleted != manifests || rep.BlobRefsDeleted != blobRefs || rep.BlobsFreed != blobsFreed {
		t.Fatalf("report: manifests=%d blob_refs=%d blobs_freed=%d, want %d/%d/%d (failures=%d)",
			rep.ManifestsDeleted, rep.BlobRefsDeleted, rep.BlobsFreed, manifests, blobRefs, blobsFreed, rep.Failures)
	}
}

// TestDockerGC_TagDeleteFreesExclusiveBlobs: deleting a tag leaves its manifest
// behind; after the grace period GC removes it and the blobs only it used, and
// keeps blobs another manifest of the repository, or another repository, uses.
// The "legacy" case drops the recorded references first, as for manifests
// pushed before they were tracked: GC must rebuild them from the stored bodies.
func TestDockerGC_TagDeleteFreesExclusiveBlobs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		wipeRefs bool
	}{
		{name: "indexed at push"},
		{name: "legacy manifests are indexed by GC", wipeRefs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGCFixture(t)
			newRepo(t, f.pg, "other", "docker")

			const shared, ex1, ex2, cfg1, cfg2 = "layer shared by both tags", "layer only in v1", "layer only in v2", `{"v":1}`, `{"v":2}`

			m1, d1 := f.pushImage("reg/app", "v1", cfg1, shared, ex1)
			_, d2 := f.pushImage("reg/app", "v2", cfg2, shared, ex2)
			// Another repository uses v1's exclusive layer too: its storage must survive.
			f.pushImage("other/app", "x", `{"o":1}`, ex1)

			f.deleteTag("reg/app", "v1")

			if tc.wipeRefs {
				if _, err := f.pg.GetDB().ExecContext(context.Background(), `DELETE FROM docker_manifest_refs`); err != nil {
					t.Fatalf("wipe refs: %v", err)
				}
			}

			f.age(2 * gcGrace)

			rep := f.run(worker.DockerGCOptions{Grace: gcGrace})
			// v1's manifest body and config are freed; its exclusive layer is
			// unreferenced here but still used by repository "other".
			expect(t, rep, 1, 2, 2)

			if want := int64(len(m1) + len(cfg1)); rep.BytesFreed != want {
				t.Errorf("BytesFreed = %d, want %d", rep.BytesFreed, want)
			}

			if got := f.manifests(); got != 1 {
				t.Errorf("manifests left = %d, want 1", got)
			}

			if got := f.blobRecords(); got != 3 { // cfg2, shared, ex2
				t.Errorf("blob records left = %d, want 3", got)
			}

			for digest, want := range map[string]bool{
				d1: false, ociDigest([]byte(cfg1)): false,
				ociDigest([]byte(ex1)): true, // other repo
				d2:                     true, ociDigest([]byte(shared)): true, ociDigest([]byte(ex2)): true,
			} {
				if f.inStore(digest) != want {
					t.Errorf("blob %s in store = %v, want %v", digest, !want, want)
				}
			}

			// The surviving tag still pulls, manifest and every blob.
			if got := f.status(http.MethodGet, "/v2/reg/app/manifests/v2"); got != http.StatusOK {
				t.Fatalf("GET v2 manifest = %d", got)
			}

			for _, l := range []string{shared, ex2, cfg2} {
				if got := f.status(http.MethodGet, "/v2/reg/app/blobs/"+ociDigest([]byte(l))); got != http.StatusOK {
					t.Errorf("GET blob %q = %d, want 200", l, got)
				}
			}

			expect(t, f.run(worker.DockerGCOptions{Grace: gcGrace}), 0, 0, 0)
		})
	}
}

// TestDockerGC_MultiArchIndexKeepsChildren: the manifests a tagged index lists
// are kept although untagged themselves; once the index is untagged and old
// enough, it and its children go together.
func TestDockerGC_MultiArchIndexKeepsChildren(t *testing.T) {
	f := newGCFixture(t)

	_, amd := f.pushImage("reg/app", "", `{"arch":"amd64"}`, "amd64 layer")
	_, arm := f.pushImage("reg/app", "", `{"arch":"arm64"}`, "arm64 layer")
	f.pushImage("reg/app", "", `{"stray":true}`, "stray layer") // untagged, in no index

	index := mustJSON(t, map[string]any{
		"schemaVersion": 2,
		"mediaType":     ociIndexType,
		"manifests": []map[string]any{
			{"mediaType": ociManifestType, "digest": amd, "size": 1, "platform": map[string]string{"os": "linux", "architecture": "amd64"}},
			{"mediaType": ociManifestType, "digest": arm, "size": 1, "platform": map[string]string{"os": "linux", "architecture": "arm64"}},
		},
	})
	f.putTyped("reg/app", "latest", index, ociIndexType)

	f.age(2 * gcGrace)

	// Only the stray manifest (and its two blobs) is garbage: the plan (dry
	// run) and the transactional delete both leave the children alone.
	expect(t, f.run(worker.DockerGCOptions{DryRun: true, Grace: gcGrace}), 1, 2, 3)
	expect(t, f.run(worker.DockerGCOptions{Grace: gcGrace}), 1, 2, 3)

	for _, d := range []string{amd, arm} {
		if got := f.status(http.MethodGet, "/v2/reg/app/manifests/"+d); got != http.StatusOK {
			t.Fatalf("GET child %s = %d, want 200", d, got)
		}
	}

	f.deleteTag("reg/app", "latest")
	f.age(2 * gcGrace)

	// Index + both children and their four blobs.
	expect(t, f.run(worker.DockerGCOptions{DryRun: true, Grace: gcGrace}), 3, 4, 7)
	expect(t, f.run(worker.DockerGCOptions{Grace: gcGrace}), 3, 4, 7)

	if f.manifests() != 0 || f.blobRecords() != 0 {
		t.Fatalf("left %d manifests, %d blob records; want none", f.manifests(), f.blobRecords())
	}
}

// TestDockerGC_RecentUntaggedManifestKept: the grace period protects a manifest
// that lost its tag only a moment ago.
func TestDockerGC_RecentUntaggedManifestKept(t *testing.T) {
	f := newGCFixture(t)

	_, digest := f.pushImage("reg/app", "v1", `{"v":1}`, "some layer")
	f.deleteTag("reg/app", "v1")

	expect(t, f.run(worker.DockerGCOptions{Grace: gcGrace}), 0, 0, 0)

	if got := f.status(http.MethodGet, "/v2/reg/app/manifests/"+digest); got != http.StatusOK {
		t.Fatalf("GET recent untagged manifest = %d, want 200", got)
	}

	// Without a grace period the same manifest is garbage.
	expect(t, f.run(worker.DockerGCOptions{}), 1, 2, 3)
}

// TestDockerGC_DryRunDeletesNothing: a dry run reports what a real run would
// delete, and the real run then does exactly that.
func TestDockerGC_DryRunDeletesNothing(t *testing.T) {
	f := newGCFixture(t)

	_, digest := f.pushImage("reg/app", "v1", `{"v":1}`, "some layer")
	f.pushImage("reg/app", "v2", `{"v":2}`, "another layer")
	f.deleteTag("reg/app", "v1")
	f.age(2 * gcGrace)

	manifestsBefore, blobsBefore := f.manifests(), f.blobRecords()

	dry := f.run(worker.DockerGCOptions{DryRun: true, Grace: gcGrace})
	expect(t, dry, 1, 2, 3)

	if len(dry.Manifests) != 1 || dry.Manifests[0].Digest != digest || len(dry.Blobs) != 2 {
		t.Errorf("dry run listed manifests=%v blobs=%v", dry.Manifests, dry.Blobs)
	}

	if f.manifests() != manifestsBefore || f.blobRecords() != blobsBefore {
		t.Fatalf("dry run changed the registry: manifests %d->%d, blob records %d->%d",
			manifestsBefore, f.manifests(), blobsBefore, f.blobRecords())
	}

	if !f.inStore(digest) || f.status(http.MethodGet, "/v2/reg/app/manifests/"+digest) != http.StatusOK {
		t.Fatal("dry run removed the manifest")
	}

	expect(t, f.run(worker.DockerGCOptions{Grace: gcGrace}), 1, 2, 3)

	if f.manifests() != manifestsBefore-1 || f.blobRecords() != blobsBefore-2 {
		t.Fatalf("real run left manifests=%d blob records=%d", f.manifests(), f.blobRecords())
	}
}

// TestDockerGC_ReferrersAreKeptWithTheirSubject: a manifest tied to another by
// the subject field survives as long as either side of the pair is kept.
func TestDockerGC_ReferrersAreKeptWithTheirSubject(t *testing.T) {
	for _, tc := range []struct {
		name            string
		taggedIsSubject bool
	}{
		{name: "referrer of a tagged subject", taggedIsSubject: true},
		{name: "subject of a tagged referrer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGCFixture(t)

			subjectManifest, subjectDigest := f.pushImage("reg/app", "", `{"image":true}`, "image layer")

			emptyConfig := []byte("{}")
			if rec := f.pushBlob("reg/app", emptyConfig); rec.Code != http.StatusCreated {
				t.Fatalf("push empty config = %d", rec.Code)
			}

			sig := mustJSON(t, map[string]any{
				"schemaVersion": 2,
				"mediaType":     ociManifestType,
				"artifactType":  "application/vnd.example.signature",
				"config":        ociDescriptor("application/vnd.oci.empty.v1+json", emptyConfig),
				"layers":        []map[string]any{},
				"subject":       ociDescriptor(ociManifestType, []byte(subjectManifest)),
			})

			// Exactly one of the pair gets a tag; the other is pushed by digest.
			subjectRef, sigRef, tag := "v1", ociDigest([]byte(sig)), "v1"
			if !tc.taggedIsSubject {
				subjectRef, sigRef, tag = subjectDigest, "sig", "sig"
			}

			f.putTyped("reg/app", subjectRef, subjectManifest, ociManifestType)
			f.putTyped("reg/app", sigRef, sig, ociManifestType)
			f.age(2 * gcGrace)

			expect(t, f.run(worker.DockerGCOptions{Grace: gcGrace}), 0, 0, 0)

			if got := f.manifests(); got != 2 {
				t.Fatalf("manifests after GC = %d, want both kept", got)
			}

			// Untag the remaining one: the pair is garbage.
			f.deleteTag("reg/app", tag)
			f.age(2 * gcGrace)

			if rep := f.run(worker.DockerGCOptions{Grace: gcGrace}); rep.ManifestsDeleted != 2 {
				t.Fatalf("manifests deleted = %d, want 2", rep.ManifestsDeleted)
			}
		})
	}
}

// TestDockerGC_BlobProbedRecentlyIsKept: a client that HEADs a layer and then
// takes a while to push the manifest referencing it must not lose the layer,
// however old the upload is; layers nobody probed or referenced are collected.
func TestDockerGC_BlobProbedRecentlyIsKept(t *testing.T) {
	f := newGCFixture(t)

	probed, abandoned, cfg := []byte("layer probed by a slow client"), []byte("abandoned layer"), []byte(`{"slow":true}`)

	for _, content := range [][]byte{probed, abandoned, cfg} {
		if rec := f.pushBlob("reg/app", content); rec.Code != http.StatusCreated {
			t.Fatalf("push blob = %d", rec.Code)
		}
	}

	f.age(2 * gcGrace)

	if got := f.status(http.MethodHead, "/v2/reg/app/blobs/"+ociDigest(probed)); got != http.StatusOK {
		t.Fatalf("HEAD probed blob = %d, want 200", got)
	}

	if got := f.status(http.MethodHead, "/v2/reg/app/blobs/"+ociDigest(cfg)); got != http.StatusOK {
		t.Fatalf("HEAD config blob = %d, want 200", got)
	}

	// Only the abandoned layer is garbage.
	expect(t, f.run(worker.DockerGCOptions{Grace: gcGrace}), 0, 1, 1)

	if f.inStore(ociDigest(abandoned)) || !f.inStore(ociDigest(probed)) {
		t.Fatal("abandoned layer should be gone and the probed one kept")
	}

	manifest := imageManifest(t, string(cfg), string(probed))
	if rec := f.putManifest("reg/app", "v1", manifest); rec.Code != http.StatusCreated {
		t.Fatalf("PUT manifest = %d; body=%s", rec.Code, rec.Body.String())
	}

	if got := f.status(http.MethodGet, "/v2/reg/app/blobs/"+ociDigest(probed)); got != http.StatusOK {
		t.Fatalf("GET probed blob after the push = %d, want 200", got)
	}
}

// TestDockerGC_RunsDoNotOverlap: a second run on a repository that is being
// collected is refused, by the worker and by the endpoint.
func TestDockerGC_RunsDoNotOverlap(t *testing.T) {
	f := newGCFixture(t)

	conn, err := f.pg.GetDB().Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}

	defer func() { _ = conn.Close() }()

	var held bool
	if err := conn.QueryRowContext(context.Background(), `SELECT pg_try_advisory_lock(hashtext($1))`, "docker-gc:"+f.repoID).Scan(&held); err != nil || !held {
		t.Fatalf("take the repository's GC lock: held=%v err=%v", held, err)
	}

	if _, err := f.gc.RunRepo(context.Background(), f.repo, worker.DockerGCOptions{}); !errors.Is(err, worker.ErrDockerGCBusy) {
		t.Fatalf("RunRepo while locked = %v, want ErrDockerGCBusy", err)
	}

	if rec := f.adminGC("reg", `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("endpoint while locked = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, "docker-gc:"+f.repoID); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	if rec := f.adminGC("reg", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("endpoint after unlock = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// adminGC posts to the GC endpoint as a global admin.
func (f *gcFixture) adminGC(repo, body string) *httptest.ResponseRecorder {
	f.t.Helper()

	return f.gcRequest(&auth.Principal{IsLegacy: true}, repo, body)
}

func (f *gcFixture) gcRequest(p *auth.Principal, repo, body string) *httptest.ResponseRecorder {
	f.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/repositories/"+repo+"/registry-gc", strings.NewReader(body))
	req = req.WithContext(middleware.WithPrincipal(req.Context(), p))

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)

	return rec
}

// TestDockerGC_AdminEndpoint covers the contract of POST
// /api/v1/repositories/{name}/registry-gc.
func TestDockerGC_AdminEndpoint(t *testing.T) {
	f := newGCFixture(t)
	newRepo(t, f.pg, "mvn", "maven")

	f.pushImage("reg/app", "v1", `{"v":1}`, "some layer")
	f.pushImage("reg/app", "v2", `{"v":2}`, "another layer")
	f.deleteTag("reg/app", "v1")

	if rec := f.gcRequest(&auth.Principal{IsAnonymous: true}, "reg", `{}`); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin = %d, want 403", rec.Code)
	}

	for _, tc := range []struct {
		name, repo, body string
		want             int
	}{
		{"unknown repository", "nope", `{}`, http.StatusNotFound},
		{"not a docker repository", "mvn", `{}`, http.StatusBadRequest},
		{"negative grace", "reg", `{"grace_period_seconds":-1}`, http.StatusBadRequest},
		{"malformed body", "reg", `{"dry_run":`, http.StatusBadRequest},
	} {
		if rec := f.adminGC(tc.repo, tc.body); rec.Code != tc.want {
			t.Errorf("%s = %d, want %d; body=%s", tc.name, rec.Code, tc.want, rec.Body.String())
		}
	}

	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}

		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}

		return out
	}

	// The configured 24h grace protects the manifest that was just untagged.
	got := decode(f.adminGC("reg", ``))
	if got["manifests_deleted"] != float64(0) || got["grace_period_seconds"] != float64(86400) || got["dry_run"] != false {
		t.Fatalf("default-grace run = %v", got)
	}

	dry := decode(f.adminGC("reg", `{"dry_run":true,"grace_period_seconds":0}`))
	if dry["manifests_deleted"] != float64(1) || dry["blob_refs_deleted"] != float64(2) || dry["dry_run"] != true || f.manifests() != 2 {
		t.Fatalf("dry run = %v (manifests in DB: %d)", dry, f.manifests())
	}

	done := decode(f.adminGC("reg", `{"grace_period_seconds":0}`))
	if done["manifests_deleted"] != float64(1) || done["blobs_freed"] != float64(3) || f.manifests() != 1 {
		t.Fatalf("real run = %v (manifests in DB: %d)", done, f.manifests())
	}
}

// TestDockerGC_ConcurrentPushesSurvive re-pushes images GC is in the middle of
// collecting. Whatever the interleaving, every image whose push succeeded must
// be pullable afterwards, manifest and blobs.
func TestDockerGC_ConcurrentPushesSurvive(t *testing.T) {
	f := newGCFixture(t)

	const n = 15

	type image struct{ cfg, layer string }

	images := make([]image, n)
	for i := range images {
		images[i] = image{fmt.Sprintf(`{"i":%d}`, i), fmt.Sprintf("layer exclusive to image %d", i)}
		tag := fmt.Sprintf("old%d", i)

		f.pushImage("reg/app", tag, images[i].cfg, images[i].layer)
		f.deleteTag("reg/app", tag)
	}

	f.age(2 * gcGrace) // all n manifests are now garbage

	ctx, cancel := context.WithCancel(context.Background())

	var (
		wg   sync.WaitGroup
		runs int
	)

	wg.Go(func() {
		for ctx.Err() == nil {
			if _, err := f.gc.RunRepo(context.WithoutCancel(ctx), f.repo, worker.DockerGCOptions{Grace: gcGrace}); err != nil {
				t.Errorf("RunRepo: %v", err)
				return
			}

			runs++
		}
	})

	stopGC := sync.OnceFunc(func() { cancel(); wg.Wait() })
	t.Cleanup(stopGC)

	// Push each image again, identical to its garbage twin, while GC runs.
	for i, img := range images {
		f.pushImage("reg/app", fmt.Sprintf("re%d", i), img.cfg, img.layer)
	}

	stopGC()

	if runs == 0 {
		t.Fatal("GC never ran")
	}

	t.Logf("GC ran %d times alongside %d pushes", runs, n)

	for i, img := range images {
		tag := fmt.Sprintf("re%d", i)

		if got := f.status(http.MethodGet, "/v2/reg/app/manifests/"+tag); got != http.StatusOK {
			t.Fatalf("GET %s manifest = %d after concurrent GC", tag, got)
		}

		for _, content := range []string{img.cfg, img.layer} {
			if got := f.status(http.MethodGet, "/v2/reg/app/blobs/"+ociDigest([]byte(content))); got != http.StatusOK {
				t.Fatalf("GET %s blob %q = %d after concurrent GC", tag, content, got)
			}
		}
	}

	expect(t, f.run(worker.DockerGCOptions{Grace: gcGrace}), 0, 0, 0)
}

// TestDockerGC_ScheduledPassCollectsRepositories: the background worker finds
// docker repositories on its own and collects them on its interval, and stops
// cleanly. A zero interval schedules nothing.
func TestDockerGC_ScheduledPassCollectsRepositories(t *testing.T) {
	f := newGCFixture(t)
	newRepo(t, f.pg, "mvn", "maven") // other formats are not visited

	f.pushImage("reg/app", "v1", `{"v":1}`, "some layer")
	f.pushImage("reg/app", "v2", `{"v":2}`, "another layer")
	f.deleteTag("reg/app", "v1")

	disabled := worker.NewDockerGCWorker(f.pg, f.store, newTestLogger(t), 0, 0)
	disabled.Start()
	disabled.Stop()

	if f.manifests() != 2 {
		t.Fatalf("a worker with interval 0 collected something: %d manifests", f.manifests())
	}

	scheduled := worker.NewDockerGCWorker(f.pg, f.store, newTestLogger(t), 20*time.Millisecond, 0)
	scheduled.Start()

	t.Cleanup(scheduled.Stop)

	waitUntil(t, 10*time.Second, "scheduled GC to delete the untagged manifest", func() bool { return f.manifests() == 1 })

	if got := f.status(http.MethodGet, "/v2/reg/app/manifests/v2"); got != http.StatusOK {
		t.Fatalf("tagged manifest after scheduled GC = %d, want 200", got)
	}
}
