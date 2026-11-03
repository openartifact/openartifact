// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package docker

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/pkg/logger"
)

// otherReplica is a second server process of the same deployment: its own
// handler (and so its own in-process state) over the database and blob store g
// uses, like a replica behind a round-robin ingress.
func otherReplica(t *testing.T, g *registry, db registryStore) *registry {
	t.Helper()

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	h := NewHTTPHandler(log, db, g.h.blobStore)
	r := chi.NewRouter()
	h.RegisterRoutes(r)

	return &registry{t: t, h: h, router: r}
}

// A chunked push whose requests alternate between replicas that share nothing
// but the database and the blob store completes, and the blob it stores is the
// bytes the client sent, in order.
func TestChunkedUpload_AlternatesBetweenReplicas(t *testing.T) {
	db := newRegFakeDB("reg")
	a := newRegistry(t, db)
	b := otherReplica(t, a, db)

	chunks := []string{"first chunk, ", "second chunk, ", "third chunk, ", "the final body"}
	layer := []byte(strings.Join(chunks, ""))

	loc := startUpload(t, a, "reg/app") // POST on A
	offset := 0

	// PATCH 1 on B, PATCH 2 on A, PATCH 3 on B; each checks the other replica's
	// view of the session and the Range/Location headers of the spec.
	for i, replicas := range [][2]*registry{{b, a}, {a, b}, {b, a}} {
		rec := replicas[0].do(http.MethodPatch, loc, chunks[i])
		if rec.Code != http.StatusAccepted {
			t.Fatalf("PATCH %d = %d; body=%s", i+1, rec.Code, rec.Body.String())
		}

		offset += len(chunks[i])

		wantRange := "0-" + strconv.Itoa(offset-1)
		if got := rec.Header().Get("Range"); got != wantRange {
			t.Fatalf("PATCH %d Range = %q, want %q", i+1, got, wantRange)
		}

		if got := rec.Header().Get("Location"); got != loc {
			t.Fatalf("PATCH %d Location = %q, want %q", i+1, got, loc)
		}

		status := replicas[1].do(http.MethodGet, loc, "") // GET on the other replica
		if status.Code != http.StatusNoContent || status.Header().Get("Range") != wantRange {
			t.Fatalf("status on the other replica = %d Range %q, want 204 %q", status.Code, status.Header().Get("Range"), wantRange)
		}
	}

	// A chunk that restarts at a stale offset is refused on whichever replica.
	stale := a.do(http.MethodPatch, loc, "again", "Content-Range", "0-4")
	if stale.Code != http.StatusRequestedRangeNotSatisfiable || stale.Header().Get("Range") != "0-"+strconv.Itoa(offset-1) {
		t.Fatalf("stale PATCH = %d Range %q, want 416 with the current range", stale.Code, stale.Header().Get("Range"))
	}

	// PUT on the replica that took the POST, after PATCHes on the other.
	rec := a.do(http.MethodPut, loc+"?digest="+digestOf(layer), chunks[3])
	if rec.Code != http.StatusCreated {
		t.Fatalf("PUT = %d; body=%s", rec.Code, rec.Body.String())
	}

	for name, g := range map[string]*registry{"a": a, "b": b} {
		got := g.do(http.MethodGet, "/v2/reg/app/blobs/"+digestOf(layer), "")
		if got.Code != http.StatusOK || got.Body.String() != string(layer) {
			t.Fatalf("pull from replica %s = %d %q, want the pushed layer", name, got.Code, got.Body.String())
		}
	}

	if n := a.uploadCount(); n != 0 || len(db.uploads) != 0 {
		t.Fatalf("after completion: chunk objects = %d, sessions = %d, want none", n, len(db.uploads))
	}
}

// A completion and a cancel on a replica other than the one that took the
// chunks work too, and a cancel leaves no chunk object behind.
func TestChunkedUpload_CompleteAndCancelOnAnotherReplica(t *testing.T) {
	db := newRegFakeDB("reg")
	a := newRegistry(t, db)
	b := otherReplica(t, a, db)

	layer := []byte("uploaded through A, completed through B")
	loc := startUpload(t, a, "reg/app")

	if rec := a.do(http.MethodPatch, loc, string(layer[:10])); rec.Code != http.StatusAccepted {
		t.Fatalf("PATCH = %d", rec.Code)
	}

	if rec := b.do(http.MethodPut, loc+"?digest="+digestOf(layer), string(layer[10:])); rec.Code != http.StatusCreated {
		t.Fatalf("PUT on B = %d; body=%s", rec.Code, rec.Body.String())
	}

	loc = startUpload(t, b, "reg/app")
	if rec := b.do(http.MethodPatch, loc, "abandoned"); rec.Code != http.StatusAccepted {
		t.Fatalf("PATCH = %d", rec.Code)
	}

	if rec := a.do(http.MethodDelete, loc, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE on A = %d", rec.Code)
	}

	if n := a.uploadCount(); n != 0 {
		t.Fatalf("chunk objects after cancel = %d, want 0", n)
	}

	if rec := b.do(http.MethodGet, loc, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status after cancel = %d, want 404", rec.Code)
	}
}

// A digest mismatch found by one replica discards the chunks another replica
// stored.
func TestChunkedUpload_DigestMismatchDiscardsChunksOfOtherReplica(t *testing.T) {
	db := newRegFakeDB("reg")
	a := newRegistry(t, db)
	b := otherReplica(t, a, db)

	loc := startUpload(t, a, "reg/app")
	b.do(http.MethodPatch, loc, "chunk")

	rec := a.do(http.MethodPut, loc+"?digest="+digestOf([]byte("something else")), "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "DIGEST_INVALID") {
		t.Fatalf("PUT = %d %s, want 400 DIGEST_INVALID", rec.Code, rec.Body.String())
	}

	if n := a.uploadCount(); n != 0 || len(db.uploads) != 0 {
		t.Fatalf("leftovers: chunk objects = %d, sessions = %d", n, len(db.uploads))
	}
}

// What one replica stored is what another one completes: the upload object is
// in the blob store both share, listed under the session id.
func TestChunkedUpload_ReplicasShareTheUploadObject(t *testing.T) {
	db := newRegFakeDB("reg")
	a := newRegistry(t, db)
	b := otherReplica(t, a, db)

	loc := startUpload(t, a, "reg/app")
	a.do(http.MethodPatch, loc, "xyz")

	if ids := uploadObjects(t, b.h); len(ids) != 1 || ids[0] != uploadID(loc) {
		t.Fatalf("replica B sees uploads %v, want the one replica A wrote to (%s)", ids, uploadID(loc))
	}

	// Together with the session row it carries the whole state of the upload:
	// nothing the next request needs lives in the replica.
	row := db.uploads[scoped("id-reg", uploadID(loc))]
	if row.Offset != 3 || len(row.HashState) == 0 {
		t.Fatalf("session = offset %d, hash state %d bytes; want offset 3 and a digest state", row.Offset, len(row.HashState))
	}
}
