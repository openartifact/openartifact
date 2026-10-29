// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package osv

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// fakeStore is an in-memory Store: stored holds the advisory ids per ecosystem.
type fakeStore struct {
	stored    map[string][]string
	applied   []string
	removed   [][]string
	listErr   error
	removeErr error
}

func (f *fakeStore) ApplyOSVAdvisories(_ context.Context, advisories []models.OSVAdvisory) ([]models.OSVPackageKey, error) {
	var keys []models.OSVPackageKey

	for i := range advisories {
		f.applied = append(f.applied, advisories[i].ID)
		keys = append(keys, models.OSVPackageKey{Ecosystem: "npm", NameNorm: "applied-" + advisories[i].ID})
	}

	return keys, nil
}

func (f *fakeStore) ListOSVAdvisoryIDs(_ context.Context, ecosystem string) ([]string, error) {
	return slices.Clone(f.stored[ecosystem]), f.listErr
}

func (f *fakeStore) RemoveOSVAdvisories(_ context.Context, ecosystem string, ids []string) ([]models.OSVPackageKey, error) {
	if f.removeErr != nil {
		return nil, f.removeErr
	}

	f.removed = append(f.removed, slices.Clone(ids))

	keys := make([]models.OSVPackageKey, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, models.OSVPackageKey{Ecosystem: ecosystem, NameNorm: "removed-" + id})
	}

	return keys, nil
}

func (f *fakeStore) removedIDs() []string {
	var all []string
	for _, r := range f.removed {
		all = append(all, r...)
	}

	slices.Sort(all)

	return all
}

func recordJSON(id, summary string) string {
	return fmt.Sprintf(`{"id":%q,"modified":"2025-01-02T03:04:05Z","summary":%q,`+
		`"affected":[{"package":{"ecosystem":"npm","name":"pkg-%s"}}]}`, id, summary, id)
}

// archiveServer serves a zip of the given files (name → content) at any path.
func archiveServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()

	return archiveServerWithHeader(t, files, nil)
}

// archiveServerWithHeader is archiveServer with extra response headers, which
// may depend on the bytes of the archive.
func archiveServerWithHeader(t *testing.T, files map[string]string, header func(archive []byte) http.Header) *httptest.Server {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}

	slices.Sort(names)

	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if header != nil {
			for k, vs := range header(buf.Bytes()) {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
		}

		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)

	return srv
}

func importerWith(t *testing.T, srv *httptest.Server, store Store, maxBytes int64) *Importer {
	t.Helper()

	log, err := logger.New("error", "json", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	imp, err := NewImporter(store, Options{
		BaseURL: srv.URL, Ecosystems: []string{"npm"}, TempDir: t.TempDir(), MaxArchiveBytes: maxBytes, Client: srv.Client(),
	}, log)
	if err != nil {
		t.Fatal(err)
	}

	return imp
}

// A record the export no longer lists is removed from the ecosystem; one that
// is listed is kept whether it parsed, was malformed, or sits in a file named
// differently from its id.
func TestImportRemovesAdvisoriesMissingFromTheArchive(t *testing.T) {
	t.Parallel()

	srv := archiveServer(t, map[string]string{
		"A.json":      recordJSON("A", "kept"),
		"C.json":      recordJSON("C", "kept"),
		"weird.json":  recordJSON("E", "id differs from the file name"),
		"D.json":      "{not json",
		"notes.txt":   "ignored",
		"sub/F.json":  recordJSON("F", "in a directory"),
		"README.json": recordJSON("README", "kept"),
	})
	store := &fakeStore{stored: map[string][]string{"npm": {"A", "B", "C", "D", "E", "F", "G", "README"}}}
	imp := importerWith(t, srv, store, 1<<20)

	st := &models.OSVMirrorState{Ecosystem: "npm"}

	res, err := imp.Import(context.Background(), "npm", st)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if got, want := store.removedIDs(), []string{"B", "G"}; !slices.Equal(got, want) {
		t.Errorf("removed = %v, want %v", got, want)
	}

	if res.Removed != 2 {
		t.Errorf("Removed = %d, want 2", res.Removed)
	}

	for _, id := range []string{"B", "G"} {
		if _, ok := res.Touched[models.OSVPackageKey{Ecosystem: "npm", NameNorm: "removed-" + id}]; !ok {
			t.Errorf("the packages of the removed advisory %s must be re-matched: %v", id, res.Touched)
		}
	}

	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want the one malformed record", res.Skipped)
	}
}

// An export with no record at all is not a statement that every advisory is
// gone: nothing is removed.
func TestImportDoesNotRemoveAnythingFromAnEmptyArchive(t *testing.T) {
	t.Parallel()

	srv := archiveServer(t, map[string]string{"README.txt": "no records here"})
	store := &fakeStore{stored: map[string][]string{"npm": {"A", "B"}}}

	if _, err := importerWith(t, srv, store, 1<<20).Import(context.Background(), "npm", &models.OSVMirrorState{}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(store.removed) != 0 {
		t.Fatalf("removed = %v, want nothing", store.removed)
	}
}

// The removals go to the database in batches, not as one statement.
func TestImportRemovesInBatches(t *testing.T) {
	t.Parallel()

	stored := make([]string, 0, 3*batchSize/2+1)
	for i := range 3 * batchSize / 2 {
		stored = append(stored, fmt.Sprintf("OLD-%04d", i))
	}

	stored = append(stored, "A")

	srv := archiveServer(t, map[string]string{"A.json": recordJSON("A", "kept")})
	store := &fakeStore{stored: map[string][]string{"npm": stored}}

	if _, err := importerWith(t, srv, store, 1<<20).Import(context.Background(), "npm", &models.OSVMirrorState{}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(store.removed) != 2 || len(store.removedIDs()) != 3*batchSize/2 {
		t.Fatalf("removal calls = %d, ids = %d; want 2 calls of at most %d covering %d ids",
			len(store.removed), len(store.removedIDs()), batchSize, 3*batchSize/2)
	}

	for _, batch := range store.removed {
		if len(batch) > batchSize {
			t.Errorf("a removal of %d ids exceeds the batch size %d", len(batch), batchSize)
		}
	}
}

// A failed removal fails the import and leaves the state where it was, so the
// next run reads the archive again.
func TestImportFailureOfTheRemovalKeepsTheState(t *testing.T) {
	t.Parallel()

	boom := errors.New("database down")

	for name, store := range map[string]*fakeStore{
		"list":   {stored: map[string][]string{"npm": {"B"}}, listErr: boom},
		"remove": {stored: map[string][]string{"npm": {"B"}}, removeErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := archiveServer(t, map[string]string{"A.json": recordJSON("A", "kept")})
			st := &models.OSVMirrorState{Ecosystem: "npm"}

			if _, err := importerWith(t, srv, store, 1<<20).Import(context.Background(), "npm", st); !errors.Is(err, boom) {
				t.Fatalf("Import error = %v, want the database error", err)
			}

			if st.HighWater != nil || st.ETag != "" || st.LastImportedAt != nil {
				t.Fatalf("a failed import must not advance the state: %+v", st)
			}
		})
	}
}

// The decompressed size of an archive is bounded as a whole, not only per
// record: a small download must not expand to an unbounded amount of data.
func TestImportCapsTheDecompressedTotal(t *testing.T) {
	t.Parallel()

	const (
		maxArchive = 4096
		recordSize = 32 << 10
	)

	files := map[string]string{}
	for i := range 8 { // 8 × 32 KiB is above the budget of maxArchive × maxDecompressRatio
		files[fmt.Sprintf("R%d.json", i)] = recordJSON(fmt.Sprintf("R%d", i), strings.Repeat("x", recordSize))
	}

	srv := archiveServer(t, files)
	store := &fakeStore{}
	st := &models.OSVMirrorState{Ecosystem: "npm"}

	_, err := importerWith(t, srv, store, maxArchive).Import(context.Background(), "npm", st)
	if !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("Import error = %v, want ErrArchiveTooLarge", err)
	}

	if st.HighWater != nil {
		t.Fatalf("a failed import must not advance the state: %+v", st)
	}

	// The same archive with room for it imports.
	if _, err := importerWith(t, srv, &fakeStore{}, 1<<20).Import(context.Background(), "npm", &models.OSVMirrorState{}); err != nil {
		t.Fatalf("an archive within the budget must import: %v", err)
	}
}

func crc32cHeader(archive []byte) string {
	return "crc32c=" + base64.StdEncoding.EncodeToString(binary.BigEndian.AppendUint32(nil, crc32.Checksum(archive, crc32.MakeTable(crc32.Castagnoli))))
}

// The checksum Google Cloud Storage sends with an export (X-Goog-Hash) is
// checked against the bytes received: a corrupted download is refused and
// nothing of it is applied. A source without the header is accepted as before.
func TestImportVerifiesTheArchiveChecksum(t *testing.T) {
	t.Parallel()

	files := map[string]string{"A.json": recordJSON("A", "kept")}

	tests := []struct {
		name    string
		header  func(archive []byte) http.Header
		wantErr bool
	}{
		{"matching", func(a []byte) http.Header { return http.Header{"X-Goog-Hash": {crc32cHeader(a)}} }, false},
		{"matching with md5 first", func(a []byte) http.Header {
			return http.Header{"X-Goog-Hash": {"md5=AAAAAAAAAAAAAAAAAAAAAA==", crc32cHeader(a)}}
		}, false},
		{"matching in one list", func(a []byte) http.Header {
			return http.Header{"X-Goog-Hash": {"md5=AAAAAAAAAAAAAAAAAAAAAA==, " + crc32cHeader(a)}}
		}, false},
		{"corrupted", func([]byte) http.Header { return http.Header{"X-Goog-Hash": {"crc32c=AAAAAA=="}} }, true},
		{"not a checksum", func([]byte) http.Header { return http.Header{"X-Goog-Hash": {"crc32c=!!"}} }, true},
		{"only an algorithm it does not know", func([]byte) http.Header { return http.Header{"X-Goog-Hash": {"md5=AAAAAAAAAAAAAAAAAAAAAA=="}} }, false},
		{"no checksum", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := archiveServerWithHeader(t, files, tt.header)
			store := &fakeStore{}
			imp := importerWith(t, srv, store, 1<<20)
			st := &models.OSVMirrorState{Ecosystem: "npm"}

			_, err := imp.Import(context.Background(), "npm", st)

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Import: %v", err)
				}

				return
			}

			if !errors.Is(err, ErrChecksumMismatch) {
				t.Fatalf("Import error = %v, want ErrChecksumMismatch", err)
			}

			if len(store.applied) != 0 || st.HighWater != nil || st.ETag != "" {
				t.Fatalf("a refused archive must change nothing: applied %v, state %+v", store.applied, st)
			}

			if left, _ := os.ReadDir(imp.opts.TempDir); len(left) != 0 {
				t.Fatalf("the refused archive is left in the temp dir: %v", left)
			}
		})
	}
}
