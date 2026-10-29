// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package search

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func seedChecksumData(t *testing.T, db *sql.DB) (uuid.UUID, uuid.UUID) {
	t.Helper()

	var readable, hidden uuid.UUID

	stmts := []string{
		`INSERT INTO blobs (id, sha256, size_bytes, blob_key)
		 VALUES ('00000000-0000-0000-0000-0000000000c1', repeat('c', 64), 7, 'checksum-test')`,
		`INSERT INTO repositories (name, format, project_id)
		 VALUES ('ck-visible', 'npm', '00000000-0000-0000-0000-000000000001'),
		        ('ck-hidden', 'npm', '00000000-0000-0000-0000-000000000001')`,
		`INSERT INTO packages (repository_id, format, name, name_key)
		 SELECT id, 'npm', 'widget', 'widget' FROM repositories WHERE name LIKE 'ck-%'`,
		`INSERT INTO package_versions (package_id, repository_id, version, version_key)
		 SELECT id, repository_id, '1.0.0', '1.0.0' FROM packages`,
		`INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, checksums)
		 SELECT id, repository_id, 'widget/-/widget-1.0.0.tgz', 'widget-1.0.0.tgz',
		        '00000000-0000-0000-0000-0000000000c1', jsonb_build_object('shasum', repeat('d', 40), 'integrity', 'x')
		 FROM package_versions`,
	}

	for _, s := range stmts {
		if _, err := db.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("seed: %v\n%s", err, s)
		}
	}

	for name, dst := range map[string]*uuid.UUID{"ck-visible": &readable, "ck-hidden": &hidden} {
		if err := db.QueryRowContext(context.Background(), `SELECT id FROM repositories WHERE name = $1`, name).Scan(dst); err != nil {
			t.Fatalf("repository %s: %v", name, err)
		}
	}

	return readable, hidden
}

func TestSearchChecksumFindsFilesInReadableRepositoriesOnly(t *testing.T) {
	db := newMigratedSearchDB(t)
	e := NewExecutor(db)
	ctx := context.Background()
	visible, hidden := seedChecksumData(t, db)

	sha256 := strings.Repeat("c", 64)
	sha1 := strings.Repeat("d", 40)

	for _, tc := range []struct {
		name, digest, algorithm string
		readable                map[uuid.UUID]struct{}
		want                    int
	}{
		{"sha256 through the blob store", sha256, AlgorithmSHA256, map[uuid.UUID]struct{}{visible: {}}, 1},
		{"sha1 recorded by npm", sha1, AlgorithmSHA1, map[uuid.UUID]struct{}{visible: {}}, 1},
		{"both repositories readable", sha1, AlgorithmSHA1, map[uuid.UUID]struct{}{visible: {}, hidden: {}}, 2},
		{"no readable repository", sha256, AlgorithmSHA256, map[uuid.UUID]struct{}{}, 0},
		{"unknown sha256", strings.Repeat("e", 64), AlgorithmSHA256, map[uuid.UUID]struct{}{visible: {}}, 0},
		{"unknown md5", strings.Repeat("f", 32), AlgorithmMD5, map[uuid.UUID]struct{}{visible: {}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := e.SearchChecksum(ctx, tc.digest, tc.algorithm, 10, 0, tc.readable)
			if err != nil {
				t.Fatal(err)
			}

			if total != tc.want || len(got) != tc.want {
				t.Fatalf("total %d rows %d, want %d", total, len(got), tc.want)
			}

			for _, r := range got {
				if r.Name != "widget" || r.Version != "1.0.0" || r.Format != "npm" || r.Checksum != tc.digest ||
					r.Algorithm != tc.algorithm {
					t.Fatalf("result %+v", r)
				}

				if r.Repository == "ck-hidden" && len(tc.readable) == 1 {
					t.Fatal("a repository outside the readable set leaked")
				}
			}
		})
	}
}
