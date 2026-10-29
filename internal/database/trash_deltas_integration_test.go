// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// A delta whose source package is in the trash must not be listed (it would
// expose the trashed file's sha256 and name); restoring the source brings it
// back.
func TestListPackageDeltasHidesTrashedSources(t *testing.T) {
	for _, tc := range []struct {
		format string
		seed   func(s *quotaSeeder, blobID string)
		kind   models.TrashKind
	}{
		{"rpm", (*quotaSeeder).rpm, models.TrashKindRPM},
		{"deb", (*quotaSeeder).deb, models.TrashKindDeb},
	} {
		t.Run(tc.format, func(t *testing.T) {
			registerFixtures(t)

			pg := newQuotaTestDB(t, 8)
			pg.SetTrashRetentionDays(7)

			ctx := context.Background()
			s := newQuotaSeeder(t, pg, "trash-deltas-"+tc.format, tc.format)

			fromLive, fromTrashed, to := s.blob(10), s.blob(20), s.blob(30)
			tc.seed(s, fromLive)
			tc.seed(s, fromTrashed)
			tc.seed(s, to)

			sha := func(blobID string) string {
				return s.queryID(`SELECT sha256 FROM blobs WHERE id = $1`, blobID)
			}

			liveSHA, trashedSHA, toSHA := sha(fromLive), sha(fromTrashed), sha(to)

			for _, d := range []struct{ from, name string }{{liveSHA, "live.pkg"}, {trashedSHA, "trashed.pkg"}} {
				if err := pg.CreatePackageDelta(ctx, &models.PackageDelta{
					RepositoryID: s.repo, FromSHA256: d.from, FromFilename: d.name, ToSHA256: toSHA, ToFilename: "to.pkg",
					DeltaSHA256: sha(s.blob(1)), DeltaSize: 1, Format: tc.format,
				}); err != nil {
					t.Fatalf("CreatePackageDelta(%s): %v", d.name, err)
				}
			}

			sources := func() []string {
				t.Helper()

				deltas, err := pg.ListPackageDeltas(ctx, s.repo, toSHA)
				if err != nil {
					t.Fatalf("ListPackageDeltas: %v", err)
				}

				names := make([]string, 0, len(deltas))
				for _, d := range deltas {
					names = append(names, d.FromFilename)
				}

				slices.Sort(names)

				return names
			}

			if got, want := sources(), []string{"live.pkg", "trashed.pkg"}; !slices.Equal(got, want) {
				t.Fatalf("sources before the delete = %v, want %v", got, want)
			}

			id := s.queryID(`SELECT id FROM package_files WHERE blob_id = $1`, fromTrashed)

			if soft, err := pg.TrashRows(ctx, s.repo, tc.kind, []string{id}, "alice"); err != nil || !soft {
				t.Fatalf("TrashRows = %v, %v; want soft delete", soft, err)
			}

			if got, want := sources(), []string{"live.pkg"}; !slices.Equal(got, want) {
				t.Fatalf("sources with the source in the trash = %v, want %v", got, want)
			}

			if _, err := pg.RestoreTrash(ctx, tc.kind, id); err != nil {
				t.Fatalf("RestoreTrash: %v", err)
			}

			if got, want := sources(), []string{"live.pkg", "trashed.pkg"}; !slices.Equal(got, want) {
				t.Fatalf("sources after the restore = %v, want %v", got, want)
			}
		})
	}
}
