// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nuget

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
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

// pushSample pushes the sample package and returns the response and the
// package size the handler should have metered.
func pushSample(t *testing.T, h *HTTPHandler) (*httptest.ResponseRecorder, int64) {
	t.Helper()

	nupkg := createNupkg(t, "Newtonsoft.Json.nuspec", sampleNuspec)

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("package", "x.nupkg")
	_, _ = fw.Write(nupkg)
	_ = mw.Close()

	req := formattest.Request(http.MethodPut, "/repos/myrepo/v3/package", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return formattest.Do(register(h), req), int64(len(nupkg))
}

func TestPushReservesQuotaAndDeleteReleasesIt(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	w, size := pushSample(t, h)
	if w.Code != http.StatusCreated {
		t.Fatalf("push status = %d: %s", w.Code, w.Body.String())
	}

	if db.used != size || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d, want %d/1", db.used, db.artifacts, size)
	}

	if w := formattest.Do(
		register(h),
		formattest.Request(http.MethodDelete, "/repos/myrepo/v3/package/Newtonsoft.Json/13.0.3", nil),
	); w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", w.Code)
	}

	if db.used != 0 || db.artifacts != 0 {
		t.Errorf("quota after delete used=%d artifacts=%d, want 0/0", db.used, db.artifacts)
	}
}

func TestPushOverQuotaReturns507AndPersistsNothing(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)
	db.limit = 10

	w, _ := pushSample(t, h)
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), `"quota_exceeded"`) {
		t.Fatalf("status=%d body=%q, want 507 quota_exceeded", w.Code, w.Body.String())
	}

	if db.pkg != nil || len(bs.Blobs) != 0 || len(pub.Events()) != 0 || db.used != 0 || db.artifacts != 0 {
		t.Errorf(
			"pkg=%v blobs=%d events=%d used=%d artifacts=%d; want nothing persisted",
			db.pkg,
			len(bs.Blobs),
			len(pub.Events()),
			db.used,
			db.artifacts,
		)
	}
}

func TestPushDuplicateDoesNotConsumeQuota(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	w, size := pushSample(t, h)
	if w.Code != http.StatusCreated {
		t.Fatalf("first push status = %d", w.Code)
	}

	if w, _ := pushSample(t, h); w.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, want 409", w.Code)
	}

	if db.used != size || db.artifacts != 1 {
		t.Errorf("quota used=%d artifacts=%d after duplicate, want %d/1", db.used, db.artifacts, size)
	}
}

func TestPushCreateFailureReleasesQuotaAndBlob(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"duplicate race": {database.ErrDuplicate, http.StatusConflict},
		"other failure":  {errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			h, db, bs, _ := newTestHandler(t)
			db.createErr = tc.err

			if w, _ := pushSample(t, h); w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}

			if db.used != 0 || db.artifacts != 0 || len(bs.Blobs) != 0 {
				t.Errorf("used=%d artifacts=%d blobs=%d; want all released", db.used, db.artifacts, len(bs.Blobs))
			}
		})
	}
}
