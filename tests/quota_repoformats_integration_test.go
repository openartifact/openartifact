//go:build integration

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	formatconan "github.com/openartifact/openartifact/internal/format/conan"
	formatconda "github.com/openartifact/openartifact/internal/format/conda"
	formativy "github.com/openartifact/openartifact/internal/format/ivy"
	formatraw "github.com/openartifact/openartifact/internal/format/raw"
	formatsbt "github.com/openartifact/openartifact/internal/format/sbt"
	"github.com/openartifact/openartifact/internal/storage"
)

// qrfNoise returns incompressible bytes whose content (and so sha256) depends
// on seed, so differently seeded payloads never share a blob.
func qrfNoise(seed, size int) []byte {
	b := make([]byte, size)
	x := uint32(seed)*2654435761 + 1

	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}

	return b
}

// qrfMount serves a hosted format handler the way cmd/server does: relative
// routes under /repos/{name}.
func qrfMount(register func(*testing.T, *database.PostgresDB, storage.BlobStore) func(chi.Router)) func(*testing.T, *database.PostgresDB, storage.BlobStore) http.Handler {
	return func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler {
		t.Helper()

		router := chi.NewRouter()
		router.Route("/repos/{name}", register(t, pg, bs))

		return router
	}
}

var (
	//nolint:gochecknoglobals // immutable mount function used by the table-driven test
	qrfMountConda = qrfMount(func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router) {
		t.Helper()

		return formatconda.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost").RegisterRepoRoutes
	})
	//nolint:gochecknoglobals // immutable mount function used by the table-driven test
	qrfMountConan = qrfMount(func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router) {
		t.Helper()

		return formatconan.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost").RegisterRepoRoutes
	})
	//nolint:gochecknoglobals // immutable mount function used by the table-driven test
	qrfMountIvy = qrfMount(func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router) {
		t.Helper()

		return formativy.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost").RegisterRepoRoutes
	})
	//nolint:gochecknoglobals // immutable mount function used by the table-driven test
	qrfMountSBT = qrfMount(func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router) {
		t.Helper()

		return formatsbt.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost").RegisterRepoRoutes
	})
	//nolint:gochecknoglobals // immutable mount function used by the table-driven test
	qrfMountRaw = qrfMount(func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) func(chi.Router) {
		t.Helper()

		return formatraw.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost").RegisterRepoRoutes
	})
)

// qrfFormat describes how to store and remove the n-th package of a format
// through the real handler. upload sends noise(seed, size) under the
// coordinates of package n; the quota must account for exactly size bytes.
type qrfFormat struct {
	format string
	mount  func(*testing.T, *database.PostgresDB, storage.BlobStore) http.Handler
	upload func(e *qrEnv, n, seed, size int) *httptest.ResponseRecorder
	remove func(e *qrEnv, n int) *httptest.ResponseRecorder

	// overwrite reports whether storing different bytes under the coordinates
	// of an existing package replaces it (conda refuses with 409).
	overwrite bool
}

func qrfConda() qrfFormat {
	file := func(n int) string { return fmt.Sprintf("qrpkg%d-1.0-py_0.tar.bz2", n) }

	return qrfFormat{
		format: "conda",
		mount:  qrfMountConda,
		upload: func(e *qrEnv, n, seed, size int) *httptest.ResponseRecorder {
			return e.do(e.request(http.MethodPost, "/repos/"+e.repoName+"/linux-64?filename="+file(n), qrfNoise(seed, size), "application/octet-stream"))
		},
		remove: func(e *qrEnv, n int) *httptest.ResponseRecorder {
			return e.do(e.request(http.MethodDelete, "/repos/"+e.repoName+"/linux-64/"+file(n), nil, ""))
		},
	}
}

const qrfConanRev = "9b4de2c1f0e8a7d65c3b2a1908f7e6d5"

func qrfConanRecipe(n int) string { return fmt.Sprintf("/conans/zlib/1.%d/_/_", n) }

func qrfConan() qrfFormat {
	return qrfFormat{
		format: "conan",
		mount:  qrfMountConan,
		upload: func(e *qrEnv, n, seed, size int) *httptest.ResponseRecorder {
			return e.do(e.request(http.MethodPut, "/repos/"+e.repoName+"/v2"+qrfConanRecipe(n)+"/revisions/"+qrfConanRev+"/files/conanfile.py", qrfNoise(seed, size), "application/octet-stream"))
		},
		remove: func(e *qrEnv, n int) *httptest.ResponseRecorder {
			return e.do(e.request(http.MethodDelete, "/repos/"+e.repoName+"/v2"+qrfConanRecipe(n), nil, ""))
		},
		overwrite: true,
	}
}

func qrfIvy() qrfFormat {
	path := func(n int) string { return fmt.Sprintf("/repos/qr-ivy/org.example/mod%d/1.0/mod%d-1.0.jar", n, n) }

	return qrfFormat{
		format: "ivy",
		mount:  qrfMountIvy,
		upload: func(e *qrEnv, n, seed, size int) *httptest.ResponseRecorder {
			return e.do(e.request(http.MethodPut, path(n), qrfNoise(seed, size), "application/java-archive"))
		},
		remove: func(e *qrEnv, n int) *httptest.ResponseRecorder {
			return e.do(e.request(http.MethodDelete, path(n), nil, ""))
		},
		overwrite: true,
	}
}

func qrfSBT() qrfFormat {
	path := func(n int) string { return fmt.Sprintf("/repos/qr-sbt/org.example/mod%d/1.0/mod%d-1.0.jar", n, n) }

	return qrfFormat{
		format: "sbt",
		mount:  qrfMountSBT,
		upload: func(e *qrEnv, n, seed, size int) *httptest.ResponseRecorder {
			return e.do(e.request(http.MethodPut, path(n), qrfNoise(seed, size), "application/java-archive"))
		},
		remove: func(e *qrEnv, n int) *httptest.ResponseRecorder {
			return e.do(e.request(http.MethodDelete, path(n), nil, ""))
		},
		overwrite: true,
	}
}

func qrfRaw() qrfFormat {
	path := func(n int) string { return fmt.Sprintf("/repos/qr-raw/dir/file%d.bin", n) }

	return qrfFormat{
		format: "raw",
		mount:  qrfMountRaw,
		upload: func(e *qrEnv, n, seed, size int) *httptest.ResponseRecorder {
			return e.do(e.request(http.MethodPut, path(n), qrfNoise(seed, size), "application/octet-stream"))
		},
		remove: func(e *qrEnv, n int) *httptest.ResponseRecorder {
			return e.do(e.request(http.MethodDelete, path(n), nil, ""))
		},
		overwrite: true,
	}
}

func qrfFormats() []qrfFormat {
	return []qrfFormat{qrfConda(), qrfConan(), qrfIvy(), qrfSBT(), qrfRaw()}
}

// rfWantReconciled recomputes the counters from the package tables and asserts
// they equal the incrementally maintained ones.
func (e *qrEnv) rfWantReconciled(step string) {
	e.t.Helper()

	e.wantNoPending(step)

	size, count := e.usage()

	q, err := e.pg.ReconcileStorageUsage(context.Background(), e.repoID)
	if err != nil {
		e.t.Fatalf("%s: ReconcileStorageUsage: %v", step, err)
	}

	if q.CurrentSizeBytes != size || q.CurrentArtifactCount != count {
		e.t.Fatalf("%s: reconcile = (%d bytes, %d artifacts), incremental = (%d, %d)", step, q.CurrentSizeBytes, q.CurrentArtifactCount, size, count)
	}
}

func (e *qrEnv) rfSetLimit(maxBytes int64) {
	e.t.Helper()

	ctx := context.Background()

	q, err := e.pg.GetOrCreateStorageQuota(ctx, e.repoID)
	if err != nil {
		e.t.Fatalf("GetOrCreateStorageQuota: %v", err)
	}

	q.MaxSizeBytes = maxBytes
	if err := e.pg.UpdateStorageQuota(ctx, q); err != nil {
		e.t.Fatalf("UpdateStorageQuota: %v", err)
	}
}

func (e *qrEnv) rfWantQuotaExceeded(step string, rec *httptest.ResponseRecorder) {
	e.t.Helper()

	e.wantStatus(step, rec, http.StatusInsufficientStorage)

	var body struct {
		Code string `json:"code"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "quota_exceeded" {
		e.t.Fatalf("%s: body %q, want code quota_exceeded (err %v)", step, rec.Body.String(), err)
	}
}

// TestQuotaRepoFormats proves conda, conan, ivy, sbt and raw uploads reserve
// one artifact of the stored bytes, refuse over-quota uploads with 507, do not
// leak on repeated or refused uploads, move the usage on an overwrite, return
// it on delete and stay equal to a reconcile.
func TestQuotaRepoFormats(t *testing.T) {
	pg := freshPostgres(t, 10)
	pg.SetTrashRetentionDays(0) // hard delete: the delete handlers release immediately

	const (
		sizeA = 300
		sizeB = 900
	)

	for _, f := range qrfFormats() {
		t.Run(f.format, func(t *testing.T) {
			e := newQREnv(t, pg, f.format, f.mount)
			e.wantUsage("fresh repository", 0, 0)

			e.wantStatus("upload A", f.upload(e, 1, 1, sizeA), http.StatusOK, http.StatusCreated)
			e.wantStatus("upload B", f.upload(e, 2, 2, sizeB), http.StatusOK, http.StatusCreated)
			e.wantUsage("after two uploads", sizeA+sizeB, 2)
			e.rfWantReconciled("after two uploads")

			// The same bytes again must not double count (conda answers 409).
			e.wantStatus("identical re-upload", f.upload(e, 1, 1, sizeA), http.StatusOK, http.StatusCreated, http.StatusConflict)
			e.wantUsage("after identical re-upload", sizeA+sizeB, 2)
			e.rfWantReconciled("after identical re-upload")

			// A limit with 50 bytes of headroom refuses the next package.
			e.rfSetLimit(sizeA + sizeB + 50)
			e.rfWantQuotaExceeded("over-quota upload", f.upload(e, 3, 3, 2000))
			e.wantUsage("after refused over-quota upload", sizeA+sizeB, 2)
			e.rfWantReconciled("after refused over-quota upload")

			e.rfSetLimit(0)

			if f.overwrite {
				const newSizeA = 500

				e.wantStatus("overwrite A", f.upload(e, 1, 9, newSizeA), http.StatusOK, http.StatusCreated)
				e.wantUsage("after overwriting A", newSizeA+sizeB, 2)
				e.rfWantReconciled("after overwriting A")

				// A replacement that does not fit changes nothing.
				e.rfSetLimit(newSizeA + sizeB + 50)
				e.rfWantQuotaExceeded("over-quota overwrite", f.upload(e, 1, 10, 5000))
				e.wantUsage("after refused overwrite", newSizeA+sizeB, 2)
				e.rfWantReconciled("after refused overwrite")

				e.rfSetLimit(0)
			} else {
				e.wantStatus("overwrite A", f.upload(e, 1, 9, 500), http.StatusConflict)
				e.wantUsage("after refused overwrite", sizeA+sizeB, 2)
			}

			e.wantStatus("delete A", f.remove(e, 1), http.StatusOK, http.StatusNoContent)
			e.wantUsage("after deleting A", sizeB, 1)
			e.rfWantReconciled("after deleting A")

			e.wantStatus("delete B", f.remove(e, 2), http.StatusOK, http.StatusNoContent)
			e.wantUsage("after deleting B", 0, 0)
			e.rfWantReconciled("after deleting B")

			// The freed headroom is real: the package that was refused fits now.
			e.rfSetLimit(2100)
			e.wantStatus("upload C after delete", f.upload(e, 3, 3, 2000), http.StatusOK, http.StatusCreated)
			e.wantUsage("after upload C", 2000, 1)
			e.rfWantReconciled("after upload C")

			e.wantStatus("delete C", f.remove(e, 3), http.StatusOK, http.StatusNoContent)
			e.wantUsage("after deleting C", 0, 0)
			e.rfWantReconciled("after deleting C")
		})
	}
}

// TestQuotaConanMultiFileRelease shows every file row of a conan recipe is its
// own artifact: a revision delete (rows and package files in one call) gives
// back all of them and the recipe delete the rest.
func TestQuotaConanMultiFileRelease(t *testing.T) {
	pg := freshPostgres(t, 10)
	pg.SetTrashRetentionDays(0)

	e := newQREnv(t, pg, "conan", qrfMountConan)

	const (
		rrev1 = qrfConanRev
		rrev2 = "0aa1bb2cc3dd4ee5ff6a7b8c9d0e1f2a"
		pid   = "d4f0ab9e8c7b6a5948372615f0e1d2c3b4a59687"
		prev  = "5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
	)

	recipe := "/repos/" + e.repoName + "/v2" + qrfConanRecipe(7)

	var total1, total2 int64

	put := func(path string, seed, size int) int64 {
		t.Helper()

		e.wantStatus("put "+path, e.do(e.request(http.MethodPut, path, qrfNoise(seed, size), "application/octet-stream")), http.StatusCreated)

		return int64(size)
	}

	seed := 100

	for _, f := range []string{"conan_export.tgz", "conan_sources.tgz", "conanfile.py", "conanmanifest.txt"} {
		seed++
		total1 += put(recipe+"/revisions/"+rrev1+"/files/"+f, seed, 100+seed)
	}

	for _, f := range []string{"conan_package.tgz", "conaninfo.txt", "conanmanifest.txt"} {
		seed++
		total1 += put(recipe+"/revisions/"+rrev1+"/packages/"+pid+"/revisions/"+prev+"/files/"+f, seed, 100+seed)
	}

	for _, f := range []string{"conanfile.py", "conanmanifest.txt"} {
		seed++
		total2 += put(recipe+"/revisions/"+rrev2+"/files/"+f, seed, 100+seed)
	}

	e.wantUsage("after publishing", total1+total2, 9)
	e.rfWantReconciled("after publishing")

	e.wantStatus("delete revision 1", e.do(e.request(http.MethodDelete, recipe+"/revisions/"+rrev1, nil, "")), http.StatusOK)
	e.wantUsage("after deleting revision 1", total2, 2)
	e.rfWantReconciled("after deleting revision 1")

	e.wantStatus("delete recipe", e.do(e.request(http.MethodDelete, recipe, nil, "")), http.StatusOK)
	e.wantUsage("after deleting the recipe", 0, 0)
	e.rfWantReconciled("after deleting the recipe")
}
