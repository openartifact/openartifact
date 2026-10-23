// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package metrics

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestCountUpload(t *testing.T) {
	r := NewRegistryOn(prometheus.NewRegistry())

	for _, f := range UploadFormats {
		r.CountUpload(f, UploadSuccess)
		r.CountUpload(f, UploadError)
		r.CountUpload(f, UploadError)
	}

	for _, f := range UploadFormats {
		if got := testutil.ToFloat64(r.UploadsTotal.WithLabelValues(f, UploadSuccess)); got != 1 {
			t.Errorf("%s success = %v, want 1", f, got)
		}

		if got := testutil.ToFloat64(r.UploadsTotal.WithLabelValues(f, UploadError)); got != 2 {
			t.Errorf("%s error = %v, want 2", f, got)
		}
	}

	// Label values outside the closed sets are folded, never new series.
	r.CountUpload("my-private-format", "rejected")

	if got := testutil.ToFloat64(r.UploadsTotal.WithLabelValues("other", UploadError)); got != 1 {
		t.Errorf("unknown format/result = %v, want 1 in other/error", got)
	}

	if n := testutil.CollectAndCount(r.UploadsTotal); n != 2*len(UploadFormats)+1 {
		t.Errorf("series = %d, want %d", n, 2*len(UploadFormats)+1)
	}
}

func TestCountUploadNilRegistry(t *testing.T) {
	var r *Registry

	r.CountUpload(FormatRaw, UploadSuccess) // must not panic
}

func TestUploadResult(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusOK:                  UploadSuccess,
		http.StatusCreated:             UploadSuccess,
		http.StatusNoContent:           UploadSuccess,
		http.StatusTemporaryRedirect:   UploadSuccess,
		http.StatusBadRequest:          UploadError,
		http.StatusConflict:            UploadError,
		http.StatusInternalServerError: UploadError,
	} {
		if got := UploadResult(status); got != want {
			t.Errorf("UploadResult(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestUploadFormatsUnique(t *testing.T) {
	seen := map[string]bool{}

	for _, f := range UploadFormats {
		if seen[f] {
			t.Errorf("duplicate upload format %q", f)
		}

		seen[f] = true
	}
}
