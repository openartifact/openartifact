// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cargo

import (
	"net/http"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/presign"
)

// redirectRecorder is a blob store that "redirects" every download and records
// what it was asked to presign.
type redirectRecorder struct {
	*formattest.BlobStore

	got presign.Blob
}

func (r *redirectRecorder) TryRedirect(w http.ResponseWriter, _ *http.Request, blob presign.Blob) bool {
	r.got = blob
	w.WriteHeader(http.StatusTemporaryRedirect)

	return true
}

// The crate row already knows the blob size; the presign path must be handed
// it instead of asking the object store (a HEAD request) for it.
func TestDownloadPresignUsesStoredSize(t *testing.T) {
	db := &cargoDB{DB: formattest.NewDB()}
	db.crates = []*models.CargoCrate{{Name: "demo", Version: "0.1.0", BlobKey: "abc", Checksum: "abc", SizeBytes: 4242}}

	store := &redirectRecorder{BlobStore: formattest.NewBlobStore()}
	h := NewHTTPHandler(formattest.NewLogger(t), db, store, "http://localhost")

	w := formattest.Do(h.RegisterRepoRoutes, formattest.Request(http.MethodGet, "/repos/myrepo/api/v1/crates/demo/0.1.0/download", nil))
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", w.Code)
	}

	if store.got.SHA256 != "abc" || store.got.Size != 4242 {
		t.Errorf("presign.Blob = {SHA256 %q, Size %d}, want {abc, 4242}", store.got.SHA256, store.got.Size)
	}
}
