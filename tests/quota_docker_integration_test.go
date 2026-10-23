// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/auth"
	formatdocker "github.com/openartifact/openartifact/internal/format/docker"
	"github.com/openartifact/openartifact/internal/middleware"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/worker"
)

// qdFixture is the docker registry + GC fixture with two repositories, "reg"
// and "src" (a mount source), a caller allowed to read both, and quota usage
// readers for each. Retention is 0: deletes are hard unless a test says so.
type qdFixture struct {
	*gcFixture

	reg *qrEnv
	src *qrEnv
}

// qdAllowAll authorizes every cross-repository mount source read.
type qdAllowAll struct{}

func (qdAllowAll) Evaluate(context.Context, auth.Principal, string, string, string) (bool, error) {
	return true, nil
}

func newQDFixture(t *testing.T) *qdFixture {
	t.Helper()

	pg := freshPostgres(t, 12)
	pg.SetTrashRetentionDays(0)

	store, err := storage.NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	log := newTestLogger(t)

	h := formatdocker.NewHTTPHandler(log, pg, store, "http://localhost")
	h.SetAuthorizer(qdAllowAll{})

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := middleware.WithPrincipal(req.Context(), &auth.Principal{Username: "tester"})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	h.RegisterRoutes(r)

	regID := newRepo(t, pg, "reg", "docker")
	srcID := newRepo(t, pg, "src", "docker")

	repo, err := pg.GetRepositoryByName(context.Background(), "reg")
	if err != nil || repo == nil {
		t.Fatalf("GetRepositoryByName: %v", err)
	}

	return &qdFixture{
		gcFixture: &gcFixture{
			dockerRegistry: &dockerRegistry{t: t, pg: pg, router: r},
			store:          store,
			gc:             worker.NewDockerGCWorker(pg, store, log, 0, gcGrace),
			repoID:         regID,
			repo:           repo,
		},
		reg: &qrEnv{t: t, pg: pg, repoID: regID, repoName: "reg"},
		src: &qrEnv{t: t, pg: pg, repoID: srcID, repoName: "src"},
	}
}

// qdLen is the number of bytes the given contents add up to.
func qdLen(parts ...string) int64 {
	var n int64
	for _, p := range parts {
		n += int64(len(p))
	}

	return n
}

// setLimit caps the repository's quota (0 removes the cap).
func (f *qdFixture) setLimit(e *qrEnv, maxBytes int64) {
	f.t.Helper()

	ctx := context.Background()

	q, err := f.pg.GetOrCreateStorageQuota(ctx, e.repoID)
	if err != nil {
		f.t.Fatalf("GetOrCreateStorageQuota: %v", err)
	}

	q.MaxSizeBytes = maxBytes
	if err := f.pg.UpdateStorageQuota(ctx, q); err != nil {
		f.t.Fatalf("UpdateStorageQuota: %v", err)
	}
}

// wantReconciled recomputes the counters from the registry tables and asserts
// they equal the incrementally maintained ones.
func (f *qdFixture) wantReconciled(e *qrEnv, step string) {
	f.t.Helper()

	e.wantNoPending(step)

	size, count := e.usage()

	q, err := f.pg.ReconcileStorageUsage(context.Background(), e.repoID)
	if err != nil {
		f.t.Fatalf("%s: ReconcileStorageUsage(%s): %v", step, e.repoName, err)
	}

	if q.CurrentSizeBytes != size || q.CurrentArtifactCount != count {
		f.t.Fatalf("%s: %s reconcile = (%d bytes, %d artifacts), incremental = (%d, %d)",
			step, e.repoName, q.CurrentSizeBytes, q.CurrentArtifactCount, size, count)
	}
}

// wantDenied asserts the registry's quota refusal: 507 and one OCI error,
// DENIED, carrying the quota message (what `docker push` prints).
func (f *qdFixture) wantDenied(step string, rec *httptest.ResponseRecorder) {
	f.t.Helper()

	if rec.Code != http.StatusInsufficientStorage {
		f.t.Fatalf("%s: status = %d, want 507; body=%s", step, rec.Code, rec.Body.String())
	}

	var body struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 {
		f.t.Fatalf("%s: body %q, want one OCI error (err %v)", step, rec.Body.String(), err)
	}

	if e := body.Errors[0]; e.Code != "DENIED" || e.Message != quota.Message {
		f.t.Fatalf("%s: error = %+v, want DENIED / %q", step, e, quota.Message)
	}
}

// mount asks dst to mount the blob digest of from, as `docker push` does
// before uploading a layer.
func (f *qdFixture) mount(dst, from string, content string) *httptest.ResponseRecorder {
	f.t.Helper()

	return f.do(http.MethodPost, "/v2/"+dst+"/blobs/uploads/?mount="+ociDigest([]byte(content))+"&from="+from, "")
}

func (f *qdFixture) wantStatusCode(step string, rec *httptest.ResponseRecorder, want int) {
	f.t.Helper()

	if rec.Code != want {
		f.t.Fatalf("%s: status = %d, want %d; body=%s", step, rec.Code, want, rec.Body.String())
	}
}

func (f *qdFixture) deleteManifest(name, digest string) *httptest.ResponseRecorder {
	f.t.Helper()

	return f.do(http.MethodDelete, "/v2/"+name+"/manifests/"+digest, "")
}

// TestQuotaDocker_PushMetersBlobsAndManifestOnce: a push costs its blob bytes
// plus the manifest body and one artifact; re-pushes, retags and a second image
// sharing the blobs add only what is new.
func TestQuotaDocker_PushMetersBlobsAndManifestOnce(t *testing.T) {
	f := newQDFixture(t)
	f.reg.wantUsage("fresh repository", 0, 0)

	const cfg, l1, l2 = `{"os":"linux"}`, "first layer bytes", "second layer bytes, a bit longer"

	m, _ := f.pushImage("reg/app", "v1", cfg, l1, l2)
	full := qdLen(cfg, l1, l2, m)

	f.reg.wantUsage("after first push", full, 1)
	f.wantReconciled(f.reg, "after first push")

	// Same blobs, same manifest: again under the tag, under another tag and by digest.
	f.pushImage("reg/app", "v1", cfg, l1, l2)
	f.pushImage("reg/app", "v2", cfg, l1, l2)
	f.pushImage("reg/app", "", cfg, l1, l2)
	f.reg.wantUsage("after re-push, retag and push by digest", full, 1)
	f.wantReconciled(f.reg, "after re-push")

	// A second image shares the layer blobs (one row per repository and
	// digest) but holds its own manifest row.
	f.pushImage("reg/team/app", "v1", cfg, l1, l2)
	f.reg.wantUsage("after pushing the same image under another name", full+qdLen(m), 2)
	f.wantReconciled(f.reg, "after second image")

	// A different manifest over a shared layer adds its config blob and body.
	const cfg3 = `{"os":"linux","rev":3}`

	m3, _ := f.pushImage("reg/app", "v3", cfg3, l1)
	f.reg.wantUsage("after a third manifest", full+qdLen(m)+qdLen(cfg3, m3), 3)
	f.wantReconciled(f.reg, "after third manifest")

	// Nothing leaked into the other repository.
	f.src.wantUsage("untouched repository", 0, 0)
}

// TestQuotaDocker_OverQuotaIs507: a blob or a manifest that does not fit is
// refused with DENIED and stores nothing; being exactly full is not over quota.
func TestQuotaDocker_OverQuotaIs507(t *testing.T) {
	f := newQDFixture(t)

	const cfg, layer = `{"c":1}`, "layer"

	manifest := imageManifest(t, cfg, layer)
	blobs := qdLen(cfg, layer)
	full := blobs + qdLen(manifest)

	// Room for the blobs, one byte short for the manifest body.
	f.setLimit(f.reg, full-1)

	for _, content := range []string{cfg, layer} {
		f.wantStatusCode("push blob", f.pushBlob("reg/app", []byte(content)), http.StatusCreated)
	}

	f.reg.wantUsage("after blobs", blobs, 0)

	f.wantDenied("manifest over quota", f.putManifest("reg/app", "v1", manifest))
	f.reg.wantUsage("after refused manifest", blobs, 0)

	if got := f.manifests(); got != 0 {
		t.Errorf("manifest rows after refusal = %d, want 0", got)
	}

	if got := f.count(`SELECT count(*) FROM docker_tags WHERE repository_id = $1`, f.repoID); got != 0 {
		t.Errorf("tags after refusal = %d, want 0", got)
	}

	f.wantReconciled(f.reg, "after refused manifest")

	// A layer that does not fit: nothing stored, no staged upload left behind.
	big := strings.Repeat("x", 1000)

	f.wantDenied("blob over quota", f.pushBlob("reg/app", []byte(big)))
	f.reg.wantUsage("after refused blob", blobs, 0)

	if got := f.blobRecords(); got != 2 {
		t.Errorf("blob rows after refusal = %d, want 2", got)
	}

	if f.inStore(ociDigest([]byte(big))) {
		t.Error("refused blob reached the store")
	}

	if got := f.count(`SELECT count(*) FROM docker_uploads WHERE repository_id = $1`, f.repoID); got != 0 {
		t.Errorf("upload sessions left after refusal = %d, want 0", got)
	}

	f.wantReconciled(f.reg, "after refused blob")

	// Exactly full fits; one byte more does not.
	f.setLimit(f.reg, full)
	f.wantStatusCode("manifest at the limit", f.putManifest("reg/app", "v1", manifest), http.StatusCreated)
	f.reg.wantUsage("at the limit", full, 1)

	f.wantDenied("one byte over", f.pushBlob("reg/app", []byte("z")))
	f.reg.wantUsage("after one byte over", full, 1)

	// A full repository still takes everything it already holds.
	f.pushImage("reg/app", "v1", cfg, layer)
	f.pushImage("reg/app", "v2", cfg, layer)
	f.reg.wantUsage("re-push at the limit", full, 1)
	f.wantReconciled(f.reg, "re-push at the limit")
}

// TestQuotaDocker_MountMetering: mounting within a repository costs nothing; a
// mount from another repository creates a row, billed to the target once.
func TestQuotaDocker_MountMetering(t *testing.T) {
	f := newQDFixture(t)

	const own, foreign = "layer pushed to reg", "layer pushed to src, longer"

	f.wantStatusCode("push own", f.pushBlob("reg/a", []byte(own)), http.StatusCreated)
	f.wantStatusCode("push foreign", f.pushBlob("src/app", []byte(foreign)), http.StatusCreated)

	f.wantStatusCode("same-repository mount", f.mount("reg/b", "reg/a", own), http.StatusCreated)
	f.reg.wantUsage("after same-repository mount", qdLen(own), 0)

	f.wantStatusCode("cross-repository mount", f.mount("reg/b", "src/app", foreign), http.StatusCreated)
	f.reg.wantUsage("after cross-repository mount", qdLen(own, foreign), 0)
	f.src.wantUsage("source repository", qdLen(foreign), 0)

	// Again, and into another image of the repository that already holds the row.
	f.wantStatusCode("repeated mount", f.mount("reg/b", "src/app", foreign), http.StatusCreated)
	f.wantStatusCode("mount into another image", f.mount("reg/c", "src/app", foreign), http.StatusCreated)
	f.reg.wantUsage("after repeated mounts", qdLen(own, foreign), 0)

	f.wantReconciled(f.reg, "after mounts")
	f.wantReconciled(f.src, "after mounts")

	// A mount that does not fit is refused, not downgraded to an upload.
	big := strings.Repeat("m", 800)
	f.wantStatusCode("push big", f.pushBlob("src/app", []byte(big)), http.StatusCreated)
	f.setLimit(f.reg, qdLen(own, foreign)+10)

	f.wantDenied("mount over quota", f.mount("reg/b", "src/app", big))
	f.reg.wantUsage("after refused mount", qdLen(own, foreign), 0)

	var linked int
	if err := f.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM docker_blobs WHERE repository_id = $1 AND digest = $2`,
		f.repoID, ociDigest([]byte(big))).Scan(&linked); err != nil || linked != 0 {
		t.Errorf("refused mount linked %d rows (err %v), want 0", linked, err)
	}

	f.wantReconciled(f.reg, "after refused mount")
}

// TestQuotaDocker_FailedManifestPushLeaksNothing: a manifest that references a
// blob the repository does not hold, or is pushed under a digest that is not
// its own, reserves nothing; the push that follows is charged as usual.
func TestQuotaDocker_FailedManifestPushLeaksNothing(t *testing.T) {
	f := newQDFixture(t)

	const cfg, layer = `{"c":2}`, "layer that is pushed late"

	f.wantStatusCode("push config", f.pushBlob("reg/app", []byte(cfg)), http.StatusCreated)
	f.reg.wantUsage("after config", qdLen(cfg), 0)

	manifest := imageManifest(t, cfg, layer)

	f.wantStatusCode("manifest with a missing blob", f.putManifest("reg/app", "v1", manifest), http.StatusBadRequest)
	f.wantStatusCode("digest reference that does not match the body",
		f.putManifest("reg/app", ociDigest([]byte("something else")), manifest), http.StatusBadRequest)
	f.reg.wantUsage("after failed pushes", qdLen(cfg), 0)

	if got := f.manifests(); got != 0 {
		t.Errorf("manifest rows = %d, want 0", got)
	}

	f.wantReconciled(f.reg, "after failed pushes")

	f.wantStatusCode("push layer", f.pushBlob("reg/app", []byte(layer)), http.StatusCreated)
	f.wantStatusCode("manifest", f.putManifest("reg/app", "v1", manifest), http.StatusCreated)
	f.reg.wantUsage("after the complete push", qdLen(cfg, layer, manifest), 1)
	f.wantReconciled(f.reg, "after the complete push")
}

// TestQuotaDocker_ConcurrentIdenticalPushesCountOnce: parallel pushes of the
// same image race on every insert; the losers give their reservation back.
func TestQuotaDocker_ConcurrentIdenticalPushesCountOnce(t *testing.T) {
	f := newQDFixture(t)

	const (
		workers = 6
		cfg     = `{"concurrent":true}`
		layer   = "a layer many clients push at once"
	)

	manifest := imageManifest(t, cfg, layer)

	var wg sync.WaitGroup

	failures := make([]string, workers)

	for i := range workers {
		wg.Go(func() {
			for _, content := range []string{cfg, layer} {
				start := f.do(http.MethodPost, "/v2/reg/app/blobs/uploads/", "")
				if start.Code != http.StatusAccepted {
					failures[i] = "upload start: " + start.Body.String()
					return
				}

				put := f.do(http.MethodPut, start.Header().Get("Location")+"?digest="+ociDigest([]byte(content)), content)
				if put.Code != http.StatusCreated {
					failures[i] = "blob push: " + put.Body.String()
					return
				}
			}

			if rec := f.putManifest("reg/app", "latest", manifest); rec.Code != http.StatusCreated {
				failures[i] = "manifest push: " + rec.Body.String()
			}
		})
	}

	wg.Wait()

	for i, msg := range failures {
		if msg != "" {
			t.Errorf("worker %d: %s", i, msg)
		}
	}

	f.reg.wantUsage("after concurrent pushes", qdLen(cfg, layer, manifest), 1)
	f.wantReconciled(f.reg, "after concurrent pushes")
}

// TestQuotaDocker_DeleteReleasesUsage: a tag delete releases nothing, deleting
// a manifest by digest releases its body and artifact once, a soft delete keeps
// the usage until the trash is purged, and deleting a blob releases its bytes.
func TestQuotaDocker_DeleteReleasesUsage(t *testing.T) {
	const cfg1, cfg2, shared, l1, l2 = `{"v":1}`, `{"v":2}`, "layer shared by both", "layer one", "layer two"

	push := func(f *qdFixture) (string, string, string, int64) {
		m1, d1 := f.pushImage("reg/app", "v1", cfg1, shared, l1)
		m2, _ := f.pushImage("reg/app", "v2", cfg2, shared, l2)
		total := qdLen(cfg1, cfg2, shared, l1, l2, m1, m2)

		f.reg.wantUsage("after both pushes", total, 2)

		return m1, d1, m2, total
	}

	t.Run("hard delete by digest", func(t *testing.T) {
		f := newQDFixture(t)
		m1, d1, _, total := push(f)

		f.deleteTag("reg/app", "v1")
		f.reg.wantUsage("after tag delete", total, 2)

		f.wantStatusCode("manifest delete", f.deleteManifest("reg/app", d1), http.StatusAccepted)
		f.reg.wantUsage("after manifest delete", total-qdLen(m1), 1)
		f.wantReconciled(f.reg, "after manifest delete")

		f.wantStatusCode("second delete", f.deleteManifest("reg/app", d1), http.StatusNotFound)
		f.reg.wantUsage("after second delete", total-qdLen(m1), 1)
	})

	t.Run("soft delete keeps the usage", func(t *testing.T) {
		f := newQDFixture(t)
		f.pg.SetTrashRetentionDays(7)

		_, d1, _, total := push(f)

		f.wantStatusCode("manifest delete", f.deleteManifest("reg/app", d1), http.StatusAccepted)
		f.reg.wantUsage("after soft delete", total, 2)
		f.wantReconciled(f.reg, "after soft delete")
	})

	t.Run("blob delete", func(t *testing.T) {
		f := newQDFixture(t)

		const keep, drop = "blob that stays", "blob that is deleted through the API"

		f.wantStatusCode("push keep", f.pushBlob("reg/app", []byte(keep)), http.StatusCreated)
		f.wantStatusCode("push drop", f.pushBlob("reg/app", []byte(drop)), http.StatusCreated)
		f.reg.wantUsage("after pushes", qdLen(keep, drop), 0)

		target := "/v2/reg/app/blobs/" + ociDigest([]byte(drop))

		f.wantStatusCode("blob delete", f.do(http.MethodDelete, target, ""), http.StatusAccepted)
		f.reg.wantUsage("after blob delete", qdLen(keep), 0)
		f.wantReconciled(f.reg, "after blob delete")

		f.wantStatusCode("second blob delete", f.do(http.MethodDelete, target, ""), http.StatusNotFound)
		f.reg.wantUsage("after second blob delete", qdLen(keep), 0)
	})
}

// TestQuotaDocker_GCReleasesManifestsAndBlobs: registry GC gives back what it
// deletes, per repository and only once; a dry run gives back nothing.
func TestQuotaDocker_GCReleasesManifestsAndBlobs(t *testing.T) {
	f := newQDFixture(t)

	const cfg1, cfg2, shared, ex1, ex2 = `{"v":1}`, `{"v":2}`, "layer shared by both tags", "layer only in v1", "layer only in v2"

	m1, _ := f.pushImage("reg/app", "v1", cfg1, shared, ex1)
	m2, _ := f.pushImage("reg/app", "v2", cfg2, shared, ex2)

	// Another repository holds v1's exclusive layer: its usage must not move.
	f.wantStatusCode("push to src", f.pushBlob("src/app", []byte(ex1)), http.StatusCreated)

	total := qdLen(cfg1, cfg2, shared, ex1, ex2, m1, m2)
	f.reg.wantUsage("after pushes", total, 2)
	f.src.wantUsage("source after push", qdLen(ex1), 0)

	f.deleteTag("reg/app", "v1")
	f.age(2 * gcGrace)

	dry := f.run(worker.DockerGCOptions{DryRun: true, Grace: gcGrace})
	if dry.ManifestsDeleted != 1 {
		t.Fatalf("dry run reported %d manifests, want 1", dry.ManifestsDeleted)
	}

	f.reg.wantUsage("after dry run", total, 2)

	rep := f.run(worker.DockerGCOptions{Grace: gcGrace})
	if rep.ManifestsDeleted != 1 || rep.BlobRefsDeleted != 2 || rep.Failures != 0 {
		t.Fatalf("GC report: manifests=%d blob_refs=%d failures=%d, want 1/2/0", rep.ManifestsDeleted, rep.BlobRefsDeleted, rep.Failures)
	}

	// v1's manifest and its two exclusive blob rows are gone.
	f.reg.wantUsage("after GC", total-qdLen(m1, cfg1, ex1), 1)
	f.src.wantUsage("source after GC", qdLen(ex1), 0)
	f.wantReconciled(f.reg, "after GC")
	f.wantReconciled(f.src, "after GC")

	// Nothing is left to collect, so nothing is released twice.
	f.run(worker.DockerGCOptions{Grace: gcGrace})
	f.reg.wantUsage("after second GC", total-qdLen(m1, cfg1, ex1), 1)

	f.wantStatusCode("surviving tag", f.do(http.MethodGet, "/v2/reg/app/manifests/v2", ""), http.StatusOK)

	// The freed room is real.
	f.setLimit(f.reg, total-qdLen(m1, cfg1, ex1))
	f.wantDenied("push over the reclaimed limit", f.pushBlob("reg/app", []byte("one more layer")))
}
