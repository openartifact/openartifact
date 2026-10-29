// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package worker

import (
	"context"
	"fmt"
	"testing"

	"github.com/openartifact/openartifact/internal/format/storage"
)

// mvnRow describes one maven file: artifact, version, file extension, age in
// days and, for a snapshot build, its timestamp and build number.
type mvnRow struct {
	artifact, version, ext string
	ageDays                int
	snapTS                 string
	snapBuild              int
}

// registerMavenFixture registers the maven storage descriptor the way the maven
// plugin does, for the generic trash and cleanup engines under test.
func registerMavenFixture(t *testing.T) {
	t.Helper()

	storage.Reset()
	t.Cleanup(storage.Reset)

	storage.Register("maven", storage.Storage{
		Trash: storage.Trash{
			Kind: "maven_artifact", NameExpr: "f.path",
			RegenKeys: "p.attrs->>'group_id', p.attrs->>'artifact_id'",
		},
		Cleanup: storage.Cleanup{
			Partition: []string{"p.id", "f.part"},
			ProtectExpr: `COALESCE((v.metadata->>'is_snapshot')::boolean, false)
				AND DENSE_RANK() OVER (PARTITION BY p.id, v.version
				    ORDER BY v.metadata->>'snapshot_timestamp' DESC NULLS LAST, (v.metadata->>'snapshot_build_number')::int DESC NULLS LAST) = 1`,
		},
	})
}

// mavenFile writes one maven file (package, version and file rows, the way the
// store leaves them) pointing at blobID, and returns the file id.
func (s *purgeSeed) mavenFile(repoID, blobID string, r mvnRow) string {
	s.t.Helper()

	ctx := context.Background()
	db := s.pg.GetDB()
	name := "g:" + r.artifact
	versionKey, part := r.version, "false"
	meta := `{"is_snapshot": false}`
	path := fmt.Sprintf("g/%s/%s/%s-%s.%s", r.artifact, r.version, r.artifact, r.version, r.ext)

	if r.snapTS != "" {
		versionKey = fmt.Sprintf("%s#%s-%d", r.version, r.snapTS, r.snapBuild)
		part = "true:" + r.version
		meta = fmt.Sprintf(`{"is_snapshot": true, "snapshot_timestamp": %q, "snapshot_build_number": %d}`, r.snapTS, r.snapBuild)
		path = fmt.Sprintf("g/%s/%s/%s-%s-%d.%s", r.artifact, r.version, r.artifact, r.snapTS, r.snapBuild, r.ext)
	}

	var pkgID, versionID, id string

	if err := db.QueryRowContext(ctx,
		`INSERT INTO packages (repository_id, format, name, name_key, attrs)
		 VALUES ($1, 'maven', $2, $2, jsonb_build_object('group_id', 'g', 'artifact_id', $3::text))
		 ON CONFLICT (repository_id, name_key) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
		repoID, name, r.artifact).Scan(&pkgID); err != nil {
		s.t.Fatalf("insert package %+v: %v", r, err)
	}

	if err := db.QueryRowContext(ctx,
		`INSERT INTO package_versions (package_id, repository_id, version, version_key, metadata, created_at)
		 VALUES ($1, $2, $3, $4, $5::jsonb, now() - make_interval(days => $6))
		 ON CONFLICT (package_id, version_key) WHERE deleted_at IS NULL DO UPDATE SET version = EXCLUDED.version RETURNING id`,
		pkgID, repoID, r.version, versionKey, meta, r.ageDays).Scan(&versionID); err != nil {
		s.t.Fatalf("insert version %+v: %v", r, err)
	}

	if err := db.QueryRowContext(ctx,
		`INSERT INTO package_files (version_id, repository_id, path, filename, blob_id, part, created_at, updated_at)
		 VALUES ($1, $2, $3, $3, $4, $5, now() - make_interval(days => $6), now() - make_interval(days => $6)) RETURNING id`,
		versionID, repoID, path, blobID, part, r.ageDays).Scan(&id); err != nil {
		s.t.Fatalf("insert file %+v: %v", r, err)
	}

	return id
}
