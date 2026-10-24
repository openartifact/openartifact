// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/quota"
)

var errCreateFailed = errors.New("create failed")

// quotaUsage is one repository's metered usage.
type quotaUsage struct {
	bytes     int64
	artifacts int
}

// quotaLedger is the in-memory storage_quotas the fake databases meter
// against, one row per repository. A zero value is ready to use: no limit.
type quotaLedger struct {
	maxBytes int64 // 0 = unlimited; applies to every repository
	usage    map[string]*quotaUsage
}

func (l *quotaLedger) row(repoID string) *quotaUsage {
	if l.usage == nil {
		l.usage = map[string]*quotaUsage{}
	}

	u := l.usage[repoID]
	if u == nil {
		u = &quotaUsage{}
		l.usage[repoID] = u
	}

	return u
}

func (l *quotaLedger) reserve(repoID string, size int64, count int) bool {
	u := l.row(repoID)
	if l.maxBytes > 0 && u.bytes+size > l.maxBytes {
		return false
	}

	u.bytes += size
	u.artifacts += count

	return true
}

// charge adds usage past any limit (a row that already exists).
func (l *quotaLedger) charge(repoID string, size int64, count int) {
	u := l.row(repoID)
	u.bytes += size
	u.artifacts += count
}

func (l *quotaLedger) release(repoID string, size int64, count int) {
	u := l.row(repoID)
	u.bytes = max(0, u.bytes-size)
	u.artifacts = max(0, u.artifacts-count)
}

func (l *quotaLedger) quota(repoID string) *models.StorageQuota {
	u := l.row(repoID)

	return &models.StorageQuota{RepositoryID: repoID, MaxSizeBytes: l.maxBytes, CurrentSizeBytes: u.bytes, CurrentArtifactCount: u.artifacts}
}

func (l *quotaLedger) of(repoID string) quotaUsage { return *l.row(repoID) }

// The fake databases' metering methods.

func (d *regFakeDB) GetOrCreateStorageQuota(_ context.Context, repoID string) (*models.StorageQuota, error) {
	return d.ledger.quota(repoID), nil
}

func (f *manifestFakeDB) GetOrCreateStorageQuota(_ context.Context, repoID string) (*models.StorageQuota, error) {
	return f.ledger.quota(repoID), nil
}

func (f *fakeDB) GetOrCreateStorageQuota(_ context.Context, repoID string) (*models.StorageQuota, error) {
	return f.ledger.quota(repoID), nil
}

func (d *regFakeDB) ReserveStorageUsage(_ context.Context, repoID string, size int64, count int) (bool, error) {
	return d.ledger.reserve(repoID, size, count), nil
}

func (d *regFakeDB) ChargeStorageUsage(_ context.Context, repoID string, size int64, count int) error {
	d.ledger.charge(repoID, size, count)
	return nil
}

func (d *regFakeDB) DecrementStorageUsage(_ context.Context, repoID string, size int64, count int) error {
	d.ledger.release(repoID, size, count)
	return nil
}

func (f *manifestFakeDB) ReserveStorageUsage(_ context.Context, repoID string, size int64, count int) (bool, error) {
	return f.ledger.reserve(repoID, size, count), nil
}

func (f *manifestFakeDB) ChargeStorageUsage(_ context.Context, repoID string, size int64, count int) error {
	f.ledger.charge(repoID, size, count)
	return nil
}

func (f *manifestFakeDB) DecrementStorageUsage(_ context.Context, repoID string, size int64, count int) error {
	f.ledger.release(repoID, size, count)
	return nil
}

func (f *fakeDB) ReserveStorageUsage(_ context.Context, repoID string, size int64, count int) (bool, error) {
	return f.ledger.reserve(repoID, size, count), nil
}

func (f *fakeDB) ChargeStorageUsage(_ context.Context, repoID string, size int64, count int) error {
	f.ledger.charge(repoID, size, count)
	return nil
}

func (f *fakeDB) DecrementStorageUsage(_ context.Context, repoID string, size int64, count int) error {
	f.ledger.release(repoID, size, count)
	return nil
}

// quotaRegistry is a registry over a regFakeDB whose repositories may hold at
// most maxBytes (0 = unlimited).
func quotaRegistry(t *testing.T, maxBytes int64, repoNames ...string) (*registry, *regFakeDB) {
	t.Helper()

	db := newRegFakeDB(repoNames...)
	db.ledger.maxBytes = maxBytes

	return newRegistry(t, db), db
}

// usage is the metered usage of repository name.
func (d *regFakeDB) usage(name string) quotaUsage { return d.ledger.of(d.repos[name].ID) }

func assertUsage(t *testing.T, what string, got quotaUsage, wantBytes int64, wantArtifacts int) {
	t.Helper()

	if got.bytes != wantBytes || got.artifacts != wantArtifacts {
		t.Errorf("%s: usage = %d bytes / %d artifacts, want %d / %d", what, got.bytes, got.artifacts, wantBytes, wantArtifacts)
	}
}

// assertDenied checks for the registry's quota refusal: 507 with one OCI error
// DENIED carrying the quota message.
func assertDenied(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()

	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507; body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 {
		t.Fatalf("body = %q, want one OCI error", rec.Body.String())
	}

	if body.Errors[0].Code != "DENIED" || body.Errors[0].Message != quota.Message {
		t.Errorf("error = %+v, want DENIED with the quota message", body.Errors[0])
	}
}

// imageManifest is a minimal image manifest referencing config and layers.
func imageManifest(t *testing.T, config []byte, layers ...[]byte) string {
	t.Helper()

	type desc struct {
		MediaType string `json:"mediaType"` //nolint:tagliatelle // OCI wire format
		Digest    string `json:"digest"`
		Size      int    `json:"size"`
	}

	m := struct {
		SchemaVersion int    `json:"schemaVersion"` //nolint:tagliatelle // OCI wire format
		MediaType     string `json:"mediaType"`     //nolint:tagliatelle // OCI wire format
		Config        desc   `json:"config"`
		Layers        []desc `json:"layers"`
	}{
		SchemaVersion: 2,
		MediaType:     ociManifestType,
		Config:        desc{"application/vnd.oci.image.config.v1+json", digestOf(config), len(config)},
	}

	for _, l := range layers {
		m.Layers = append(m.Layers, desc{"application/vnd.oci.image.layer.v1.tar", digestOf(l), len(l)})
	}

	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	return string(b)
}

func (g *registry) mustPushBlob(name string, content []byte) {
	g.t.Helper()

	if rec := g.pushBlob(name, content); rec.Code != http.StatusCreated {
		g.t.Fatalf("push blob to %s = %d; body=%s", name, rec.Code, rec.Body.String())
	}
}

func (g *registry) mustPutManifest(name, ref, body string) {
	g.t.Helper()

	if rec := g.putManifest(name, ref, body); rec.Code != http.StatusCreated {
		g.t.Fatalf("put manifest %s:%s = %d; body=%s", name, ref, rec.Code, rec.Body.String())
	}
}

func TestQuotaBlobUploadMetersBytesOnlyOnce(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	layer := []byte(strings.Repeat("layer", 40))

	g.mustPushBlob("reg/app", layer)
	assertUsage(t, "after push", db.usage("reg"), int64(len(layer)), 0)

	// Re-pushing the blob, to the same image or another one, adds nothing.
	g.mustPushBlob("reg/app", layer)
	g.mustPushBlob("reg/other", layer)
	assertUsage(t, "after re-push", db.usage("reg"), int64(len(layer)), 0)
}

func TestQuotaBlobOverQuotaIs507StoresNothing(t *testing.T) {
	g, db := quotaRegistry(t, 100, "reg")

	assertDenied(t, g.pushBlob("reg/app", []byte(strings.Repeat("x", 101))))

	if len(db.blobs) != 0 {
		t.Errorf("blob rows = %d, want none", len(db.blobs))
	}

	assertUsage(t, "after refused push", db.usage("reg"), 0, 0)

	// The refused session is discarded rather than left staged.
	if len(db.uploads) != 0 {
		t.Errorf("upload sessions left = %d, want 0", len(db.uploads))
	}
}

func TestQuotaManifestMetersBodyAndOneArtifact(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	config, layer := []byte(`{"os":"linux"}`), []byte(strings.Repeat("l", 64))
	g.mustPushBlob("reg/app", config)
	g.mustPushBlob("reg/app", layer)

	body := imageManifest(t, config, layer)
	want := int64(len(config) + len(layer) + len(body))

	g.mustPutManifest("reg/app", "v1", body)
	assertUsage(t, "after push", db.usage("reg"), want, 1)

	// A tag move or an identical re-push by digest is not a new manifest.
	g.mustPutManifest("reg/app", "v2", body)
	g.mustPutManifest("reg/app", digestOf([]byte(body)), body)
	assertUsage(t, "after re-push", db.usage("reg"), want, 1)
}

func TestQuotaManifestOverQuotaIs507(t *testing.T) {
	config, layer := []byte(`{}`), []byte(strings.Repeat("l", 32))

	// Room for both blobs, not for the manifest body.
	g, db := quotaRegistry(t, int64(len(config)+len(layer)), "reg")
	g.mustPushBlob("reg/app", config)
	g.mustPushBlob("reg/app", layer)

	assertDenied(t, g.putManifest("reg/app", "v1", imageManifest(t, config, layer)))

	if len(db.manifests) != 0 || len(db.tags) != 0 {
		t.Errorf("manifests=%d tags=%d, want nothing persisted", len(db.manifests), len(db.tags))
	}

	assertUsage(t, "after refused manifest", db.usage("reg"), int64(len(config)+len(layer)), 0)
}

func TestQuotaRePushAtLimitStillSucceeds(t *testing.T) {
	config, layer := []byte(`{}`), []byte(strings.Repeat("l", 32))
	body := imageManifest(t, config, layer)
	full := int64(len(config) + len(layer) + len(body))

	g, db := quotaRegistry(t, full, "reg")
	g.mustPushBlob("reg/app", config)
	g.mustPushBlob("reg/app", layer)
	g.mustPutManifest("reg/app", "v1", body)

	// The repository is exactly full: nothing new is stored, so nothing is refused.
	g.mustPushBlob("reg/app", layer)
	g.mustPutManifest("reg/app", "v2", body)
	assertUsage(t, "full repository", db.usage("reg"), full, 1)
}

func TestQuotaFailedManifestPushLeaksNothing(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	config, layer := []byte(`{}`), []byte("layer")
	g.mustPushBlob("reg/app", config)

	// The referenced layer was never pushed: refused before anything is reserved.
	if rec := g.putManifest("reg/app", "v1", imageManifest(t, config, layer)); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	assertUsage(t, "after missing-blob push", db.usage("reg"), int64(len(config)), 0)

	// A failure after the reservation (here: the layer vanished under the
	// insert's own check) gives the reservation back.
	g.mustPushBlob("reg/app", layer)

	db.createManifestErr = database.ErrDockerBlobsMissing

	if rec := g.putManifest("reg/app", "v1", imageManifest(t, config, layer)); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	assertUsage(t, "after failed insert", db.usage("reg"), int64(len(config)+len(layer)), 0)

	db.createManifestErr = errCreateFailed

	if rec := g.putManifest("reg/app", "v1", imageManifest(t, config, layer)); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}

	assertUsage(t, "after insert error", db.usage("reg"), int64(len(config)+len(layer)), 0)
}

func TestQuotaManifestRaceFollowsWhatTheInsertDid(t *testing.T) {
	config := []byte(`{}`)
	body := imageManifest(t, config)

	t.Run("another push created the row first", func(t *testing.T) {
		g, db := quotaRegistry(t, 0, "reg")
		g.mustPushBlob("reg/app", config)

		// The lookup finds no manifest, the insert then reports a refresh.
		notInserted := false
		db.insertedOverride = &notInserted

		g.mustPutManifest("reg/app", "v1", body)
		assertUsage(t, "reservation returned", db.usage("reg"), int64(len(config)), 0)
	})

	t.Run("the row the lookup saw was removed", func(t *testing.T) {
		g, db := quotaRegistry(t, 0, "reg")
		g.mustPushBlob("reg/app", config)
		g.mustPutManifest("reg/app", "v1", body)

		// The lookup finds the manifest (nothing reserved), the insert then
		// creates a new row: it is charged after the fact.
		inserted := true
		db.insertedOverride = &inserted

		g.mustPutManifest("reg/app", "v1", body)
		assertUsage(t, "re-created row charged", db.usage("reg"), int64(len(config)+2*len(body)), 2)
	})
}

func TestQuotaDeleteManifestByDigestReleasesManifestOnly(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	config, layer := []byte(`{}`), []byte(strings.Repeat("l", 32))
	g.mustPushBlob("reg/app", config)
	g.mustPushBlob("reg/app", layer)

	body := imageManifest(t, config, layer)
	blobs := int64(len(config) + len(layer))
	g.mustPutManifest("reg/app", "v1", body)

	// A tag delete keeps the manifest, so it releases nothing.
	if rec := g.do(http.MethodDelete, "/v2/reg/app/manifests/v1", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("tag delete = %d", rec.Code)
	}

	assertUsage(t, "after tag delete", db.usage("reg"), blobs+int64(len(body)), 1)

	// Deleting by digest releases the manifest; the layers stay until GC.
	byDigest := "/v2/reg/app/manifests/" + digestOf([]byte(body))
	if rec := g.do(http.MethodDelete, byDigest, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("manifest delete = %d; body=%s", rec.Code, rec.Body.String())
	}

	assertUsage(t, "after manifest delete", db.usage("reg"), blobs, 0)

	// The second delete finds nothing and must not release again.
	if rec := g.do(http.MethodDelete, byDigest, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}

	assertUsage(t, "after second delete", db.usage("reg"), blobs, 0)
}

func TestQuotaDeleteManifestRaceReleasesOnce(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	config := []byte(`{}`)
	g.mustPushBlob("reg/app", config)

	body := imageManifest(t, config)
	g.mustPutManifest("reg/app", "v1", body)

	// A concurrent request removed the row after this one looked it up: the
	// database reports no row, so this request releases nothing.
	gone := &goneManifestDB{regFakeDB: db}
	g.h.db = gone

	if rec := g.do(http.MethodDelete, "/v2/reg/app/manifests/"+digestOf([]byte(body)), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}

	assertUsage(t, "usage untouched", db.usage("reg"), int64(len(config)+len(body)), 1)
}

// goneManifestDB loses every manifest delete race: the row is there for the
// lookup and gone for the delete.
type goneManifestDB struct{ *regFakeDB }

func (d *goneManifestDB) DeleteDockerManifest(context.Context, string, string, string) error {
	return database.ErrDockerRowNotFound
}

func TestQuotaDeleteBlobReleasesBytesOnce(t *testing.T) {
	g, db := quotaRegistry(t, 0, "reg")
	layer := []byte(strings.Repeat("l", 48))
	g.mustPushBlob("reg/app", layer)

	target := "/v2/reg/app/blobs/" + digestOf(layer)
	if rec := g.do(http.MethodDelete, target, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("blob delete = %d; body=%s", rec.Code, rec.Body.String())
	}

	assertUsage(t, "after blob delete", db.usage("reg"), 0, 0)

	// A second delete is refused before the database sees it; a delete that
	// loses the race to remove the row (the database reports none) releases
	// nothing either.
	if rec := g.do(http.MethodDelete, target, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", rec.Code)
	}

	g.mustPushBlob("reg/app", layer)
	g.h.db = &goneBlobDB{regFakeDB: db}

	if rec := g.do(http.MethodDelete, target, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("raced delete = %d, want 404", rec.Code)
	}

	assertUsage(t, "after raced delete", db.usage("reg"), int64(len(layer)), 0)
}

// goneBlobDB loses every blob delete race.
type goneBlobDB struct{ *regFakeDB }

func (d *goneBlobDB) DeleteDockerBlob(context.Context, string, string) error {
	return database.ErrDockerRowNotFound
}

// mount POSTs a cross-repository mount of layer into dst/<image> from src/app.
func (g *registry) mount(image string, layer []byte) *httptest.ResponseRecorder {
	g.t.Helper()

	return g.do(http.MethodPost, "/v2/dst/"+image+"/blobs/uploads/?mount="+digestOf(layer)+"&from=src/app", "")
}

func TestQuotaMountMetersTargetRepositoryOnce(t *testing.T) {
	g, db := quotaRegistry(t, 0, "dst", "src")
	g.h.SetAuthorizer(&fakeAuthz{allow: true})

	layer := []byte(strings.Repeat("m", 40))
	g.mustPushBlob("src/app", layer)

	if rec := g.mount("app", layer); rec.Code != http.StatusCreated {
		t.Fatalf("mount = %d; body=%s", rec.Code, rec.Body.String())
	}

	assertUsage(t, "dst after mount", db.usage("dst"), int64(len(layer)), 0)
	assertUsage(t, "src untouched", db.usage("src"), int64(len(layer)), 0)

	// Mounting it again finds the row in dst.
	if rec := g.mount("app", layer); rec.Code != http.StatusCreated {
		t.Fatalf("second mount = %d", rec.Code)
	}

	assertUsage(t, "dst after second mount", db.usage("dst"), int64(len(layer)), 0)
}

func TestQuotaMountOverQuotaAnswers507(t *testing.T) {
	g, db := quotaRegistry(t, 50, "dst", "src")
	g.h.SetAuthorizer(&fakeAuthz{allow: true})

	layer := []byte(strings.Repeat("m", 40))
	g.mustPushBlob("src/app", layer)
	// dst has room for the layer alone, but already holds another blob.
	g.mustPushBlob("dst/app", []byte(strings.Repeat("o", 20)))

	assertDenied(t, g.mount("app", layer))
	assertUsage(t, "dst after refused mount", db.usage("dst"), 20, 0)

	if db.blobs[scoped(db.repos["dst"].ID, digestOf(layer))] != nil {
		t.Error("a refused mount must not link the blob")
	}
}

func TestQuotaMountOfBlobDstAlreadyHoldsReservesNothing(t *testing.T) {
	g, db := quotaRegistry(t, 0, "dst", "src")
	g.h.SetAuthorizer(&fakeAuthz{allow: true})

	layer := []byte(strings.Repeat("m", 40))
	g.mustPushBlob("src/app", layer)
	g.mustPushBlob("dst/one", layer)

	// dst holds the row, but image "two" is not linked to it: the mount goes
	// through the source and the insert refreshes the existing row.
	db.upsertBlobs = true

	if rec := g.mount("two", layer); rec.Code != http.StatusCreated {
		t.Fatalf("mount = %d; body=%s", rec.Code, rec.Body.String())
	}

	assertUsage(t, "dst", db.usage("dst"), int64(len(layer)), 0)
}

func TestQuotaStoreFailureReleasesBlobReservation(t *testing.T) {
	db := &fakeDB{repo: &models.Repository{ID: "repo-1", Name: "reg", Format: "docker"}, createBlobErr: errCreateFailed}
	h, _ := newTestHandler(t, db)

	layer := []byte("layer-bytes")
	blob := &models.DockerBlob{RepositoryID: "repo-1", Digest: digestOf(layer), Size: int64(len(layer))}

	err := h.storeNewBlob(context.Background(), blob, strings.TrimPrefix(digestOf(layer), "sha256:"))
	if !errors.Is(err, errCreateFailed) {
		t.Fatalf("storeNewBlob error = %v, want the insert failure", err)
	}

	assertUsage(t, "after failed insert", db.ledger.of("repo-1"), 0, 0)
}
