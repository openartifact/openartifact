//go:build integration

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formatcargo "github.com/openartifact/openartifact/internal/format/cargo"
	formatgomod "github.com/openartifact/openartifact/internal/format/gomod"
	formatnuget "github.com/openartifact/openartifact/internal/format/nuget"
	formatpypi "github.com/openartifact/openartifact/internal/format/pypi"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/worker"
)

// qpfNoise returns incompressible bytes whose content (and so sha256) depends
// on seed, so differently sized packages never share a blob.
func qpfNoise(seed, size int) []byte {
	b := make([]byte, size)
	x := uint32(seed)*2654435761 + 1

	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}

	return b
}

// qpfFormat describes how to upload (and, where the API has one, delete) the
// n-th package of a format through the real handler. upload returns the
// response and the bytes the quota must account for that package.
type qpfFormat struct {
	format string
	mount  func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler
	upload func(t *testing.T, e *qrEnv, n, pad int) (*httptest.ResponseRecorder, int64)
	remove func(e *qrEnv, n int) *httptest.ResponseRecorder // nil: no delete endpoint

	// duplicateStatus lists the statuses a repeated upload may answer with.
	duplicateStatus []int

	// table and kind identify the package rows for the trash purge path
	// (formats without a delete endpoint).
	table string
	kind  models.TrashKind
}

func qpfMount(register func(*testing.T, chi.Router, *database.PostgresDB, storage.BlobStore)) func(*testing.T, *database.PostgresDB, storage.BlobStore) http.Handler {
	return func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler {
		t.Helper()

		router := chi.NewRouter()
		register(t, router, pg, bs)

		return router
	}
}

// --- pypi --------------------------------------------------------------------

func qpfPyPIFile(n int) string { return fmt.Sprintf("qrpkg%d-1.0.tar.gz", n) }

func qpfPyPIUpload(t *testing.T, e *qrEnv, n, pad int) (*httptest.ResponseRecorder, int64) {
	t.Helper()

	data := qpfNoise(n, pad)

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", fmt.Sprintf("qrpkg%d", n))
	_ = mw.WriteField("version", "1.0")

	fw, err := mw.CreateFormFile("content", qpfPyPIFile(n))
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}

	_, _ = fw.Write(data)
	_ = mw.Close()

	return e.do(e.request(http.MethodPost, "/repos/"+e.repoName+"/legacy/", body.Bytes(), mw.FormDataContentType())), int64(len(data))
}

// --- nuget -------------------------------------------------------------------

func qpfNuGetID(n int) string { return fmt.Sprintf("Qr.Pkg%d", n) }

func qpfNuGetUpload(t *testing.T, e *qrEnv, n, pad int) (*httptest.ResponseRecorder, int64) {
	t.Helper()

	var nupkg bytes.Buffer

	zw := zip.NewWriter(&nupkg)

	w, err := zw.Create(qpfNuGetID(n) + ".nuspec")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}

	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="utf-8"?>
<package><metadata><id>%s</id><version>1.0.0</version><authors>qr</authors><description>quota test</description></metadata></package>`, qpfNuGetID(n))

	// Stored (not deflated) so the package size follows pad.
	lib, err := zw.CreateHeader(&zip.FileHeader{Name: "lib/data.bin", Method: zip.Store})
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}

	_, _ = lib.Write(qpfNoise(n, pad))

	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	var body bytes.Buffer

	mw := multipart.NewWriter(&body)

	fw, err := mw.CreateFormFile("package", "x.nupkg")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}

	_, _ = fw.Write(nupkg.Bytes())
	_ = mw.Close()

	return e.do(e.request(http.MethodPut, "/repos/"+e.repoName+"/v3/package", body.Bytes(), mw.FormDataContentType())), int64(nupkg.Len())
}

// --- go ----------------------------------------------------------------------

func qpfGoModule(n int) string { return fmt.Sprintf("example.com/qr/mod%d", n) }

func qpfGoUpload(t *testing.T, e *qrEnv, n, pad int) (*httptest.ResponseRecorder, int64) {
	t.Helper()

	const version = "v1.0.0"

	module := qpfGoModule(n)
	goMod := "module " + module + "\n\ngo 1.21\n"

	var zipBuf bytes.Buffer

	zw := zip.NewWriter(&zipBuf)

	for _, f := range []struct {
		name string
		data []byte
	}{
		{module + "@" + version + "/go.mod", []byte(goMod)},
		{module + "@" + version + "/data.bin", qpfNoise(n, pad)},
	} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Store})
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}

		_, _ = w.Write(f.data)
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}

	rec := e.do(e.request(http.MethodPut, "/repos/"+e.repoName+"/"+module+"/@v/"+version, zipBuf.Bytes(), "application/zip"))

	// One module is one artifact of zip + go.mod bytes.
	return rec, int64(zipBuf.Len() + len(goMod))
}

// --- cargo -------------------------------------------------------------------

func qpfCargoUpload(t *testing.T, e *qrEnv, n, pad int) (*httptest.ResponseRecorder, int64) {
	t.Helper()

	meta := fmt.Appendf(nil, `{"name":"qrcrate%d","vers":"0.1.0"}`, n)
	crate := qpfNoise(n, pad)

	var body bytes.Buffer

	_ = binary.Write(&body, binary.LittleEndian, uint32(len(meta)))
	body.Write(meta)
	_ = binary.Write(&body, binary.LittleEndian, uint32(len(crate)))
	body.Write(crate)

	return e.do(e.request(http.MethodPut, "/repos/"+e.repoName+"/api/v1/crates/new", body.Bytes(), "application/octet-stream")), int64(len(crate))
}

// --- harness -----------------------------------------------------------------

func qpfFormats() []qpfFormat {
	return []qpfFormat{
		{
			format: "pypi",
			mount: qpfMount(func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore) {
				t.Helper()

				r.Route("/repos/{name}", formatpypi.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost").RegisterRepoRoutes)
			}),
			upload: qpfPyPIUpload,
			remove: func(e *qrEnv, n int) *httptest.ResponseRecorder {
				return e.do(e.request(http.MethodDelete, "/repos/"+e.repoName+"/files/"+qpfPyPIFile(n), nil, ""))
			},
			duplicateStatus: []int{http.StatusConflict},
		},
		{
			format: "nuget",
			mount: qpfMount(func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore) {
				t.Helper()

				r.Route("/repos/{name}", formatnuget.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost").RegisterRepoRoutes)
			}),
			upload: qpfNuGetUpload,
			remove: func(e *qrEnv, n int) *httptest.ResponseRecorder {
				return e.do(e.request(http.MethodDelete, "/repos/"+e.repoName+"/v3/package/"+qpfNuGetID(n)+"/1.0.0", nil, ""))
			},
			duplicateStatus: []int{http.StatusConflict},
		},
		{
			format: "go",
			mount: qpfMount(func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore) {
				t.Helper()

				h := formatgomod.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost")
				r.Route("/repos/{name}", func(sub chi.Router) {
					h.RegisterRepoRoutes(sub)
					sub.Put("/*", h.HandlePut)
				})
			}),
			upload:          qpfGoUpload,
			duplicateStatus: []int{http.StatusConflict, http.StatusInternalServerError},
			table:           "gomod_modules",
			kind:            models.TrashKindGoMod,
		},
		{
			format: "cargo",
			mount: qpfMount(func(t *testing.T, r chi.Router, pg *database.PostgresDB, bs storage.BlobStore) {
				t.Helper()

				r.Route("/repos/{name}", formatcargo.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost").RegisterRepoRoutes)
			}),
			upload:          qpfCargoUpload,
			duplicateStatus: []int{http.StatusConflict},
			table:           "cargo_crates",
			kind:            models.TrashKindCargo,
		},
	}
}

// wantNoPending asserts that every reservation and delete of the repository was
// matched by its package row or release: nothing is left in quota_pending
// for a recount to count on top of the rows.
func (e *qrEnv) wantNoPending(step string) {
	e.t.Helper()

	var n int
	if err := e.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM quota_pending WHERE repository_id = $1`, e.repoID).Scan(&n); err != nil {
		e.t.Fatalf("%s: count pending quota entries: %v", step, err)
	}

	if n != 0 {
		e.t.Fatalf("%s: %d pending quota entries left, want 0 (a reservation or delete that no row or release consumed)", step, n)
	}
}

// wantReconciled recomputes the counters from the package tables and asserts
// they equal the incrementally maintained ones, with nothing left pending.
func (e *qrEnv) wantReconciled(step string) {
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

func (e *qrEnv) setLimit(maxBytes int64) {
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

func (e *qrEnv) wantQuotaExceeded(step string, rec *httptest.ResponseRecorder) {
	e.t.Helper()

	e.wantStatus(step, rec, http.StatusInsufficientStorage)

	var body struct {
		Code string `json:"code"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "quota_exceeded" {
		e.t.Fatalf("%s: body %q, want code quota_exceeded (err %v)", step, rec.Body.String(), err)
	}
}

// purgeAll soft-deletes every package row of the repository, ages the deletion
// past the retention window and runs the trash purge worker, which is where a
// format without a delete endpoint gives its quota back.
func (e *qrEnv) purgeAll(f qpfFormat) {
	e.t.Helper()

	ctx := context.Background()

	rows, err := e.pg.GetDB().QueryContext(ctx, `SELECT id FROM `+f.table+` WHERE repository_id = $1`, e.repoID)
	if err != nil {
		e.t.Fatalf("list %s rows: %v", f.table, err)
	}

	defer func() { _ = rows.Close() }()

	var ids []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatalf("scan: %v", err)
		}

		ids = append(ids, id)
	}

	if err := rows.Err(); err != nil {
		e.t.Fatalf("rows: %v", err)
	}

	_ = rows.Close()

	e.pg.SetTrashRetentionDays(7)
	e.t.Cleanup(func() { e.pg.SetTrashRetentionDays(0) })

	soft, err := e.pg.TrashRows(ctx, e.repoID, f.kind, ids, "alice")
	if err != nil || !soft {
		e.t.Fatalf("TrashRows = %v, %v; want a soft delete", soft, err)
	}

	if _, err := e.pg.GetDB().ExecContext(ctx, `UPDATE `+f.table+` SET deleted_at = now() - interval '30 days' WHERE repository_id = $1`, e.repoID); err != nil {
		e.t.Fatalf("age deletion: %v", err)
	}

	stats, err := worker.NewTrashPurgeWorker(e.pg, e.bs, newTestLogger(e.t), 0).RunOnce(ctx)
	if err != nil || stats.Purged != len(ids) || stats.Failed != 0 {
		e.t.Fatalf("purge RunOnce = %+v, %v; want %d purged", stats, err, len(ids))
	}
}

// TestQuotaPackageFormats proves pypi, nuget, go and cargo uploads reserve
// exactly one artifact of the package's bytes, refuse over-quota uploads with
// 507, do not leak on conflicts, return the usage on delete (or trash purge
// where the format has no delete endpoint) and stay equal to a reconcile.
func TestQuotaPackageFormats(t *testing.T) {
	pg := freshPostgres(t, 10)
	pg.SetTrashRetentionDays(0) // hard delete: the delete handlers release immediately

	for _, f := range qpfFormats() {
		t.Run(f.format, func(t *testing.T) {
			e := newQREnv(t, pg, f.format, f.mount)
			e.wantUsage("fresh repository", 0, 0)

			recA, sizeA := f.upload(t, e, 1, 300)
			e.wantStatus("upload A", recA, http.StatusOK, http.StatusCreated)

			recB, sizeB := f.upload(t, e, 2, 900)
			e.wantStatus("upload B", recB, http.StatusOK, http.StatusCreated)
			e.wantUsage("after two uploads", sizeA+sizeB, 2)
			e.wantReconciled("after two uploads")

			// A repeated upload must not double count.
			dup, _ := f.upload(t, e, 1, 300)
			e.wantStatus("duplicate upload", dup, f.duplicateStatus...)
			e.wantUsage("after refused duplicate", sizeA+sizeB, 2)

			// A limit with 50 bytes of headroom refuses the next package.
			e.setLimit(sizeA + sizeB + 50)

			over, _ := f.upload(t, e, 3, 2000)
			e.wantQuotaExceeded("over-quota upload", over)
			e.wantUsage("after refused over-quota upload", sizeA+sizeB, 2)
			e.wantReconciled("after refused over-quota upload")

			e.setLimit(0)

			if f.remove == nil {
				e.purgeAll(f)
				e.wantUsage("after trash purge", 0, 0)
				e.wantReconciled("after trash purge")

				return
			}

			e.wantStatus("delete A", f.remove(e, 1), http.StatusOK, http.StatusNoContent)
			e.wantUsage("after deleting A", sizeB, 1)
			e.wantReconciled("after deleting A")

			e.wantStatus("delete B", f.remove(e, 2), http.StatusOK, http.StatusNoContent)
			e.wantUsage("after deleting B", 0, 0)
			e.wantReconciled("after deleting B")

			// The freed headroom is real: the package that was refused fits now.
			e.setLimit(sizeA + sizeB + 50)

			recC, sizeC := f.upload(t, e, 3, 2000)
			if sizeC > sizeA+sizeB+50 {
				e.wantQuotaExceeded("upload C beyond limit", recC)
				return
			}

			e.wantStatus("upload C after delete", recC, http.StatusOK, http.StatusCreated)
			e.wantUsage("after upload C", sizeC, 1)
			e.wantReconciled("after upload C")
		})
	}
}

// TestQuotaPackageFormatsCargoYankKeepsUsage shows yank is not a delete: the
// crate stays stored and counted.
func TestQuotaPackageFormatsCargoYankKeepsUsage(t *testing.T) {
	pg := freshPostgres(t, 10)

	var f qpfFormat

	for _, c := range qpfFormats() {
		if c.format == "cargo" {
			f = c
		}
	}

	e := newQREnv(t, pg, f.format, f.mount)

	rec, size := f.upload(t, e, 1, 400)
	e.wantStatus("publish", rec, http.StatusOK)
	e.wantUsage("after publish", size, 1)

	e.wantStatus("yank", e.do(e.request(http.MethodDelete, "/repos/"+e.repoName+"/api/v1/crates/qrcrate1/0.1.0/yank", nil, "")), http.StatusOK)
	e.wantUsage("after yank", size, 1)
	e.wantReconciled("after yank")
}
