// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/backup/backuptest"
	"github.com/openartifact/openartifact/internal/database/models"
)

// pagedRows serves rows 1-based page by page like the store list methods.
func pagedRows[T any](all []T, page int) []T {
	start := (page - 1) * listPageSize
	if start >= len(all) {
		return nil
	}

	return all[start:min(start+listPageSize, len(all))]
}

// TestListAll_ReadsEveryPage: a table larger than a page (and one of exactly
// N pages) is read completely instead of cut at the first page.
func TestListAll_ReadsEveryPage(t *testing.T) {
	for _, n := range []int{0, 1, listPageSize, listPageSize + 5, 2 * listPageSize, 2*listPageSize + 1} {
		all := make([]int, n)
		for i := range all {
			all[i] = i
		}

		got, err := listAll("numbers", func(page int) ([]int, int, error) {
			return pagedRows(all, page), n, nil
		})
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}

		if len(got) != n {
			t.Fatalf("n=%d: read %d rows", n, len(got))
		}

		got, err = listAll("numbers", func(page int) ([]int, int, error) {
			return pagedRows(all, page), unknownTotal, nil
		})
		if err != nil || len(got) != n {
			t.Fatalf("n=%d unknown total: read %d rows, err %v", n, len(got), err)
		}
	}
}

// TestListAll_FailsWhenRowsAreMissing: pages that do not add up to the table's
// total fail the export instead of producing a truncated table.
func TestListAll_FailsWhenRowsAreMissing(t *testing.T) {
	all := make([]int, listPageSize+3)

	_, err := listAll("numbers", func(page int) ([]int, int, error) {
		return pagedRows(all, page), len(all) + 10, nil
	})
	if !errors.Is(err, errListTruncated) {
		t.Fatalf("err = %v, want errListTruncated", err)
	}

	boom := errors.New("boom")

	_, err = listAll("numbers", func(int) ([]int, int, error) { return nil, 0, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the list error", err)
	}
}

// bundleSnapshot serves release bundles page by page.
type bundleSnapshot struct {
	*backuptest.LiveSnapshot

	bundles []*models.ReleaseBundle
}

func (s *bundleSnapshot) ListReleaseBundles(
	_ context.Context, _, _ string, page, limit int,
) ([]*models.ReleaseBundle, int, error) {
	if limit != listPageSize {
		return nil, 0, fmt.Errorf("limit = %d, want %d", limit, listPageSize)
	}

	return pagedRows(s.bundles, page), len(s.bundles), nil
}

func (*bundleSnapshot) ListBundleItems(context.Context, string) ([]*models.ReleaseBundleItem, error) {
	return nil, nil
}

func (*bundleSnapshot) ListBundlePromotions(context.Context, string) ([]*models.BundlePromotion, error) {
	return nil, nil
}

// tableEntry returns the content of one archive entry.
func tableEntry(t *testing.T, raw []byte, name string) []byte {
	t.Helper()

	tr := tar.NewReader(bytes.NewReader(raw))

	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			t.Fatalf("archive has no %s", name)
		}

		if err != nil {
			t.Fatalf("read archive: %v", err)
		}

		if h.Name == name {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}

			return b
		}
	}
}

// TestExportBundles_ExportsMoreThanOnePageOfBundles: a backup of an instance
// with more bundles than one page holds all of them.
func TestExportBundles_ExportsMoreThanOnePageOfBundles(t *testing.T) {
	const total = 2*listPageSize + 7

	bundles := make([]*models.ReleaseBundle, total)
	for i := range bundles {
		bundles[i] = &models.ReleaseBundle{ID: fmt.Sprintf("b%d", i), Name: "bundle", Version: strconv.Itoa(i)}
	}

	x := newExporter(t, newFakeBlobStore(0), &bundleSnapshot{LiveSnapshot: &backuptest.LiveSnapshot{}, bundles: bundles})

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)
	manifest := &BackupManifest{TableCounts: map[string]int{}}

	if err := x.exportBundles(context.Background(), tw, manifest); err != nil {
		t.Fatalf("exportBundles: %v", err)
	}

	_ = tw.Close()

	var got []*models.ReleaseBundle
	if err := json.Unmarshal(tableEntry(t, buf.Bytes(), "release_bundles.jsonl"), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(got) != total || manifest.TableCounts["release_bundles"] != total {
		t.Fatalf("archive holds %d bundles (manifest %d), want %d", len(got), manifest.TableCounts["release_bundles"], total)
	}
}

// tagSnapshot returns a docker tag list as long as the limit it was given.
type tagSnapshot struct {
	*backuptest.LiveSnapshot
}

func (tagSnapshot) ListDockerBlobs(context.Context, string) ([]*models.DockerBlob, error) {
	return nil, nil
}

func (tagSnapshot) ListDockerManifestsByRepo(context.Context, string) ([]*models.DockerManifest, error) {
	return nil, nil
}

func (tagSnapshot) ListDockerTagsByRepo(_ context.Context, _ string, limit int) ([]*models.DockerTag, error) {
	return make([]*models.DockerTag, limit), nil
}

// TestExportDockerTables_FailsWhenTagsAreCutAtTheLimit: a repository with as
// many tags as the single-call list can return fails the backup rather than
// silently exporting the first ones.
func TestExportDockerTables_FailsWhenTagsAreCutAtTheLimit(t *testing.T) {
	x := newExporter(t, newFakeBlobStore(0), tagSnapshot{LiveSnapshot: &backuptest.LiveSnapshot{}})

	var pkg packageExport

	x.exportDockerTables(context.Background(), &models.Repository{ID: "r1"}, &pkg)

	if len(pkg.errs) != 1 || !errors.Is(pkg.errs[0], errListTruncated) {
		t.Fatalf("errs = %v, want one errListTruncated", pkg.errs)
	}
}

// countingReader counts the bytes handed out.
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n

	return n, err
}

// TestDecodeEach_StreamsRecords: the first record is handed over long before
// the entry has been read, so a restore inserts as it reads instead of holding
// the whole table.
func TestDecodeEach_StreamsRecords(t *testing.T) {
	const records = 20000

	type rec struct {
		Name string `json:"name"`
	}

	var table bytes.Buffer

	table.WriteByte('[')

	for i := range records {
		if i > 0 {
			table.WriteByte(',')
		}

		fmt.Fprintf(&table, `{"name":"record-%d"}`, i)
	}

	table.WriteByte(']')

	size := table.Len()
	src := &countingReader{r: &table}

	var (
		seen        int
		readAtFirst int
	)

	for r, err := range decodeEach[*rec](src) {
		if err != nil {
			t.Fatalf("decode: %v", err)
		}

		if seen == 0 {
			readAtFirst = src.n
		}

		if want := fmt.Sprintf("record-%d", seen); r.Name != want {
			t.Fatalf("record %d = %q, want %q", seen, r.Name, want)
		}

		seen++
	}

	if seen != records {
		t.Fatalf("decoded %d records, want %d", seen, records)
	}

	if readAtFirst > size/10 {
		t.Errorf("first record yielded after %d of %d bytes were read", readAtFirst, size)
	}
}

func TestDecodeEach_Tables(t *testing.T) {
	type rec struct {
		N int `json:"n"`
	}

	cases := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"records", `[{"n":1},{"n":2}]`, 2, false},
		{"empty array", `[]`, 0, false},
		{"null table", `null`, 0, false},
		{"null element skipped", `[{"n":1},null,{"n":2}]`, 2, false},
		{"truncated after a record", `[{"n":1},{"n":2}`, 2, true},
		{"truncated inside a record", `[{"n":1},{"n":`, 1, true},
		{"not an array", `{"n":1}`, 0, true},
		{"empty entry", ``, 0, true},
		{"bad element", `[{"n":"x"}]`, 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				got    int
				gotErr error
			)

			for _, err := range decodeEach[*rec](strings.NewReader(tc.in)) {
				if err != nil {
					gotErr = err
					break
				}

				got++
			}

			if (gotErr != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("decoded %d records, err %v; want %d, error %v", got, gotErr, tc.want, tc.wantErr)
			}
		})
	}
}

func TestRedactedStorageConfig_WalksNestedObjects(t *testing.T) {
	raw := `{"bucket":"b","secret_key":"s","options":{"region":"r","sasToken":"t","nested":[{"connStr":"c","keep":1}]},` +
		`"credentials_json":"{}"}`

	out, err := redactedStorageConfig([]byte(raw))
	if err != nil {
		t.Fatalf("redactedStorageConfig: %v", err)
	}

	want := map[string]any{
		"bucket":  "b",
		"options": map[string]any{"region": "r", "nested": []any{map[string]any{"keep": float64(1)}}},
	}

	if wantJSON, _ := json.Marshal(want); !bytes.Equal(out, wantJSON) {
		t.Fatalf("redacted = %s, want %s", out, wantJSON)
	}
}

func TestRedactOIDCSecret_WalksNestedObjects(t *testing.T) {
	out, err := redactOIDCSecret(json.RawMessage(`{"issuer":"i","client_secret":"s","extra":{"client_secret":"s2","a":1}}`))
	if err != nil {
		t.Fatalf("redactOIDCSecret: %v", err)
	}

	if want := `{"extra":{"a":1},"issuer":"i"}`; string(out) != want {
		t.Fatalf("redacted = %s, want %s", out, want)
	}
}

// TestRedaction_FailsOnUnredactableConfig: a config whose secrets cannot be
// located is an error (the backup fails), not a silently empty config.
func TestRedaction_FailsOnUnredactableConfig(t *testing.T) {
	for _, raw := range []string{`["secret"]`, `"s"`, `{"a":`} {
		if _, err := redactedStorageConfig([]byte(raw)); !errors.Is(err, errUnredactableConfig) {
			t.Errorf("storage %s: err = %v, want errUnredactableConfig", raw, err)
		}

		if _, err := redactOIDCSecret(json.RawMessage(raw)); !errors.Is(err, errUnredactableConfig) {
			t.Errorf("oidc %s: err = %v, want errUnredactableConfig", raw, err)
		}
	}

	for _, raw := range []string{``, `null`} {
		if out, err := redactedStorageConfig([]byte(raw)); err != nil || string(out) != raw {
			t.Errorf("storage %q: out %q err %v, want it unchanged", raw, out, err)
		}
	}
}

// TestRedactEach_LeavesListedRowsUntouched: redaction edits copies.
func TestRedactEach_LeavesListedRowsUntouched(t *testing.T) {
	orig := &models.StorageBackend{Config: []byte(`{"secretKey":"s","bucket":"b"}`)}
	rows := []*models.StorageBackend{orig, nil}

	err := redactEach(rows, func(b *models.StorageBackend) error {
		cfg, err := redactedStorageConfig(b.Config)
		b.Config = cfg

		return err
	})
	if err != nil {
		t.Fatalf("redactEach: %v", err)
	}

	if string(orig.Config) != `{"secretKey":"s","bucket":"b"}` {
		t.Errorf("listed row was modified: %s", orig.Config)
	}

	if string(rows[0].Config) != `{"bucket":"b"}` || rows[1] != nil {
		t.Errorf("rows = %s, %v", rows[0].Config, rows[1])
	}
}
