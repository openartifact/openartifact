// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package nix

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ulikunitz/xz"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/formattest"
	"github.com/openartifact/openartifact/internal/webhook"
	"github.com/openartifact/openartifact/pkg/nix"
)

// nixDB is the in-memory nix_paths and nix_pending_nars tables, with the unique
// live store hash, the staging row dropped by the insert and a blob reference
// count over both.
type nixDB struct {
	*formattest.DB

	paths   map[string]*models.NixPath
	pending map[string]*models.NixPendingNar

	limit, used int64
	downloads   map[string]int
}

func newNixDB() *nixDB {
	db := &nixDB{
		DB: formattest.NewDB(), paths: map[string]*models.NixPath{},
		pending: map[string]*models.NixPendingNar{}, downloads: map[string]int{},
	}
	db.Repo.Format = FormatName

	return db
}

func (d *nixDB) ReserveStorageUsage(_ context.Context, _ string, size int64, _ int) (bool, error) {
	if d.limit > 0 && d.used+size > d.limit {
		return false, nil
	}

	d.used += size

	return true, nil
}

func (d *nixDB) DecrementStorageUsage(_ context.Context, _ string, size int64, _ int) error {
	d.used -= size
	return nil
}

func (d *nixDB) PutNixPendingNar(_ context.Context, n *models.NixPendingNar) error {
	d.pending[n.SHA256] = n
	return nil
}

func (d *nixDB) GetNixPendingNar(_ context.Context, _, sha string) (*models.NixPendingNar, error) {
	return d.pending[sha], nil
}

func (d *nixDB) GetNixPathByNarFile(_ context.Context, _, file string) (*models.NixPath, error) {
	for _, p := range d.paths {
		if p.NarFile == file {
			return p, nil
		}
	}

	return nil, nil //nolint:nilnil // absent
}

func (d *nixDB) GetNixPathByHash(_ context.Context, _, hash string) (*models.NixPath, error) {
	return d.paths[hash], nil
}

func (d *nixDB) CreateNixPath(_ context.Context, p *models.NixPath) error {
	if d.paths[p.StoreHash] != nil {
		return database.ErrDuplicate
	}

	p.ID = "path-" + p.StoreHash
	d.paths[p.StoreHash] = p
	delete(d.pending, p.SHA256)

	return nil
}

func (d *nixDB) DeleteNixPath(_ context.Context, _, id string) error {
	for hash, p := range d.paths {
		if p.ID == id {
			delete(d.paths, hash)
		}
	}

	return nil
}

func (d *nixDB) IncrementNixDownloadCount(_ context.Context, id, _ string) error {
	d.downloads[id]++
	return nil
}

func (d *nixDB) BlobReferenceCount(_ context.Context, sha string) (int, error) {
	n := 0

	for _, p := range d.paths {
		if p.SHA256 == sha {
			n++
		}
	}

	if d.pending[sha] != nil {
		n++
	}

	return n, nil
}

// fixedKey is a KeyProvider with one key (nil: unsigned).
type fixedKey struct{ key *nix.SecretKey }

func (f fixedKey) SecretKeyForRepo(context.Context, *models.Repository) (*nix.SecretKey, error) {
	return f.key, nil
}

type fixture struct {
	h   *HTTPHandler
	db  *nixDB
	bs  *formattest.BlobStore
	pub *formattest.Publisher
	key *nix.SecretKey
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	key, err := nix.GenerateKey("test-repo-1")
	if err != nil {
		t.Fatal(err)
	}

	db := newNixDB()
	bs := formattest.NewBlobStore()
	pub := &formattest.Publisher{}
	h := NewHTTPHandler(formattest.NewLogger(t), db, bs, "http://localhost").WithKeys(fixedKey{key})
	h.SetPublisher(pub)

	return &fixture{h: h, db: db, bs: bs, pub: pub, key: key}
}

func (f *fixture) do(method, target string, body []byte, headers ...string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == nil {
		req = formattest.Request(method, target, nil)
	} else {
		req = formattest.Request(method, target, bytes.NewReader(body))
	}

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}

	return formattest.Do(f.h.RegisterRepoRoutes, req)
}

const (
	hashA = "0123456789abcdfghijklmnpqrsvwxyz"
	hashB = "zyxwvsrqpnmlkjihgfdcba9876543210"
)

// storePath is one path as "nix copy" uploads it: the compressed NAR file and
// the narinfo that points to it.
type storePath struct {
	hash, name string
	nar        []byte
	file       string
	info       nix.NarInfo
}

func sha(b []byte) nix.SHA256 { return sha256.Sum256(b) }

func newStorePath(hash, name, content string) *storePath {
	nar := []byte("pretend this is an xz compressed NAR: " + content)
	fileHash := sha(nar)
	narHash := sha([]byte("uncompressed " + content))

	return &storePath{
		hash: hash, name: name, nar: nar, file: fileHash.Base32() + ".nar.xz",
		info: nix.NarInfo{
			StorePath: nix.StoreDir + "/" + hash + "-" + name, URL: "nar/" + fileHash.Base32() + ".nar.xz", Compression: "xz",
			FileHash: fileHash.String(), FileSize: int64(len(nar)), NarHash: narHash.String(), NarSize: 4096,
			References: []string{hash + "-" + name, hashB + "-dep-1.0"}, Deriver: hash + "-" + name + ".drv",
		},
	}
}

func (s *storePath) narinfo() []byte { return []byte(s.info.String()) }

// upload performs the PUTs of "nix copy": the NAR file, then the narinfo.
func (f *fixture) upload(t *testing.T, s *storePath) {
	t.Helper()

	if w := f.do(http.MethodPut, "/repos/myrepo/nar/"+s.file, s.nar); w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("PUT nar = %d: %s", w.Code, w.Body)
	}

	if w := f.do(http.MethodPut, "/repos/myrepo/"+s.hash+".narinfo", s.narinfo()); w.Code != http.StatusCreated {
		t.Fatalf("PUT narinfo = %d: %s", w.Code, w.Body)
	}
}

// verified checks that body carries a Sig by key that verifies over the
// fingerprint, and returns the parsed narinfo.
func verified(t *testing.T, body, publicKey string) *nix.NarInfo {
	t.Helper()

	info, err := nix.ParseNarInfo([]byte(body))
	if err != nil {
		t.Fatalf("served narinfo does not parse: %v\n%s", err, body)
	}

	name, b64, _ := strings.Cut(publicKey, ":")
	pub, _ := base64.StdEncoding.DecodeString(b64)
	fp, _ := info.Fingerprint()

	for _, sig := range info.Sigs {
		if n, v, _ := strings.Cut(sig, ":"); n == name {
			raw, _ := base64.StdEncoding.DecodeString(v)
			if !ed25519.Verify(pub, []byte(fp), raw) {
				t.Fatalf("signature by %s does not verify over %q", name, fp)
			}

			return info
		}
	}

	t.Fatalf("no signature by %s in\n%s", name, body)

	return nil
}

func TestCacheInfoAndPublicKey(t *testing.T) {
	f := newFixture(t)

	w := f.do(http.MethodGet, "/repos/myrepo/nix-cache-info", nil)
	if w.Code != http.StatusOK || w.Body.String() != "StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n" {
		t.Errorf("nix-cache-info = %d %q", w.Code, w.Body)
	}

	if w = f.do(http.MethodGet, "/repos/myrepo/public.key", nil); w.Code != http.StatusOK ||
		strings.TrimSpace(w.Body.String()) != f.key.PublicKey() {
		t.Errorf("public.key = %d %q", w.Code, w.Body)
	}

	if w = f.do(http.MethodPost, "/repos/myrepo/nix-cache-info", []byte("x")); w.Code != http.StatusMethodNotAllowed ||
		w.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST nix-cache-info = %d, Allow %q", w.Code, w.Header().Get("Allow"))
	}

	f.h.WithKeys(fixedKey{})

	if w = f.do(http.MethodGet, "/repos/myrepo/public.key", nil); w.Code != http.StatusNotFound {
		t.Errorf("public.key without a key = %d, want 404", w.Code)
	}
}

//nolint:gocyclo,cyclop // one scenario: probe, upload, download, repeat, delete
func TestUploadServeDelete(t *testing.T) {
	f := newFixture(t)
	s := newStorePath(hashA, "hello-2.12", "hello")

	if w := f.do(http.MethodGet, "/repos/myrepo/"+hashA+".narinfo", nil); w.Code != http.StatusNotFound {
		t.Fatalf("narinfo before upload = %d", w.Code)
	}

	if w := f.do(http.MethodHead, "/repos/myrepo/nar/"+s.file, nil); w.Code != http.StatusNotFound {
		t.Fatalf("HEAD nar before upload = %d", w.Code)
	}

	if w := f.do(http.MethodPut, "/repos/myrepo/nar/"+s.file, s.nar, "Content-Type", "application/x-nix-nar"); w.Code != http.StatusCreated {
		t.Fatalf("PUT nar = %d: %s", w.Code, w.Body)
	}

	// Staged: the uploader's HEAD finds it, nobody can read it, no path exists.
	if w := f.do(http.MethodHead, "/repos/myrepo/nar/"+s.file, nil); w.Code != http.StatusOK ||
		w.Header().Get("Content-Length") != strconv.Itoa(len(s.nar)) {
		t.Errorf("HEAD staged nar = %d, length %q", w.Code, w.Header().Get("Content-Length"))
	}

	if w := f.do(http.MethodGet, "/repos/myrepo/nar/"+s.file, nil); w.Code != http.StatusNotFound {
		t.Errorf("GET staged nar = %d, want 404", w.Code)
	}

	if w := f.do(http.MethodPut, "/repos/myrepo/nar/"+s.file, s.nar); w.Code != http.StatusOK {
		t.Errorf("repeated PUT nar = %d, want 200", w.Code)
	}

	if w := f.do(http.MethodPut, "/repos/myrepo/"+hashA+".narinfo", s.narinfo(), "Content-Type", "text/x-nix-narinfo"); w.Code != http.StatusCreated {
		t.Fatalf("PUT narinfo = %d: %s", w.Code, w.Body)
	}

	row := f.db.paths[hashA]
	if row == nil || row.Name != "hello-2.12" || row.NarFile != s.file || row.SHA256 != sha(s.nar).Hex() || len(f.db.pending) != 0 {
		t.Fatalf("row = %+v, pending %d", row, len(f.db.pending))
	}

	if f.db.used != int64(len(s.nar)) {
		t.Errorf("quota used = %d, want %d", f.db.used, len(s.nar))
	}

	if evs := f.pub.Events(); len(evs) != 1 || evs[0].Type != webhook.EventArtifactUploaded || evs[0].Artifact()["version"] != hashA {
		t.Errorf("events = %+v", evs)
	}

	w := f.do(http.MethodGet, "/repos/myrepo/"+hashA+".narinfo", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/x-nix-narinfo" {
		t.Fatalf("GET narinfo = %d %q", w.Code, w.Header().Get("Content-Type"))
	}

	served := verified(t, w.Body.String(), f.key.PublicKey())
	if served.StorePath != s.info.StorePath || served.NarHash != s.info.NarHash || served.FileHash != s.info.FileHash ||
		served.URL != s.info.URL || strings.Join(served.References, " ") != strings.Join(s.info.References, " ") {
		t.Errorf("served narinfo = %+v", served)
	}

	if w = f.do(http.MethodHead, "/repos/myrepo/"+hashA+".narinfo", nil); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD narinfo = %d with %d bytes", w.Code, w.Body.Len())
	}

	if w = f.do(http.MethodGet, "/repos/myrepo/nar/"+s.file, nil); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), s.nar) {
		t.Fatalf("GET nar = %d, %d bytes", w.Code, w.Body.Len())
	}

	if f.db.downloads["path-"+hashA] != 1 {
		t.Errorf("download count = %d", f.db.downloads["path-"+hashA])
	}

	// Publishing the same path again is a no-op; different contents conflict.
	if w = f.do(http.MethodPut, "/repos/myrepo/"+hashA+".narinfo", s.narinfo()); w.Code != http.StatusOK {
		t.Errorf("repeated narinfo = %d, want 200", w.Code)
	}

	other := newStorePath(hashA, "hello-2.12", "changed")
	f.do(http.MethodPut, "/repos/myrepo/nar/"+other.file, other.nar)

	if w = f.do(http.MethodPut, "/repos/myrepo/"+hashA+".narinfo", other.narinfo()); w.Code != http.StatusConflict {
		t.Errorf("narinfo with other contents = %d, want 409", w.Code)
	}

	if w = f.do(http.MethodDelete, "/repos/myrepo/"+hashA+".narinfo", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", w.Code)
	}

	if len(f.db.paths) != 0 || f.db.used != 0 {
		t.Errorf("after delete: rows=%d quota=%d", len(f.db.paths), f.db.used)
	}

	if _, kept := f.bs.Blobs[sha(s.nar).Hex()]; kept {
		t.Error("the NAR blob of the deleted path is still stored")
	}

	if w = f.do(http.MethodGet, "/repos/myrepo/"+hashA+".narinfo", nil); w.Code != http.StatusNotFound {
		t.Errorf("narinfo after delete = %d", w.Code)
	}
}

func TestUnsignedWithoutKey(t *testing.T) {
	f := newFixture(t)
	f.h.WithKeys(fixedKey{})

	s := newStorePath(hashA, "hello-2.12", "hello")
	s.info.Sigs = []string{"upstream-1:" + strings.Repeat("A", 86) + "=="}
	f.upload(t, s)

	w := f.do(http.MethodGet, "/repos/myrepo/"+hashA+".narinfo", nil)

	info, err := nix.ParseNarInfo(w.Body.Bytes())
	if err != nil || len(info.Sigs) != 1 || !strings.HasPrefix(info.Sigs[0], "upstream-1:") {
		t.Errorf("served sigs = %v (%v), want only the uploader's", info.Sigs, err)
	}
}

func TestSigningReplacesOwnOlderSignature(t *testing.T) {
	f := newFixture(t)
	s := newStorePath(hashA, "hello-2.12", "hello")
	s.info.Sigs = []string{f.key.Name() + ":" + strings.Repeat("A", 86) + "==", "upstream-1:" + strings.Repeat("B", 86) + "=="}
	f.upload(t, s)

	w := f.do(http.MethodGet, "/repos/myrepo/"+hashA+".narinfo", nil)
	info := verified(t, w.Body.String(), f.key.PublicKey())

	if len(info.Sigs) != 2 || !strings.HasPrefix(info.Sigs[0], "upstream-1:") {
		t.Errorf("sigs = %v, want the upstream one and one by %s", info.Sigs, f.key.Name())
	}
}

func TestKeyRotationResignsAtOnce(t *testing.T) {
	f := newFixture(t)
	s := newStorePath(hashA, "hello-2.12", "hello")
	f.upload(t, s)

	next, err := nix.GenerateKey("test-repo-2")
	if err != nil {
		t.Fatal(err)
	}

	f.h.WithKeys(fixedKey{next})

	w := f.do(http.MethodGet, "/repos/myrepo/"+hashA+".narinfo", nil)
	verified(t, w.Body.String(), next.PublicKey())
}

func TestNarUploadValidation(t *testing.T) {
	f := newFixture(t)
	s := newStorePath(hashA, "hello-2.12", "hello")

	for name, tc := range map[string]struct {
		file string
		body []byte
		want int
	}{
		"content does not match its name": {s.file, []byte("something else"), http.StatusBadRequest},
		"not a nar file name":             {"hello.nar", s.nar, http.StatusBadRequest},
		"malformed extension":             {sha(s.nar).Base32() + ".nar.XZ", s.nar, http.StatusBadRequest},
	} {
		if w := f.do(http.MethodPut, "/repos/myrepo/nar/"+tc.file, tc.body); w.Code != tc.want {
			t.Errorf("%s: status %d, want %d: %s", name, w.Code, tc.want, w.Body)
		}
	}

	if len(f.db.pending) != 0 || len(f.bs.Blobs) != 0 {
		t.Errorf("a refused NAR left %d staging rows and %d blobs", len(f.db.pending), len(f.bs.Blobs))
	}
}

func TestNarInfoUploadValidation(t *testing.T) {
	f := newFixture(t)
	s := newStorePath(hashA, "hello-2.12", "hello")

	mutate := func(edit func(*nix.NarInfo)) []byte {
		n := s.info
		edit(&n)

		return []byte(n.String())
	}

	// Without its NAR, a narinfo is refused.
	if w := f.do(http.MethodPut, "/repos/myrepo/"+hashA+".narinfo", s.narinfo()); w.Code != http.StatusConflict {
		t.Fatalf("narinfo before its NAR = %d, want 409", w.Code)
	}

	f.do(http.MethodPut, "/repos/myrepo/nar/"+s.file, s.nar)

	for name, tc := range map[string]struct {
		target string
		body   []byte
	}{
		"junk":                  {hashA, []byte("hello")},
		"other store path":      {hashB, s.narinfo()},
		"file hash not the URL": {hashA, mutate(func(n *nix.NarInfo) { n.FileHash = sha([]byte("x")).String() })},
		"file size is wrong":    {hashA, mutate(func(n *nix.NarInfo) { n.FileSize++ })},
		"url outside nar/":      {hashA, mutate(func(n *nix.NarInfo) { n.URL = "../" + s.file })},
		"other extension":       {hashA, mutate(func(n *nix.NarInfo) { n.URL = "nar/" + sha(s.nar).Base32() + ".nar" })},
	} {
		if w := f.do(http.MethodPut, "/repos/myrepo/"+tc.target+".narinfo", tc.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", name, w.Code, w.Body)
		}
	}

	if len(f.db.paths) != 0 {
		t.Error("a refused narinfo created a path")
	}

	if w := f.do(http.MethodPut, "/repos/myrepo/"+hashA+".narinfo", s.narinfo(), "Content-Encoding", "snappy"); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("unsupported Content-Encoding = %d, want 415", w.Code)
	}
}

func TestNarInfoContentEncoding(t *testing.T) {
	var xzBuf, gzBuf bytes.Buffer

	plain := newStorePath(hashA, "hello-2.12", "hello").narinfo()

	xw, err := xz.NewWriter(&xzBuf)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = xw.Write(plain)
	_ = xw.Close()

	gw := gzip.NewWriter(&gzBuf)
	_, _ = gw.Write(plain)
	_ = gw.Close()

	for enc, body := range map[string][]byte{"xz": xzBuf.Bytes(), "gzip": gzBuf.Bytes()} {
		f := newFixture(t)
		s := newStorePath(hashA, "hello-2.12", "hello")
		f.do(http.MethodPut, "/repos/myrepo/nar/"+s.file, s.nar)

		if w := f.do(http.MethodPut, "/repos/myrepo/"+hashA+".narinfo", body, "Content-Encoding", enc); w.Code != http.StatusCreated {
			t.Errorf("%s narinfo = %d: %s", enc, w.Code, w.Body)
		}
	}
}

func TestUncompressedNarWithoutFileHash(t *testing.T) {
	f := newFixture(t)
	nar := []byte("an uncompressed NAR")
	narHash := sha(nar)
	n := nix.NarInfo{
		StorePath: nix.StoreDir + "/" + hashA + "-plain-1", URL: "nar/" + narHash.Base32() + ".nar", Compression: "none",
		NarHash: narHash.String(), NarSize: int64(len(nar)),
	}

	f.do(http.MethodPut, "/repos/myrepo/nar/"+narHash.Base32()+".nar", nar)

	if w := f.do(http.MethodPut, "/repos/myrepo/"+hashA+".narinfo", []byte(n.String())); w.Code != http.StatusCreated {
		t.Fatalf("narinfo = %d: %s", w.Code, w.Body)
	}

	w := f.do(http.MethodGet, "/repos/myrepo/"+hashA+".narinfo", nil)

	got := verified(t, w.Body.String(), f.key.PublicKey())
	if got.FileHash != narHash.String() || got.FileSize != int64(len(nar)) {
		t.Errorf("FileHash %s, FileSize %d", got.FileHash, got.FileSize)
	}
}

func TestPathsShareANar(t *testing.T) {
	f := newFixture(t)
	a := newStorePath(hashA, "one-1", "same")
	b := newStorePath(hashB, "two-1", "same")

	f.upload(t, a)

	// The second path finds the NAR stored under its hash: no second upload.
	if w := f.do(http.MethodPut, "/repos/myrepo/nar/"+b.file, b.nar); w.Code != http.StatusOK {
		t.Fatalf("PUT of a stored nar = %d, want 200", w.Code)
	}

	if w := f.do(http.MethodPut, "/repos/myrepo/"+hashB+".narinfo", b.narinfo()); w.Code != http.StatusCreated {
		t.Fatalf("second narinfo = %d: %s", w.Code, w.Body)
	}

	if w := f.do(http.MethodDelete, "/repos/myrepo/"+hashA+".narinfo", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", w.Code)
	}

	if w := f.do(http.MethodGet, "/repos/myrepo/nar/"+b.file, nil); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), b.nar) {
		t.Errorf("the shared NAR after deleting one path = %d", w.Code)
	}
}

func TestQuotaRefusal(t *testing.T) {
	f := newFixture(t)
	f.db.limit = 10

	s := newStorePath(hashA, "hello-2.12", "hello")
	f.do(http.MethodPut, "/repos/myrepo/nar/"+s.file, s.nar)

	if w := f.do(http.MethodPut, "/repos/myrepo/"+hashA+".narinfo", s.narinfo()); w.Code != http.StatusInsufficientStorage {
		t.Errorf("narinfo over quota = %d, want 507", w.Code)
	}

	if len(f.db.paths) != 0 || f.db.used != 0 {
		t.Errorf("rows=%d used=%d after a refused path", len(f.db.paths), f.db.used)
	}
}

func TestListingsAreDiscarded(t *testing.T) {
	f := newFixture(t)

	for _, target := range []string{"/repos/myrepo/" + hashA + ".ls", "/repos/myrepo/debuginfo/abcdef"} {
		if w := f.do(http.MethodPut, target, []byte(`{"version":1}`)); w.Code != http.StatusOK {
			t.Errorf("PUT %s = %d", target, w.Code)
		}

		if w := f.do(http.MethodGet, target, nil); w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, w.Code)
		}
	}

	if len(f.bs.Blobs) != 0 {
		t.Error("a listing was stored")
	}
}

func TestQuarantinedPathIsNotServed(t *testing.T) {
	f := newFixture(t)
	s := newStorePath(hashA, "hello-2.12", "hello")
	f.upload(t, s)

	f.db.Keys[hashA+".narinfo"] = true
	f.db.Keys["nar/"+s.file] = true

	for _, target := range []string{"/repos/myrepo/" + hashA + ".narinfo", "/repos/myrepo/nar/" + s.file} {
		if w := f.do(http.MethodGet, target, nil); w.Code == http.StatusOK {
			t.Errorf("GET %s of a quarantined path = 200", target)
		}
	}
}

func TestOtherFormatIsNotFound(t *testing.T) {
	f := newFixture(t)
	f.db.Repo.Format = "raw"

	if w := f.do(http.MethodGet, "/repos/myrepo/nix-cache-info", nil); w.Code != http.StatusNotFound {
		t.Errorf("nix-cache-info of a raw repository = %d, want 404", w.Code)
	}
}

// TestGuardClaimsOnlyNixRepositories drives the guard the way the server
// mounts it, in front of another format's catch-all route.
func TestGuardClaimsOnlyNixRepositories(t *testing.T) {
	f := newFixture(t)
	s := newStorePath(hashA, "hello-2.12", "hello")
	f.upload(t, s)

	router := chi.NewRouter()
	router.Route("/repos/{name}", func(r chi.Router) {
		r.Use(f.h.Guard)
		r.Get("/*", func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("X-Other", "1") })
	})

	get := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, formattest.Request(http.MethodGet, target, nil))

		return w
	}

	if w := get("/repos/myrepo/" + hashA + ".narinfo"); w.Code != http.StatusOK || w.Header().Get("X-Other") != "" {
		t.Errorf("narinfo through the guard = %d", w.Code)
	}

	if w := get("/repos/myrepo/some/other/path"); w.Header().Get("X-Other") != "1" {
		t.Error("a path that is not a nix path did not reach the next handler")
	}

	f.db.Repo.Format = "raw"

	if w := get("/repos/myrepo/" + hashA + ".narinfo"); w.Header().Get("X-Other") != "1" {
		t.Error("a request for a repository of another format was claimed")
	}
}
