//go:build integration

// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/openartifact/openartifact/internal/api"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	formathelm "github.com/openartifact/openartifact/internal/format/helm"
	"github.com/openartifact/openartifact/internal/storage"
)

// qrEnv is a real-Postgres + real-filesystem-blob-store fixture with one
// hosted repository of a single format, served by the real upload handler.
type qrEnv struct {
	t        *testing.T
	pg       *database.PostgresDB
	bs       storage.BlobStore
	repoID   string
	repoName string
	router   http.Handler
}

func newQREnv(t *testing.T, pg *database.PostgresDB, format string, mount func(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler) *qrEnv {
	t.Helper()

	bs, err := storage.NewFilesystemStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	org := models.SystemProjectID
	repo := &models.Repository{Name: "qr-" + format, Format: format, RepoType: "hosted", ProjectID: &org}

	if err := pg.CreateRepository(context.Background(), repo); err != nil {
		t.Fatalf("CreateRepository(%s): %v", format, err)
	}

	return &qrEnv{t: t, pg: pg, bs: bs, repoID: repo.ID, repoName: repo.Name, router: mount(t, pg, bs)}
}

// usage reads the quota counters straight from the table; a missing row means
// nothing was ever reserved.
func (e *qrEnv) usage() (int64, int) {
	e.t.Helper()

	var (
		size  int64
		count int
	)

	err := e.pg.GetDB().QueryRowContext(context.Background(),
		`SELECT current_size_bytes, current_artifact_count FROM storage_quotas WHERE repository_id = $1`,
		e.repoID).Scan(&size, &count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0
	}

	if err != nil {
		e.t.Fatalf("read usage: %v", err)
	}

	return size, count
}

func (e *qrEnv) wantUsage(step string, size int64, count int) {
	e.t.Helper()

	gotSize, gotCount := e.usage()
	if gotSize != size || gotCount != count {
		e.t.Fatalf("%s: usage = (%d bytes, %d artifacts), want (%d, %d)", step, gotSize, gotCount, size, count)
	}
}

func (e *qrEnv) do(req *http.Request) *httptest.ResponseRecorder {
	e.t.Helper()

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	return rec
}

func (e *qrEnv) wantStatus(step string, rec *httptest.ResponseRecorder, want ...int) {
	e.t.Helper()

	if slices.Contains(want, rec.Code) {
		return
	}

	e.t.Fatalf("%s: status = %d, want one of %v: %s", step, rec.Code, want, rec.Body.String())
}

func (e *qrEnv) request(method, path string, body []byte, contentType string) *http.Request {
	e.t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	req.Header.Set("Authorization", "Bearer token") // accepted by authenticateTestBearer (npm only)

	return req
}

// wantBlobGone asserts the content-addressed blob of data was removed from the store.
func (e *qrEnv) wantBlobGone(data []byte) {
	e.t.Helper()

	sum := sha256.Sum256(data)

	sweepQueuedBlobs(e.t, e.pg, e.bs)

	_, exists, err := e.bs.ExistsBySHA256(context.Background(), hex.EncodeToString(sum[:]))
	if err != nil {
		e.t.Fatalf("ExistsBySHA256: %v", err)
	}

	if exists {
		e.t.Errorf("blob %x still in the store after its last reference was deleted", sum[:6])
	}
}

func qrMultipart(t *testing.T, filename string, data []byte) ([]byte, string) {
	t.Helper()

	var body bytes.Buffer

	w := multipart.NewWriter(&body)

	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}

	if _, err := io.Copy(fw, bytes.NewReader(data)); err != nil {
		t.Fatalf("copy: %v", err)
	}

	_ = w.Close()

	return body.Bytes(), w.FormDataContentType()
}

// --- format fixtures ---------------------------------------------------------

func qrMountMaven(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler {
	t.Helper()

	router := chi.NewRouter()
	api.NewMavenUploadHandler(newTestLogger(t), pg, bs, nil).Register(router)

	return router
}

func qrMountNPM(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler {
	t.Helper()

	router := chi.NewRouter()
	router.Use(authenticateTestBearer)
	api.NewNPMUploadHandler(newTestLogger(t), pg, bs, "http://localhost", nil).Register(router)

	return router
}

func qrMountRPM(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler {
	t.Helper()

	router := chi.NewRouter()
	api.NewRPMUploadHandler(newTestLogger(t), pg, bs, nil).Register(router)

	return router
}

func qrMountDeb(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler {
	t.Helper()

	router := chi.NewRouter()
	api.NewDebUploadHandler(newTestLogger(t), pg, bs, nil).Register(router)

	return router
}

func qrMountHelm(t *testing.T, pg *database.PostgresDB, bs storage.BlobStore) http.Handler {
	t.Helper()

	router := chi.NewRouter()
	router.Route("/repos/{name}", formathelm.NewHTTPHandler(newTestLogger(t), pg, bs, "http://localhost").RegisterRepoRoutes)

	return router
}

// --- maven -------------------------------------------------------------------

func (e *qrEnv) mavenPut(path string, data []byte) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.do(e.request(http.MethodPut, "/repos/maven/"+e.repoName+"/"+path, data, "application/java-archive"))
}

func (e *qrEnv) mavenDelete(path string) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.do(e.request(http.MethodDelete, "/repos/maven/"+e.repoName+"/"+path, nil, ""))
}

func qrRunMaven(t *testing.T, e *qrEnv) {
	t.Helper()

	const (
		releasePath  = "com/example/app/1.0.0/app-1.0.0.jar"
		snapshotPath = "com/example/lib/2.0-SNAPSHOT/lib-2.0-SNAPSHOT.jar"
	)

	release := bytes.Repeat([]byte("R"), 1234)
	snap1 := bytes.Repeat([]byte("s"), 700)
	snap2 := bytes.Repeat([]byte("S"), 2100)

	e.wantStatus("put release", e.mavenPut(releasePath, release), http.StatusCreated)
	e.wantStatus("put snapshot", e.mavenPut(snapshotPath, snap1), http.StatusCreated)
	e.wantUsage("after two uploads", int64(len(release)+len(snap1)), 2)

	// Release re-publish with different content is refused and must not leak a reservation.
	e.wantStatus("release conflict", e.mavenPut(releasePath, []byte("different")), http.StatusConflict)
	e.wantUsage("after refused release overwrite", int64(len(release)+len(snap1)), 2)

	// Identical re-publish is an idempotent no-op.
	e.wantStatus("idempotent re-publish", e.mavenPut(releasePath, release), http.StatusOK)
	e.wantUsage("after idempotent re-publish", int64(len(release)+len(snap1)), 2)

	// Snapshot overwrite replaces the old size, count unchanged.
	e.wantStatus("snapshot overwrite", e.mavenPut(snapshotPath, snap2), http.StatusOK, http.StatusCreated)
	e.wantUsage("after snapshot overwrite", int64(len(release)+len(snap2)), 2)
	e.wantBlobGone(snap1)

	e.wantStatus("delete release", e.mavenDelete(releasePath), http.StatusNoContent, http.StatusOK)
	e.wantUsage("after deleting release", int64(len(snap2)), 1)
	e.wantBlobGone(release)

	e.wantStatus("delete snapshot", e.mavenDelete(snapshotPath), http.StatusNoContent, http.StatusOK)
	e.wantUsage("after deleting snapshot", 0, 0)
	e.wantBlobGone(snap2)
}

// --- npm ---------------------------------------------------------------------

// qrNPMBody builds a publish body and returns it with its tarball size.
func qrNPMBody(t *testing.T, name, version string) ([]byte, int64) {
	t.Helper()

	body := buildPublishBody(t, name, version)

	var parsed struct {
		Attachments map[string]struct {
			Length int64 `json:"length"`
		} `json:"_attachments"` //nolint:tagliatelle // npm registry wire format
	}

	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Attachments) != 1 {
		t.Fatalf("parse publish body: %v", err)
	}

	for _, a := range parsed.Attachments {
		return body, a.Length
	}

	return nil, 0
}

func (e *qrEnv) npmPublish(name string, body []byte) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.do(e.request(http.MethodPut, "/repos/"+e.repoName+"/"+name, body, "application/json"))
}

func (e *qrEnv) npmUnpublish(path string) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.do(e.request(http.MethodDelete, "/repos/"+e.repoName+"/"+path, nil, ""))
}

func qrRunNPM(t *testing.T, e *qrEnv) {
	t.Helper()

	a1, a1Size := qrNPMBody(t, "alpha", "1.0.0")
	a2, a2Size := qrNPMBody(t, "alpha", "2.0.0")
	a3, a3Size := qrNPMBody(t, "alpha", "3.0.0")
	b1, b1Size := qrNPMBody(t, "beta", "1.0.0")

	e.wantStatus("publish alpha@1", e.npmPublish("alpha", a1), http.StatusCreated)
	e.wantStatus("publish beta@1", e.npmPublish("beta", b1), http.StatusCreated)
	e.wantUsage("after two publishes", a1Size+b1Size, 2)

	// Same version again is refused and must release its reservation.
	e.wantStatus("republish alpha@1", e.npmPublish("alpha", a1), http.StatusConflict)
	e.wantUsage("after refused republish", a1Size+b1Size, 2)

	e.wantStatus("publish alpha@2", e.npmPublish("alpha", a2), http.StatusCreated)
	e.wantStatus("publish alpha@3", e.npmPublish("alpha", a3), http.StatusCreated)
	e.wantUsage("after four versions", a1Size+b1Size+a2Size+a3Size, 4)

	// `npm unpublish alpha@1.0.0` final step: delete one version's tarball.
	e.wantStatus("unpublish alpha@1 tarball", e.npmUnpublish("alpha/-/alpha-1.0.0.tgz/-rev/1-abc"), http.StatusOK)
	e.wantUsage("after unpublishing alpha@1", b1Size+a2Size+a3Size, 3)

	// Whole-package delete of beta.
	e.wantStatus("unpublish beta", e.npmUnpublish("beta/-rev/1-abc"), http.StatusOK)
	e.wantUsage("after unpublishing beta", a2Size+a3Size, 2)

	// Whole-package delete of alpha releases its remaining two versions.
	e.wantStatus("unpublish alpha", e.npmUnpublish("alpha/-rev/1-abc"), http.StatusOK)
	e.wantUsage("after unpublishing alpha", 0, 0)
}

// --- rpm ---------------------------------------------------------------------

// qrRPM derives a distinct valid rpm from buildMinimalRPM: the 7-byte name is
// replaced in place (header offsets stay valid) and trailing payload bytes
// vary the size.
func qrRPM(name string, extra int) []byte {
	if len(name) != len("rpmtest") {
		panic("rpm test name must be 7 bytes")
	}

	data := bytes.Replace(buildMinimalRPM(), []byte("rpmtest\x00"), []byte(name+"\x00"), 1)

	return append(data, bytes.Repeat([]byte{0xAB}, extra)...)
}

func (e *qrEnv) rpmUpload(data []byte) (*httptest.ResponseRecorder, string) {
	e.t.Helper()

	body, ct := qrMultipart(e.t, "pkg.rpm", data)
	rec := e.do(e.request(http.MethodPost, "/api/v1/repositories/"+e.repoName+"/packages", body, ct))

	return rec, qrResultID(e.t, rec)
}

func (e *qrEnv) rpmDelete(id string) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.do(e.request(http.MethodDelete, "/api/v1/repositories/"+e.repoName+"/packages/"+id, nil, ""))
}

// qrResultID extracts data.id from a success envelope ("" for failures).
func qrResultID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	if rec.Code != http.StatusOK {
		return ""
	}

	var env struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data.ID == "" {
		t.Fatalf("decode upload result %q: %v", rec.Body.String(), err)
	}

	return env.Data.ID
}

func qrRunRPM(t *testing.T, e *qrEnv) {
	t.Helper()

	r1 := qrRPM("rpmtesA", 500)
	r2 := qrRPM("rpmtesB", 1700)

	rec, id1 := e.rpmUpload(r1)
	e.wantStatus("upload rpm 1", rec, http.StatusOK)

	rec, id2 := e.rpmUpload(r2)
	e.wantStatus("upload rpm 2", rec, http.StatusOK)
	e.wantUsage("after two uploads", int64(len(r1)+len(r2)), 2)

	rec, _ = e.rpmUpload(r1)
	e.wantStatus("duplicate rpm", rec, http.StatusConflict)
	e.wantUsage("after refused duplicate", int64(len(r1)+len(r2)), 2)

	e.wantStatus("delete rpm 1", e.rpmDelete(id1), http.StatusOK)
	e.wantUsage("after deleting rpm 1", int64(len(r2)), 1)
	e.wantBlobGone(r1)

	e.wantStatus("delete rpm 2", e.rpmDelete(id2), http.StatusOK)
	e.wantUsage("after deleting rpm 2", 0, 0)
	e.wantBlobGone(r2)
}

// --- deb ---------------------------------------------------------------------

// qrDeb builds a minimal ar archive holding a control.tar.gz; the payload
// padding changes the size without affecting the parsed control data.
func qrDeb(t *testing.T, pkgName, version string, pad int) []byte {
	t.Helper()

	ctrl := fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: amd64\nMaintainer: A <a@b.c>\nInstalled-Size: 10\nDescription: hi\n", pkgName, version)

	var tarBuf bytes.Buffer

	tw := tar.NewWriter(&tarBuf)
	if err := tw.WriteHeader(&tar.Header{Name: "./control", Mode: 0o644, Size: int64(len(ctrl))}); err != nil {
		t.Fatal(err)
	}

	_, _ = tw.Write([]byte(ctrl))
	_ = tw.Close()

	var gz bytes.Buffer

	gw := gzip.NewWriter(&gz)
	_, _ = gw.Write(tarBuf.Bytes())
	_ = gw.Close()

	var out bytes.Buffer

	out.WriteString("!<arch>\n")
	fmt.Fprintf(&out, "%-16s%-12s%-6s%-6s%-8s%-10d`\n", "control.tar.gz", "0", "0", "0", "100644", gz.Len())
	out.Write(gz.Bytes())

	if gz.Len()%2 == 1 {
		out.WriteByte('\n')
	}

	// Trailing bytes (not a valid ar member): only the control archive is parsed.
	out.Write(bytes.Repeat([]byte{0}, pad))

	return out.Bytes()
}

func (e *qrEnv) debUpload(data []byte) (*httptest.ResponseRecorder, string) {
	e.t.Helper()

	body, ct := qrMultipart(e.t, "pkg.deb", data)
	rec := e.do(e.request(http.MethodPost, "/api/v1/repositories/"+e.repoName+"/debs", body, ct))

	return rec, qrResultID(e.t, rec)
}

func (e *qrEnv) debDelete(id string) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.do(e.request(http.MethodDelete, "/api/v1/repositories/"+e.repoName+"/debs/"+id, nil, ""))
}

func qrRunDeb(t *testing.T, e *qrEnv) {
	t.Helper()

	d1 := qrDeb(t, "hello", "1.0", 0)
	d2 := qrDeb(t, "world", "2.0", 0)

	rec, id1 := e.debUpload(d1)
	e.wantStatus("upload deb 1", rec, http.StatusOK)

	rec, id2 := e.debUpload(d2)
	e.wantStatus("upload deb 2", rec, http.StatusOK)
	e.wantUsage("after two uploads", int64(len(d1)+len(d2)), 2)

	rec, _ = e.debUpload(d1)
	e.wantStatus("duplicate deb", rec, http.StatusConflict)
	e.wantUsage("after refused duplicate", int64(len(d1)+len(d2)), 2)

	// Same name/version/arch with different bytes is refused as well.
	rec, _ = e.debUpload(qrDeb(t, "hello", "1.0", 4))
	e.wantStatus("same filename, other content", rec, http.StatusConflict)
	e.wantUsage("after refused same-filename upload", int64(len(d1)+len(d2)), 2)

	e.wantStatus("delete deb 1", e.debDelete(id1), http.StatusOK)
	e.wantUsage("after deleting deb 1", int64(len(d2)), 1)
	e.wantBlobGone(d1)

	e.wantStatus("delete deb 2", e.debDelete(id2), http.StatusOK)
	e.wantUsage("after deleting deb 2", 0, 0)
	e.wantBlobGone(d2)
}

// --- helm --------------------------------------------------------------------

func qrChart(t *testing.T, name, version string, pad int) []byte {
	t.Helper()

	var buf bytes.Buffer

	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	add := func(path string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}

	add(name+"/Chart.yaml", []byte(fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\ndescription: test\n", name, version)))

	if pad > 0 {
		// Incompressible padding so the gzip size differs between charts.
		noise := make([]byte, pad)
		for i := range noise {
			noise[i] = byte(i*131 + i/7)
		}

		add(name+"/templates/pad.txt", noise)
	}

	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

func (e *qrEnv) helmUpload(data []byte) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.do(e.request(http.MethodPost, "/repos/"+e.repoName+"/charts", data, "application/gzip"))
}

func (e *qrEnv) helmDelete(filename string) *httptest.ResponseRecorder {
	e.t.Helper()

	return e.do(e.request(http.MethodDelete, "/repos/"+e.repoName+"/charts/"+filename, nil, ""))
}

func qrRunHelm(t *testing.T, e *qrEnv) {
	t.Helper()

	c1 := qrChart(t, "mychart", "0.1.0", 300)
	c2 := qrChart(t, "other", "1.2.3", 900)

	e.wantStatus("upload chart 1", e.helmUpload(c1), http.StatusCreated)
	e.wantStatus("upload chart 2", e.helmUpload(c2), http.StatusCreated)
	e.wantUsage("after two uploads", int64(len(c1)+len(c2)), 2)

	e.wantStatus("duplicate chart", e.helmUpload(c1), http.StatusConflict)
	e.wantUsage("after refused duplicate", int64(len(c1)+len(c2)), 2)

	e.wantStatus("delete chart 1", e.helmDelete("mychart-0.1.0.tgz"), http.StatusOK)
	e.wantUsage("after deleting chart 1", int64(len(c2)), 1)
	e.wantBlobGone(c1)

	e.wantStatus("delete chart 2", e.helmDelete("other-1.2.3.tgz"), http.StatusOK)
	e.wantUsage("after deleting chart 2", 0, 0)
	e.wantBlobGone(c2)
}

// --- tests -------------------------------------------------------------------

// TestQuotaReleaseOnDelete proves, against real Postgres and the real HTTP
// handlers, that every upload reserves (size, 1) and every delete returns
// exactly that, ending at (0, 0).
//
// rubygems is intentionally not covered: its API only yanks (no hard delete),
// so there is no delete path that releases quota to compare against.
func TestQuotaReleaseOnDelete(t *testing.T) {
	pg := freshPostgres(t, 10)

	cases := []struct {
		format string
		mount  func(*testing.T, *database.PostgresDB, storage.BlobStore) http.Handler
		run    func(*testing.T, *qrEnv)
	}{
		{"maven", qrMountMaven, qrRunMaven},
		{"npm", qrMountNPM, qrRunNPM},
		{"rpm", qrMountRPM, qrRunRPM},
		{"deb", qrMountDeb, qrRunDeb},
		{"helm", qrMountHelm, qrRunHelm},
	}

	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			e := newQREnv(t, pg, tc.format, tc.mount)
			e.wantUsage("fresh repository", 0, 0)
			tc.run(t, e)
		})
	}
}

// TestQuotaEnforcedAgainstLimitOnRealDB shows a size limit is enforced with
// 507 and that deleting an artifact frees real headroom for the next upload.
func TestQuotaEnforcedAgainstLimitOnRealDB(t *testing.T) {
	pg := freshPostgres(t, 10)
	e := newQREnv(t, pg, "maven", qrMountMaven)
	ctx := context.Background()

	q, err := pg.GetOrCreateStorageQuota(ctx, e.repoID)
	if err != nil {
		t.Fatalf("GetOrCreateStorageQuota: %v", err)
	}

	const blobSize = 100

	q.MaxSizeBytes = 2*blobSize + 50 // room for two, not three
	if err := pg.UpdateStorageQuota(ctx, q); err != nil {
		t.Fatalf("UpdateStorageQuota: %v", err)
	}

	artifact := func(n int) (string, []byte) {
		return fmt.Sprintf("org/q/app/%d.0.0/app-%d.0.0.jar", n, n), bytes.Repeat([]byte{byte('a' + n)}, blobSize)
	}

	for n := 1; n <= 2; n++ {
		path, data := artifact(n)
		e.wantStatus(fmt.Sprintf("put %d", n), e.mavenPut(path, data), http.StatusCreated)
	}

	e.wantUsage("at the limit", 2*blobSize, 2)

	path3, data3 := artifact(3)
	e.wantStatus("put over limit", e.mavenPut(path3, data3), http.StatusInsufficientStorage)
	e.wantUsage("after refused upload", 2*blobSize, 2)

	path1, _ := artifact(1)
	e.wantStatus("delete 1", e.mavenDelete(path1), http.StatusNoContent, http.StatusOK)
	e.wantUsage("after delete", blobSize, 1)

	e.wantStatus("put 3 after delete", e.mavenPut(path3, data3), http.StatusCreated)
	e.wantUsage("after retry", 2*blobSize, 2)

	// The limit itself must be untouched by uploads and deletes.
	q, err = pg.GetOrCreateStorageQuota(ctx, e.repoID)
	if err != nil || q.MaxSizeBytes != 2*blobSize+50 {
		t.Fatalf("quota limit changed: %+v, err %v", q, err)
	}
}
