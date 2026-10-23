// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

// maxParentRescan bounds how many unresolved poms one parent upload re-evaluates.
const maxParentRescan = 500

// repoPOMLookup returns a licensemeta.POMLookup that reads a parent pom from the
// stored artifacts of one repository. It never reaches outside the repository:
// a parent that is not stored there is "not available", and the child's
// license stays unresolved until the parent is uploaded.
func repoPOMLookup(db database.Database, blobs storage.BlobStore, repoID string) licensemeta.POMLookup {
	return func(ctx context.Context, groupID, artifactID, version string) (*pkgmaven.POM, error) {
		if blobs == nil {
			return nil, nil
		}

		art, err := db.GetMavenArtifactByPath(ctx, repoID, pkgmaven.RepositoryPath(pkgmaven.Coordinate{
			GroupID: groupID, ArtifactID: artifactID, Version: version, Extension: "pom",
		}))
		if err != nil {
			return nil, fmt.Errorf("get parent pom: %w", err)
		}

		if art == nil && pkgmaven.IsSnapshot(version) {
			art, err = db.GetLatestSnapshotFile(ctx, repoID, groupID, artifactID, version, "", "pom")
			if err != nil {
				return nil, fmt.Errorf("get parent snapshot pom: %w", err)
			}
		}

		if art == nil || art.SHA256 == "" {
			return nil, nil
		}

		rc, err := blobs.Get(ctx, art.SHA256)
		if err != nil {
			return nil, fmt.Errorf("open parent pom: %w", err)
		}

		defer func() { _ = rc.Close() }()

		pom, perr := pkgmaven.ParsePOM(rc)
		if perr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("read parent pom: %w", ctxErr)
			}

			return nil, nil
		}

		return pom, nil
	}
}

// inheritedPOMLicenses resolves the licenses of a pom that declares none from
// its parent chain in the repository. resolved is false when a parent is not
// stored (yet).
func inheritedPOMLicenses(ctx context.Context, db database.Database, blobs storage.BlobStore, repoID string, parent *pkgmaven.Parent) ([]string, bool, error) {
	return licensemeta.InheritPOMLicenses(ctx, parent, repoPOMLookup(db, blobs, repoID)) //nolint:wrapcheck // callers only log it
}

// recordPOMLicenses stores the license scan result of an uploaded pom that
// declares no license of its own: the inherited licenses when the parent chain
// resolves, an empty (unresolved) result otherwise, which a later parent upload
// re-evaluates (reevaluateUnresolvedPOMs).
func recordPOMLicenses(ctx context.Context, db database.Database, log *logger.Logger, repoID, artifactID string, licenses []string) error {
	_, _, err := licensepolicy.Record(ctx, db, log, repoID, artifactID, licenses, models.LicenseSourceArtifact)
	if err != nil {
		return fmt.Errorf("record pom licenses: %w", err)
	}

	return nil
}

// reevaluateUnresolvedPOMs re-resolves the poms of a repository whose license
// scan found no license, after a pom that may be their parent was stored. A pom
// whose parent chain now resolves gets its scan result rewritten with the
// inherited licenses (quarantining it, and the files stored beside it, when a
// blocking policy is violated). It returns how many results it updated.
func reevaluateUnresolvedPOMs(ctx context.Context, db database.Database, blobs storage.BlobStore, log *logger.Logger, repoID string) (int, error) {
	scans, err := db.ListLicenseScanResults(ctx, repoID)
	if err != nil {
		return 0, fmt.Errorf("list license scans: %w", err)
	}

	src := artifactLicenseSource{db: db, blobs: blobs, log: log}

	var (
		byID    map[string]*models.MavenArtifact
		updated int
		seen    int
	)

	for _, scan := range scans {
		if scan == nil || len(scan.Licenses) > 0 || scan.ScanStatus != "completed" {
			continue
		}

		if seen++; seen > maxParentRescan {
			break
		}

		blob, err := db.GetRepoComponentBlob(ctx, repoID, scan.ArtifactID)
		if err != nil {
			return updated, fmt.Errorf("resolve artifact: %w", err)
		}

		if blob == nil || blob.Format != "maven" || !strings.HasSuffix(strings.ToLower(blob.Filename), ".pom") {
			continue
		}

		licenses, err := src.fromFile(ctx, repoID, scan.ArtifactID)
		if err != nil {
			log.Warnw("maven parent license: re-evaluation skipped", "artifact_id", scan.ArtifactID, "error", err)
			continue
		}

		if len(licenses) == 0 {
			continue
		}

		result, violations, err := licensepolicy.Record(ctx, db, log, repoID, scan.ArtifactID, licenses, models.LicenseSourceArtifact)
		if err != nil {
			return updated, fmt.Errorf("record license scan: %w", err)
		}

		updated++

		if len(licensepolicy.Blocked(violations)) == 0 || !result.Quarantined {
			continue
		}

		if byID == nil {
			byID, err = mavenArtifactsByID(ctx, db, repoID)
			if err != nil {
				return updated, err
			}
		}

		if art := byID[scan.ArtifactID]; art != nil {
			quarantineSiblings(ctx, db, log, repoID, art.GroupID, art.ArtifactCoord, art.Version, art.RepoPath, licenses)
		}
	}

	return updated, nil
}

func mavenArtifactsByID(ctx context.Context, db database.Database, repoID string) (map[string]*models.MavenArtifact, error) {
	arts, err := db.ListMavenArtifactsByRepo(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("list maven artifacts: %w", err)
	}

	out := make(map[string]*models.MavenArtifact, len(arts))
	for _, a := range arts {
		out[a.ID] = a
	}

	return out, nil
}

// quarantineSiblings records the (blocked) licenses against the other files
// stored for the same group, artifact and version, best-effort, so the blocked
// version cannot be downloaded through them.
func quarantineSiblings(ctx context.Context, db database.Database, log *logger.Logger, repoID, groupID, artifactID, version, skipPath string, licenses []string) {
	siblings, err := db.GetMavenArtifactsByGAV(ctx, repoID, groupID, artifactID, version)
	if err != nil {
		log.Warnw("maven license block: failed to list sibling files", "path", skipPath, "error", err)
		return
	}

	for _, s := range siblings {
		if s.RepoPath == skipPath {
			continue
		}

		if _, _, recErr := licensepolicy.Record(ctx, db, log, repoID, s.RepoPath, licenses, models.LicenseSourceArtifact); recErr != nil {
			log.Warnw("maven license block: failed to quarantine sibling", "path", s.RepoPath, "error", recErr)
		}
	}
}
