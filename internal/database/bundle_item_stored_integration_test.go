// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package database

import (
	"context"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
)

// TestBundleItemStoredAcceptsWhatTheClientsSend writes Ivy and sbt files through
// their stores, reads them back through the package listing the way the CLI
// (`oa bundle add --from-build`) and the web item picker do, and checks the item
// verification accepts every spelling of their path the clients send: the listed
// file name, the repository URL path (`oa bundle add --path`, a multi-segment
// group is written with slashes there and stored with dots), the stored path and
// either behind a leading slash. Another digest, another repository, another
// format and another file never verify.
func TestBundleItemStoredAcceptsWhatTheClientsSend(t *testing.T) {
	pg := newQuotaTestDB(t, 10)
	ctx := context.Background()

	ivy := newQuotaSeeder(t, pg, "items-ivy", "ivy")
	sbt := newQuotaSeeder(t, pg, "items-sbt", "sbt")

	ivyBlob, sbtBlob := sha256Of("ivy jar"), sha256Of("sbt jar")

	if err := pg.CreateIvyArtifact(ctx, &models.IvyArtifact{
		RepositoryID: ivy.repo, Organisation: "com.example", Module: "lib", Revision: "1.0", ArtifactName: "lib-1.0.jar",
		ArtifactType: "jar", ArtifactExt: "jar", SHA256: ivyBlob, BlobKey: ivyBlob, SizeBytes: 7,
	}); err != nil {
		t.Fatalf("CreateIvyArtifact: %v", err)
	}

	if err := pg.CreateSBTArtifact(ctx, &models.SBTArtifact{
		RepositoryID: sbt.repo, GroupID: "org.scala", ArtifactID: "lib_2.13", Version: "1.0", Filename: "lib_2.13-1.0.jar",
		SHA256: sbtBlob, BlobKey: sbtBlob, SizeBytes: 7,
	}); err != nil {
		t.Fatalf("CreateSBTArtifact: %v", err)
	}

	tests := []struct {
		name   string
		format string
		repo   *quotaSeeder
		other  *quotaSeeder // a repository of the other format
		sha    string
		// stored is the stored path, native the repository URL path.
		stored, native string
	}{
		{"ivy", "ivy", ivy, sbt, ivyBlob, "com.example/lib/1.0/lib-1.0.jar", "com/example/lib/1.0/lib-1.0.jar"},
		{"sbt", "sbt", sbt, ivy, sbtBlob, "org.scala/lib_2.13/1.0/lib_2.13-1.0.jar", "org/scala/lib_2.13/1.0/lib_2.13-1.0.jar"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			listed, _, err := pg.SearchComponents(ctx, tc.repo.repo, ComponentListOptions{Page: 1, Limit: 10})
			if err != nil || len(listed) != 1 || listed[0].SHA256 != tc.sha {
				t.Fatalf("listing = %+v, %v; want the one file", listed, err)
			}

			repoName := "items-" + tc.name
			accepted := []string{
				listed[0].Filename,
				tc.stored,
				"/" + tc.stored,
				tc.native,
				"/" + tc.native,
				"repos/" + repoName + "/" + tc.native,
			}

			for _, path := range accepted {
				got, err := pg.BundleItemStored(ctx, tc.repo.repo, repoName, tc.format, tc.sha, path)
				if err != nil || !got {
					t.Errorf("%q: stored = %v, %v; want it accepted", path, got, err)
				}
			}

			rejected := []struct{ what, repo, format, sha, path string }{
				{"wrong digest", tc.repo.repo, tc.format, strings.Repeat("0", 64), tc.native},
				{"another repository", tc.other.repo, tc.format, tc.sha, tc.native},
				{"another format", tc.repo.repo, "docker", tc.sha, tc.native},
				{"another file", tc.repo.repo, tc.format, tc.sha, strings.TrimSuffix(tc.native, ".jar") + "-sources.jar"},
				{"the URL of another repository", tc.repo.repo, tc.format, tc.sha, "repos/elsewhere/" + tc.native},
			}

			for _, r := range rejected {
				if got, err := pg.BundleItemStored(ctx, r.repo, repoName, r.format, r.sha, r.path); err != nil || got {
					t.Errorf("%s: stored = %v, %v; want it rejected", r.what, got, err)
				}
			}
		})
	}
}
