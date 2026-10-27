// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
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

var errCreateFailed = errors.New("create failed")

// failingCreateDB makes the row insert fail.
type failingCreateDB struct{ *conanDB }

func (failingCreateDB) CreateConanPackage(context.Context, *models.ConanPackage) error {
	return errCreateFailed
}

// recipeBytes is the total size of the files publishRecipe uploads for rrev.
func recipeBytes(rrev string) int64 {
	var n int64

	for _, f := range []string{"conan_export.tgz", "conan_sources.tgz", "conanfile.py", "conanmanifest.txt"} {
		n += int64(len(f + "@" + rrev))
	}

	return n
}

func TestUploadReservesQuotaPerFile(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)

	if e.db.used != recipeBytes(rrev1) || e.db.artifacts != 4 {
		t.Errorf("quota used=%d artifacts=%d, want %d/4", e.db.used, e.db.artifacts, recipeBytes(rrev1))
	}
}

func TestUploadOverQuotaReturns507AndPersistsNothing(t *testing.T) {
	e := newEnv(t)
	e.db.limit = 3

	w := e.do(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "data")
	if w.Code != http.StatusInsufficientStorage {
		t.Fatalf("status = %d, want 507", w.Code)
	}

	if e.db.count() != 0 || len(e.bs.Blobs) != 0 || len(e.pub.Events()) != 0 || e.db.used != 0 || e.db.artifacts != 0 {
		t.Errorf(
			"rows=%d blobs=%d events=%d used=%d artifacts=%d; want nothing persisted",
			e.db.count(),
			len(e.bs.Blobs),
			len(e.pub.Events()),
			e.db.used,
			e.db.artifacts,
		)
	}
}

func TestIdenticalReuploadDoesNotConsumeQuota(t *testing.T) {
	e := newEnv(t)

	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "data", http.StatusCreated)
	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "data", http.StatusCreated)

	if e.db.used != 4 || e.db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after identical re-upload, want 4/1", e.db.used, e.db.artifacts)
	}
}

func TestOverwriteReleasesPreviousSize(t *testing.T) {
	e := newEnv(t)

	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "first", http.StatusCreated)
	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "second-longer", http.StatusCreated)

	if want := int64(len("second-longer")); e.db.used != want || e.db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after overwrite, want %d/1", e.db.used, e.db.artifacts, want)
	}
}

func TestOverwriteOverQuotaKeepsPreviousUsage(t *testing.T) {
	e := newEnv(t)
	e.db.limit = 6

	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "first", http.StatusCreated)
	e.mustDo(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), "second-longer", http.StatusInsufficientStorage)

	if e.db.used != 5 || e.db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after refused overwrite, want 5/1", e.db.used, e.db.artifacts)
	}
}

func TestDeleteReleasesEveryDoomedFile(t *testing.T) {
	e := newEnv(t)

	e.publishRecipe(rrev1)
	e.publishPackage(rrev1, pid, prev1)
	e.publishRecipe(rrev2)

	e.mustDo(http.MethodDelete, recipeURL+"/revisions/"+rrev1, "", http.StatusOK)

	if e.db.used != recipeBytes(rrev2) || e.db.artifacts != 4 {
		t.Errorf("quota after deleting revision 1 used=%d artifacts=%d, want %d/4", e.db.used, e.db.artifacts, recipeBytes(rrev2))
	}

	e.mustDo(http.MethodDelete, recipeURL, "", http.StatusOK)

	if e.db.used != 0 || e.db.artifacts != 0 {
		t.Errorf("quota after deleting the recipe used=%d artifacts=%d, want 0/0", e.db.used, e.db.artifacts)
	}
}

func TestUploadCreateFailureReleasesQuota(t *testing.T) {
	db := newConanDB()
	h := NewHTTPHandler(formattest.NewLogger(t), &failingCreateDB{conanDB: db}, formattest.NewBlobStore(), "http://localhost")

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodPut, recipeFileURL(rrev1, "conanfile.py"), nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}

	if db.used != 0 || db.artifacts != 0 {
		t.Errorf("quota not released: used=%d artifacts=%d", db.used, db.artifacts)
	}
}
