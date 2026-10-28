// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
	pkgansible "github.com/openartifact/openartifact/pkg/ansible"
	"github.com/openartifact/openartifact/pkg/ansible/ansibletest"
)

// ansibleDB is an in-memory ansible_collections table with its live unique key.
type ansibleDB struct {
	*formattest.DB

	rows []*models.AnsibleCollection

	limit, used int64
}

func (d *ansibleDB) ReserveStorageUsage(_ context.Context, _ string, size int64, _ int) (bool, error) {
	if d.limit > 0 && d.used+size > d.limit {
		return false, nil
	}

	d.used += size

	return true, nil
}

func (d *ansibleDB) DecrementStorageUsage(_ context.Context, _ string, size int64, _ int) error {
	d.used -= size
	return nil
}

func (d *ansibleDB) CreateAnsibleCollection(_ context.Context, c *models.AnsibleCollection) error {
	for _, r := range d.rows {
		if r.Namespace == c.Namespace && r.Name == c.Name && r.Version == c.Version {
			return database.ErrDuplicate
		}
	}

	c.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", len(d.rows)+1)
	c.UpdatedAt = c.CreatedAt
	d.rows = append(d.rows, c)

	return nil
}

func (d *ansibleDB) GetAnsibleCollection(_ context.Context, _, namespace, name, version string) (*models.AnsibleCollection, error) {
	for _, r := range d.rows {
		if r.Namespace == namespace && r.Name == name && r.Version == version {
			return r, nil
		}
	}

	return nil, nil
}

func (d *ansibleDB) GetAnsibleCollectionByID(_ context.Context, _, id string) (*models.AnsibleCollection, error) {
	for _, r := range d.rows {
		if r.ID == id {
			return r, nil
		}
	}

	return nil, nil
}

func (d *ansibleDB) ListAnsibleCollectionVersions(_ context.Context, _, namespace, name string) ([]*models.AnsibleCollection, error) {
	var out []*models.AnsibleCollection

	for _, r := range d.rows {
		if r.Namespace == namespace && r.Name == name {
			out = append(out, r)
		}
	}

	return out, nil
}

func (d *ansibleDB) ListAnsibleCollectionNames(
	_ context.Context,
	_ string,
	limit, offset int,
) ([]*models.AnsibleCollectionName, int, error) {
	var names []*models.AnsibleCollectionName

	for _, r := range d.rows {
		if !slices.ContainsFunc(
			names,
			func(n *models.AnsibleCollectionName) bool { return n.Namespace == r.Namespace && n.Name == r.Name },
		) {
			names = append(
				names,
				&models.AnsibleCollectionName{Namespace: r.Namespace, Name: r.Name, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt},
			)
		}
	}

	slices.SortFunc(names, func(a, b *models.AnsibleCollectionName) int {
		return strings.Compare(a.Namespace+"."+a.Name, b.Namespace+"."+b.Name)
	})

	return pkgansible.Window(names, limit, offset), len(names), nil
}

func (d *ansibleDB) DeleteAnsibleCollection(_ context.Context, _, id string) error {
	d.rows = slices.DeleteFunc(d.rows, func(r *models.AnsibleCollection) bool { return r.ID == id })
	return nil
}

func newTestHandler(t *testing.T) (*HTTPHandler, *ansibleDB, *formattest.BlobStore, *formattest.Publisher) {
	t.Helper()

	db := &ansibleDB{DB: formattest.NewDB()}
	db.Repo.Format = FormatName

	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost")
	h.SetPublisher(pub)

	return h, db, bs, pub
}

// newHandlerOnly is newTestHandler for tests that need the handler alone.
func newHandlerOnly(t *testing.T) *HTTPHandler {
	t.Helper()

	h, _, _, _ := newTestHandler(t) //nolint:dogsled // the other fixtures are not needed

	return h
}

func do(h *HTTPHandler, method, target string, body []byte, headers ...string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == nil {
		req = formattest.Request(method, target, nil)
	} else {
		req = formattest.Request(method, target, bytes.NewReader(body))
	}

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	return formattest.Do(h.RegisterRepoRoutes, req)
}

const (
	api         = "/repos/myrepo/api/v3/"
	publishPath = api + "artifacts/collections/"
)

// publishForm builds the multipart body ansible-galaxy sends.
func publishForm(t *testing.T, filename string, data []byte, sha string) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)

	if sha != "" {
		if err := mw.WriteField("sha256", sha); err != nil {
			t.Fatal(err)
		}
	}

	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}

	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes(), mw.FormDataContentType()
}

func publish(t *testing.T, h *HTTPHandler, spec ansibletest.Spec) (*httptest.ResponseRecorder, []byte) {
	t.Helper()

	data := ansibletest.Build(t, spec)
	body, ct := publishForm(t, pkgansible.FileName(spec.Namespace, spec.Name, spec.Version), data, "")

	return do(h, http.MethodPost, publishPath, body, "Content-Type", ct), data
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response is not JSON (%d): %s", w.Code, w.Body.String())
	}

	return doc
}

var acme = ansibletest.Spec{ //nolint:gochecknoglobals // read-only test fixture
	Namespace: "acme", Name: "tools", Version: "1.0.0", License: "MIT",
	Dependencies: map[string]string{"ansible.posix": ">=1.0.0"}, RequiresAnsible: ">=2.15.0",
}

//nolint:gocyclo,cyclop // one scenario: publish, poll the import, read the metadata, download, delete
func TestPublishReadDownloadDelete(t *testing.T) {
	h, db, bs, pub := newTestHandler(t)

	w, data := publish(t, h, acme)
	if w.Code != http.StatusAccepted {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}

	row := db.rows[0]
	if row.Filename != "acme-tools-1.0.0.tar.gz" || row.Dependencies["ansible.posix"] != ">=1.0.0" || row.RequiresAnsible != ">=2.15.0" {
		t.Fatalf("row = %+v", row)
	}

	if db.used != int64(len(data)) || len(bs.Blobs) != 1 {
		t.Errorf("quota used = %d (want %d), blobs = %d", db.used, len(data), len(bs.Blobs))
	}

	if evs := pub.Events(); len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded ||
		evs[0].Artifact()["name"] != "acme.tools" || evs[0].Artifact()["path"] != "acme-tools-1.0.0.tar.gz" {
		t.Errorf("events = %+v", evs)
	}

	// The import task the client polls is finished.
	task, _ := decode(t, w)["task"].(string)
	if !strings.HasPrefix(task, "http://localhost/repos/myrepo/api/v3/imports/collections/") {
		t.Fatalf("task = %q", task)
	}

	w = do(h, http.MethodGet, task, nil)
	if imp := decode(
		t,
		w,
	); w.Code != http.StatusOK || imp["state"] != "completed" || imp["finished_at"] == "" ||
		imp["finished_at"] == nil {
		t.Fatalf("import = %d %v", w.Code, imp)
	}

	// Discovery and listing.
	for _, p := range []string{"/repos/myrepo/api/", "/repos/myrepo/api", api} {
		if w = do(h, http.MethodGet, p, nil); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d", p, w.Code)
		}
	}

	doc := decode(t, do(h, http.MethodGet, "/repos/myrepo/api/", nil))
	if v, _ := doc["available_versions"].(map[string]any); v["v3"] != "v3/" {
		t.Errorf("available_versions = %v", doc["available_versions"])
	}

	doc = decode(t, do(h, http.MethodGet, api+"collections/", nil))
	if meta, _ := doc["meta"].(map[string]any); meta["count"] != float64(1) {
		t.Errorf("index = %v", doc)
	}

	doc = decode(t, do(h, http.MethodGet, api+"collections/acme/tools/", nil))
	if hv, _ := doc["highest_version"].(map[string]any); hv["version"] != "1.0.0" || doc["updated_at"] == "" {
		t.Errorf("collection = %v", doc)
	}

	// The version detail carries what ansible-galaxy needs to install.
	w = do(h, http.MethodGet, api+"collections/acme/tools/versions/1.0.0/", nil)
	detail := decode(t, w)
	artifact, _ := detail["artifact"].(map[string]any)
	meta, _ := detail["metadata"].(map[string]any)
	deps, _ := meta["dependencies"].(map[string]any)

	if w.Code != http.StatusOK || artifact["sha256"] != row.SHA256 || deps["ansible.posix"] != ">=1.0.0" ||
		detail["download_url"] != "http://localhost/repos/myrepo/api/v3/artifacts/collections/acme-tools-1.0.0.tar.gz" {
		t.Fatalf("version = %d %v", w.Code, detail)
	}

	if sigs, ok := detail["signatures"].([]any); !ok || len(sigs) != 0 {
		t.Errorf("signatures = %v", detail["signatures"])
	}

	w = do(h, http.MethodGet, detail["download_url"].(string), nil)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data) {
		t.Fatalf("download = %d, %d bytes", w.Code, w.Body.Len())
	}

	if w, _ = publish(t, h, acme); w.Code != http.StatusConflict {
		t.Errorf("second publish = %d, want 409", w.Code)
	}

	if w = do(h, http.MethodDelete, api+"collections/acme/tools/versions/1.0.0/", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", w.Code, w.Body.String())
	}

	if len(db.rows) != 0 || len(bs.Blobs) != 0 || db.used != 0 {
		t.Errorf("after delete: rows=%d blobs=%d quota=%d", len(db.rows), len(bs.Blobs), db.used)
	}

	if w = do(h, http.MethodGet, api+"collections/acme/tools/versions/1.0.0/", nil); w.Code != http.StatusNotFound {
		t.Errorf("deleted version = %d, want 404", w.Code)
	}
}

func TestPublishRejections(t *testing.T) {
	good := ansibletest.Build(t, acme)
	digest := sha256.Sum256(good)
	sum := hex.EncodeToString(digest[:])

	tests := []struct {
		name     string
		filename string
		data     []byte
		sha      string
		status   int
		want     string
	}{
		{"checksum mismatch", "acme-tools-1.0.0.tar.gz", good, strings.Repeat("0", 64), http.StatusBadRequest, "sha256"},
		{"wrong file name", "other-tools-1.0.0.tar.gz", good, sum, http.StatusBadRequest, "must be"},
		{"not a collection", "acme-tools-1.0.0.tar.gz", []byte("hello"), "", http.StatusBadRequest, "gzip"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, db, bs, _ := newTestHandler(t)
			body, ct := publishForm(t, tc.filename, tc.data, tc.sha)

			w := do(h, http.MethodPost, publishPath, body, "Content-Type", ct)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}

			if len(db.rows) != 0 || len(bs.Blobs) != 0 || db.used != 0 {
				t.Errorf("rows=%d blobs=%d used=%d", len(db.rows), len(bs.Blobs), db.used)
			}
		})
	}

	h := newHandlerOnly(t)
	body, ct := publishForm(t, "acme-tools-1.0.0.tar.gz", good, sum)

	if w := do(h, http.MethodPost, publishPath, body, "Content-Type", ct); w.Code != http.StatusAccepted {
		t.Errorf("a correct checksum is refused: %d %s", w.Code, w.Body.String())
	}

	if w := do(h, http.MethodPost, publishPath, []byte("x"), "Content-Type", "text/plain"); w.Code != http.StatusBadRequest {
		t.Errorf("a post that is no form = %d", w.Code)
	}
}

func TestOtherFormatIsNotFound(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	db.Repo.Format = "conda"

	if w := do(h, http.MethodGet, "/repos/myrepo/api/", nil); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestQuotaRefusal(t *testing.T) {
	h, db, bs, _ := newTestHandler(t)
	db.limit = 10

	if w, _ := publish(t, h, acme); w.Code == http.StatusAccepted {
		t.Errorf("publish over the quota accepted")
	}

	if len(db.rows) != 0 || len(bs.Blobs) != 0 || db.used != 0 {
		t.Errorf("rows=%d blobs=%d used=%d", len(db.rows), len(bs.Blobs), db.used)
	}
}

func TestQuarantinedDownloadIsRefused(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	if w, _ := publish(t, h, acme); w.Code != http.StatusAccepted {
		t.Fatalf("publish = %d", w.Code)
	}

	db.Keys["acme-tools-1.0.0.tar.gz"] = true

	if w := do(h, http.MethodGet, publishPath+"acme-tools-1.0.0.tar.gz", nil); w.Code != http.StatusForbidden {
		t.Errorf("quarantined download = %d, want 403", w.Code)
	}
}

func TestVersionList(t *testing.T) {
	h, db, _, _ := newTestHandler(t)

	for _, v := range []string{"1.0.0", "1.10.0", "1.2.0", "2.0.0-rc.1"} {
		spec := acme
		spec.Version = v

		if w, _ := publish(t, h, spec); w.Code != http.StatusAccepted {
			t.Fatalf("publish %s = %d: %s", v, w.Code, w.Body.String())
		}
	}

	db.rows[0].CreatedAt = time.Now()

	list := func(query string) ([]string, map[string]any) {
		t.Helper()

		var versions []string

		w := do(h, http.MethodGet, api+"collections/acme/tools/versions/"+query, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("list %q = %d: %s", query, w.Code, w.Body.String())
		}

		doc := decode(t, w)
		data, _ := doc["data"].([]any)

		for _, e := range data {
			versions = append(versions, e.(map[string]any)["version"].(string))
		}

		links, _ := doc["links"].(map[string]any)

		return versions, links
	}

	got, links := list("?limit=2")
	if !slices.Equal(got, []string{"2.0.0-rc.1", "1.10.0"}) || links["previous"] != nil ||
		links["next"] != "http://localhost/repos/myrepo/api/v3/collections/acme/tools/versions/?limit=2&offset=2" {
		t.Errorf("first page = %v %v", got, links)
	}

	got, links = list("?limit=2&offset=2")
	if !slices.Equal(got, []string{"1.2.0", "1.0.0"}) || links["next"] != nil || links["previous"] == nil {
		t.Errorf("second page = %v %v", got, links)
	}

	// The collection's highest version is the highest release, not the pre-release.
	doc := decode(t, do(h, http.MethodGet, api+"collections/acme/tools/", nil))
	if hv, _ := doc["highest_version"].(map[string]any); hv["version"] != "1.10.0" {
		t.Errorf("highest_version = %v", doc["highest_version"])
	}

	for _, bad := range []string{"?limit=0", "?limit=x", "?offset=-1"} {
		if w := do(h, http.MethodGet, api+"collections/acme/tools/versions/"+bad, nil); w.Code != http.StatusBadRequest {
			t.Errorf("list %s = %d, want 400", bad, w.Code)
		}
	}

	if w := do(h, http.MethodGet, api+"collections/acme/nothing/versions/", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown collection = %d, want 404", w.Code)
	}

	if w := do(h, http.MethodGet, api+"collections/Bad-NS/tools/", nil); w.Code != http.StatusNotFound {
		t.Errorf("invalid namespace = %d, want 404", w.Code)
	}
}

func TestMetadataRevalidation(t *testing.T) {
	h := newHandlerOnly(t)
	if w, _ := publish(t, h, acme); w.Code != http.StatusAccepted {
		t.Fatalf("publish = %d", w.Code)
	}

	w := do(h, http.MethodGet, api+"collections/", nil)
	etag := w.Header().Get("ETag")

	if etag == "" {
		t.Fatal("no ETag")
	}

	if w = do(h, http.MethodGet, api+"collections/", nil, "If-None-Match", etag); w.Code != http.StatusNotModified {
		t.Errorf("revalidation = %d, want 304", w.Code)
	}
}

func TestImportTaskLookup(t *testing.T) {
	h := newHandlerOnly(t)

	for _, id := range []string{"not-a-uuid", "00000000-0000-4000-8000-000000000999"} {
		if w := do(h, http.MethodGet, api+"imports/collections/"+id+"/", nil); w.Code != http.StatusNotFound {
			t.Errorf("import %q = %d, want 404", id, w.Code)
		}
	}
}

func TestDownloadUnknownFile(t *testing.T) {
	h := newHandlerOnly(t)

	for _, f := range []string{"acme-tools-1.0.0.tar.gz", "junk", "..%2Fx-y-1.0.0.tar.gz"} {
		if w := do(h, http.MethodGet, publishPath+f, nil); w.Code != http.StatusNotFound {
			t.Errorf("download %q = %d, want 404", f, w.Code)
		}
	}
}

// TestPublishBase64Part publishes the way ansible-galaxy does: the file part is
// base64 encoded, with CRLF-wrapped lines, and says so in its transfer encoding.
func TestPublishBase64Part(t *testing.T) {
	h, db, _, _ := newTestHandler(t)
	data := ansibletest.Build(t, acme)
	digest := sha256.Sum256(data)

	var buf bytes.Buffer

	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("sha256", hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}

	part, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Disposition":       {`form-data; name="file"; filename="acme-tools-1.0.0.tar.gz"`},
		"Content-Type":              {"application/octet-stream"},
		"Content-Transfer-Encoding": {"base64"},
	})
	if err != nil {
		t.Fatal(err)
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 76 {
		_, _ = part.Write([]byte(encoded[:76] + "\r\n"))
		encoded = encoded[76:]
	}

	_, _ = part.Write([]byte(encoded + "\r\n"))

	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	w := do(h, http.MethodPost, publishPath, buf.Bytes(), "Content-Type", mw.FormDataContentType())
	if w.Code != http.StatusAccepted {
		t.Fatalf("publish = %d: %s", w.Code, w.Body.String())
	}

	if len(db.rows) != 1 || db.rows[0].SHA256 != hex.EncodeToString(digest[:]) || db.rows[0].SizeBytes != int64(len(data)) {
		t.Errorf("stored %+v, want the decoded artifact", db.rows)
	}
}
