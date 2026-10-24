// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	fmtmaven "github.com/openartifact/openartifact/internal/format/maven"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

type MavenMetadataWorker struct {
	db        database.Database
	blobStore storage.BlobStore
	log       *logger.Logger
	sqlDB     *sql.DB
	q         *regenQueue[mavenKey]
}

// mavenKey identifies one group:artifact within a repository.
type mavenKey struct {
	repoID, groupID, artifactID string
}

func (k mavenKey) String() string { return k.repoID + ":" + k.groupID + ":" + k.artifactID }

func NewMavenMetadataWorker(db database.Database, blobStore storage.BlobStore, sqlDB *sql.DB, log *logger.Logger, reg *metrics.Registry) *MavenMetadataWorker {
	w := &MavenMetadataWorker{db: db, blobStore: blobStore, log: log, sqlDB: sqlDB}
	w.q = newRegenQueue("maven", log, func(ctx context.Context, k mavenKey) error {
		return w.regenerate(ctx, k.repoID, k.groupID, k.artifactID)
	})
	w.q.setMetrics(reg)

	return w
}

func (w *MavenMetadataWorker) WithDebounce(d time.Duration) *MavenMetadataWorker {
	w.q.setDebounce(d)
	return w
}

// WithDrainTimeout sets how long Stop waits for pending regenerations (default
// 10s); `openartifact rebuild` raises it to wait for a whole rebuilt catalogue.
func (w *MavenMetadataWorker) WithDrainTimeout(d time.Duration) *MavenMetadataWorker {
	w.q.setDrainTimeout(d)
	return w
}

// WithRegenLimiter shares a concurrency limiter with the other metadata workers.
func (w *MavenMetadataWorker) WithRegenLimiter(l *RegenLimiter) *MavenMetadataWorker {
	w.q.setLimiter(l)
	return w
}

// NotifyStale schedules a coalesced regeneration of the artifact's maven-metadata.xml.
func (w *MavenMetadataWorker) NotifyStale(repoID, groupID, artifactID string) {
	w.q.Notify(mavenKey{repoID: repoID, groupID: groupID, artifactID: artifactID})
}

// Stop drains pending regenerations and waits for running ones (bounded).
func (w *MavenMetadataWorker) Stop() {
	w.q.Stop()
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (w *MavenMetadataWorker) regenerate(ctx context.Context, repoID, groupID, artifactID string) error {
	if w.sqlDB != nil {
		release, err := lockForRegeneration(ctx, w.sqlDB, "maven:"+mavenKey{repoID, groupID, artifactID}.String())
		if err != nil {
			return fmt.Errorf("maven worker: %w", err)
		}

		defer release()
	}

	refs, err := w.db.ListMavenGARefs(ctx, repoID, groupID, artifactID)
	if err != nil {
		return fmt.Errorf("maven worker: querying artifacts: %w", err)
	}

	groupPath := strings.ReplaceAll(groupID, ".", "/")
	dir := groupPath + "/" + artifactID + "/"

	// Paths of the metadata files this run writes; everything else below dir
	// is pruned so it never advertises versions that no longer have a live file.
	keep := make([]string, 0, 4)

	if len(refs.Versions) == 0 {
		return w.pruneMetadata(ctx, repoID, dir, keep)
	}

	// The query returns distinct versions already, in string order, so the
	// stable Maven-order sort below is deterministic across runs.
	versions := refs.Versions
	pkgmaven.SortVersions(versions)

	latestVersion := versions[len(versions)-1]
	latestRelease := ""

	for _, v := range slices.Backward(versions) {
		if !pkgmaven.IsSnapshot(v) {
			latestRelease = v
			break
		}
	}

	groupMetaPath := dir + "maven-metadata.xml"
	keep = append(keep, groupMetaPath)

	if err := w.storeMetadata(ctx, repoID, groupMetaPath, "group-metadata", func(updated time.Time) ([]byte, error) {
		return fmtmaven.GenerateGroupMetadataXML(groupID, artifactID, versions, latestRelease, latestVersion, updated)
	}); err != nil {
		return fmt.Errorf("maven worker: group metadata: %w", err)
	}

	snapshotVersions := make(map[string][]models.MavenSnapshotRef)

	for _, a := range refs.Snapshots {
		snapshotVersions[a.Version] = append(snapshotVersions[a.Version], a)
	}

	for snapVersion, snapArts := range snapshotVersions {
		latestTs := ""

		latestBn := 0
		for _, a := range snapArts {
			if a.Timestamp > latestTs || (a.Timestamp == latestTs && a.BuildNumber > latestBn) {
				latestTs = a.Timestamp
				latestBn = a.BuildNumber
			}
		}

		// Keep only the newest build per (extension, classifier).
		type snapKey struct{ ext, classifier string }

		newest := make(map[snapKey]models.MavenSnapshotRef)

		for _, a := range snapArts {
			k := snapKey{a.Extension, a.Classifier}
			if cur, ok := newest[k]; !ok || a.Timestamp > cur.Timestamp ||
				(a.Timestamp == cur.Timestamp && a.BuildNumber > cur.BuildNumber) {
				newest[k] = a
			}
		}

		entries := make([]fmtmaven.SnapshotEntry, 0, len(newest))

		baseVersion := strings.TrimSuffix(snapVersion, "-SNAPSHOT")
		for _, a := range newest {
			value := baseVersion + "-" + a.Timestamp + "-" + strconv.Itoa(a.BuildNumber)
			entries = append(entries, fmtmaven.SnapshotEntry{
				Extension:  a.Extension,
				Classifier: a.Classifier,
				Value:      value,
				Updated:    strings.ReplaceAll(a.Timestamp, ".", ""),
			})
		}

		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Extension != entries[j].Extension {
				return entries[i].Extension < entries[j].Extension
			}

			return entries[i].Classifier < entries[j].Classifier
		})

		coord := pkgmaven.Coordinate{
			GroupID:    groupID,
			ArtifactID: artifactID,
			Version:    snapVersion,
		}

		snapMetaPath := dir + snapVersion + "/maven-metadata.xml"
		keep = append(keep, snapMetaPath)

		if err := w.storeMetadata(ctx, repoID, snapMetaPath, "snapshot-metadata", func(updated time.Time) ([]byte, error) {
			return fmtmaven.GenerateSnapshotMetadataXML(coord, latestTs, latestBn, updated, entries)
		}); err != nil {
			return fmt.Errorf("maven worker: snapshot metadata for %s: %w", snapVersion, err)
		}
	}

	return w.pruneMetadata(ctx, repoID, dir, keep)
}

// storeMetadata renders one maven-metadata.xml and points its
// repository_metadata row at it, doing nothing when the content is unchanged.
//
// lastUpdated is part of the document, so rendering with the current time
// would change the hash on every run and rewrite (and re-reclaim) every
// metadata file of the artifact each time any one of them changed. The
// document is therefore first rendered with the previous generation time: when
// that reproduces the stored hash nothing changed and nothing is written;
// otherwise it is rendered again with the current time, which is what clients
// compare to notice a change.
//
// Put and upsert run under the blob's lock so a concurrent reclaim of
// identical content (another repository, or a previous generation) cannot
// delete the object between them. The blob the row pointed to before is
// reclaimed afterwards, since nothing else would ever free the old object.
func (w *MavenMetadataWorker) storeMetadata(ctx context.Context, repoID, path, pathType string, render func(lastUpdated time.Time) ([]byte, error)) error {
	prev, err := w.db.GetRepoMetadataByPath(ctx, repoID, path)
	if err != nil {
		return fmt.Errorf("looking up current metadata: %w", err)
	}

	var (
		data      []byte
		sha       string
		generated time.Time
	)

	for _, at := range []time.Time{prevGenerated(prev), time.Now().UTC()} {
		if at.IsZero() {
			continue
		}

		generated = at

		data, err = render(at)
		if err != nil {
			return fmt.Errorf("generating xml: %w", err)
		}

		sha, err = computeChecksumsForSHA256(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("computing checksums: %w", err)
		}

		if prev != nil && prev.BlobSHA256 == sha {
			return nil
		}
	}

	blob := storage.BytesBlob(sha, data, map[string]string{
		"sha256":    sha,
		"format":    "maven",
		"repo_id":   repoID,
		"path_type": pathType,
	})

	err = storage.CommitBlob(ctx, w.db, w.blobStore, blob, func(ctx context.Context, key string) error {
		if err := w.db.UpsertRepoMetadata(ctx, &models.RepoMetadata{
			RepositoryID:  repoID,
			Path:          path,
			BlobSHA256:    key,
			BlobSize:      int64(len(data)),
			LastGenerated: generated,
		}); err != nil {
			return fmt.Errorf("upserting row: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	if prev != nil && prev.BlobSHA256 != sha {
		w.reclaim(ctx, prev.BlobSHA256)
	}

	return nil
}

// reclaim frees a superseded metadata blob; failures are logged, the next
// regeneration of another path cannot retry it, but the object is only space.
func (w *MavenMetadataWorker) reclaim(ctx context.Context, sha string) {
	if err := storage.ReclaimMetadataBlob(ctx, w.db, w.blobStore, sha); err != nil {
		w.log.Warnw("maven worker: failed to reclaim superseded metadata blob", "sha256", sha, "error", err)
	}
}

// pruneMetadata removes generated metadata rows under dir except keep, when
// the database can (the PostgreSQL implementation does).
func (w *MavenMetadataWorker) pruneMetadata(ctx context.Context, repoID, dir string, keep []string) error {
	pruner, ok := w.db.(interface {
		PruneMavenMetadata(ctx context.Context, repositoryID, dir string, keep []string) ([]string, error)
	})
	if !ok {
		return nil
	}

	pruned, err := pruner.PruneMavenMetadata(ctx, repoID, dir, keep)
	if err != nil {
		return fmt.Errorf("maven worker: %w", err)
	}

	for _, sha := range pruned {
		w.reclaim(ctx, sha)
	}

	return nil
}

func prevGenerated(prev *models.RepoMetadata) time.Time {
	if prev == nil {
		return time.Time{}
	}

	return prev.LastGenerated.UTC()
}

// computeChecksumsForSHA256 computes checksums and returns only the SHA256 hex value.
func computeChecksumsForSHA256(r *bytes.Reader) (string, error) {
	md5Hex, sha1Hex, sha256Hex, sha512Hex, err := pkgmaven.ComputeChecksums(r)
	_ = md5Hex
	_ = sha1Hex
	_ = sha512Hex

	return sha256Hex, err
}
