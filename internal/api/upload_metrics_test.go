// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The presigned completion counts under the raw format, with the same
// success/error results as every other upload.
func TestPresignedUpload_CompleteIsCounted(t *testing.T) {
	reg := metrics.NewRegistryOn(prometheus.NewRegistry())

	t.Run("success", func(t *testing.T) {
		f := newPresignUploadFixture(t, "raw", "hosted")
		f.handler.reg = reg
		content := []byte("hello presigned world")
		resp := f.start(t, content)
		f.store.staged[resp.ID] = content

		if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusCreated {
			t.Fatalf("complete status = %d", rw.Code)
		}

		// A retried complete returns the first result and is not a new upload.
		if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusOK {
			t.Fatalf("repeat status = %d", rw.Code)
		}

		if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues("raw", "success")); got != 1 {
			t.Errorf("raw success = %v, want 1", got)
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		f := newPresignUploadFixture(t, "raw", "hosted")
		f.handler.reg = reg
		resp := f.start(t, []byte("the declared content"))
		f.store.staged[resp.ID] = []byte("short")

		if rw := f.call(t, false, completePath(resp.ID), nil); rw.Code != http.StatusUnprocessableEntity {
			t.Fatalf("complete status = %d", rw.Code)
		}

		if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues("raw", "error")); got != 1 {
			t.Errorf("raw error = %v, want 1", got)
		}
	})

	// Only success and error exist: the former rejected/failure/aborted
	// results are gone.
	if n := testutil.CollectAndCount(reg.UploadsTotal); n != 2 {
		t.Errorf("upload series = %d, want 2", n)
	}
}

// A rejected multipart upload to the API deb endpoint is counted as an error.
func TestDebUploadRejectionIsCounted(t *testing.T) {
	reg := metrics.NewRegistryOn(prometheus.NewRegistry())
	h := NewDebUploadHandler(nil, &debAPIDB{DB: formattest.NewDB()}, nil, reg)

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("distribution", "stable") // no file part
	_ = mw.Close()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/repositories/apt/debs", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	rw := httptest.NewRecorder()
	h.handleUpload(rw, req)

	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rw.Code)
	}

	if got := testutil.ToFloat64(reg.UploadsTotal.WithLabelValues("deb", "error")); got != 1 {
		t.Errorf("deb error = %v, want 1", got)
	}
}
