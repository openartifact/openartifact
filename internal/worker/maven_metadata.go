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
	"sync"
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
	reg       *metrics.Registry

	mu       sync.Mutex
	timers   map[string]*time.Timer
	debounce time.Duration
}

func NewMavenMetadataWorker(db database.Database, blobStore storage.BlobStore, sqlDB *sql.DB, log *logger.Logger, reg *metrics.Registry) *MavenMetadataWorker {
	return &MavenMetadataWorker{
		db:        db,
		blobStore: blobStore,
		log:       log,
		sqlDB:     sqlDB,
		reg:       reg,
		timers:    make(map[string]*time.Timer),
		debounce:  500 * time.Millisecond,
	}
}

func (w *MavenMetadataWorker) WithDebounce(d time.Duration) *MavenMetadataWorker {
	w.debounce = d
	return w
}

func (w *MavenMetadataWorker) NotifyStale(repoID, groupID, artifactID string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	key := repoID + ":" + groupID + ":" + artifactID
	if t, ok := w.timers[key]; ok {
		t.Stop()
	}

	var t *time.Timer

	t = time.AfterFunc(w.debounce, func() {
		start := time.Now()

		err := w.regenerate(context.Background(), repoID, groupID, artifactID)
		if w.reg != nil {
			w.reg.MetadataRegenerationDurationSeconds.WithLabelValues("maven").Observe(time.Since(start).Seconds())
			w.reg.MetadataRegenerationsTotal.WithLabelValues("maven").Inc()
		}

		if err != nil {
			w.log.Errorw("maven metadata regeneration failed", "repo_id", repoID, "group_id", groupID, "artifact_id", artifactID, "error", err)
		}

		w.mu.Lock()
		if w.timers[key] == t {
			delete(w.timers, key)
		}
		w.mu.Unlock()
	})
	w.timers[key] = t
}

func (w *MavenMetadataWorker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	for key, t := range w.timers {
		t.Stop()
		delete(w.timers, key)
	}
}

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (w *MavenMetadataWorker) regenerate(ctx context.Context, repoID, groupID, artifactID string) error {
	lockKey := repoID + ":" + groupID + ":" + artifactID

	if w.sqlDB != nil {
		release, locked, err := tryAdvisoryLock(ctx, w.sqlDB, "maven:"+lockKey)
		if err != nil {
			return fmt.Errorf("maven worker: advisory lock query: %w", err)
		}

		if !locked {
			w.log.Infow("maven metadata: advisory lock held by another process, skipping", "key", lockKey)
			return nil
		}

		defer release()
	}

	arts, err := w.db.GetMavenArtifactsByGA(ctx, repoID, groupID, artifactID)
	if err != nil {
		return fmt.Errorf("maven worker: querying artifacts: %w", err)
	}

	if len(arts) == 0 {
		return nil
	}

	versionSet := make(map[string]struct{})
	for _, a := range arts {
		versionSet[a.Version] = struct{}{}
	}

	versions := make([]string, 0, len(versionSet))
	for v := range versionSet {
		versions = append(versions, v)
	}

	sort.Slice(versions, func(i, j int) bool { return pkgmaven.CompareVersions(versions[i], versions[j]) < 0 })

	latestVersion := versions[len(versions)-1]
	latestRelease := ""

	for _, v := range slices.Backward(versions) {
		if !pkgmaven.IsSnapshot(v) {
			latestRelease = v
			break
		}
	}

	groupPath := strings.ReplaceAll(groupID, ".", "/")

	xmlData, err := fmtmaven.GenerateGroupMetadataXML(groupID, artifactID, versions, latestRelease, latestVersion, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("maven worker: generating group metadata xml: %w", err)
	}

	sha256Hex, err := computeChecksumsForSHA256(bytes.NewReader(xmlData))
	if err != nil {
		return fmt.Errorf("maven worker: computing checksums: %w", err)
	}

	blobID, err := w.blobStore.Put(ctx, bytes.NewReader(xmlData), map[string]string{
		"sha256":    sha256Hex,
		"format":    "maven",
		"repo_id":   repoID,
		"path_type": "group-metadata",
	})
	if err != nil {
		return fmt.Errorf("maven worker: storing group metadata blob: %w", err)
	}

	groupMetaPath := groupPath + "/" + artifactID + "/maven-metadata.xml"
	if err := w.db.UpsertRepoMetadata(ctx, &models.RepoMetadata{
		RepositoryID:  repoID,
		Path:          groupMetaPath,
		BlobSHA256:    blobID,
		BlobSize:      int64(len(xmlData)),
		LastGenerated: time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("maven worker: upserting group metadata: %w", err)
	}

	snapshotVersions := make(map[string][]*models.MavenArtifact)

	for _, a := range arts {
		if pkgmaven.IsSnapshot(a.Version) {
			snapshotVersions[a.Version] = append(snapshotVersions[a.Version], a)
		}
	}

	for snapVersion, snapArts := range snapshotVersions {
		latestTs := ""

		latestBn := 0
		for _, a := range snapArts {
			if a.SnapshotTimestamp > latestTs || (a.SnapshotTimestamp == latestTs && a.SnapshotBuildNumber > latestBn) {
				latestTs = a.SnapshotTimestamp
				latestBn = a.SnapshotBuildNumber
			}
		}

		// Keep only the newest build per (extension, classifier).
		type snapKey struct{ ext, classifier string }

		newest := make(map[snapKey]*models.MavenArtifact)

		for _, a := range snapArts {
			k := snapKey{a.Extension, a.Classifier}
			if cur, ok := newest[k]; !ok || a.SnapshotTimestamp > cur.SnapshotTimestamp ||
				(a.SnapshotTimestamp == cur.SnapshotTimestamp && a.SnapshotBuildNumber > cur.SnapshotBuildNumber) {
				newest[k] = a
			}
		}

		entries := make([]fmtmaven.SnapshotEntry, 0, len(newest))

		baseVersion := strings.TrimSuffix(snapVersion, "-SNAPSHOT")
		for _, a := range newest {
			value := baseVersion + "-" + a.SnapshotTimestamp + "-" + strconv.Itoa(a.SnapshotBuildNumber)
			entries = append(entries, fmtmaven.SnapshotEntry{
				Extension:  a.Extension,
				Classifier: a.Classifier,
				Value:      value,
				Updated:    strings.ReplaceAll(a.SnapshotTimestamp, ".", ""),
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

		snapXML, err := fmtmaven.GenerateSnapshotMetadataXML(coord, latestTs, latestBn, time.Now().UTC(), entries)
		if err != nil {
			return fmt.Errorf("maven worker: generating snapshot metadata xml for %s: %w", snapVersion, err)
		}

		_, _, snapSHA256, _, err := pkgmaven.ComputeChecksums(bytes.NewReader(snapXML))
		if err != nil {
			return fmt.Errorf("maven worker: computing snapshot checksums: %w", err)
		}

		snapBlobID, err := w.blobStore.Put(ctx, bytes.NewReader(snapXML), map[string]string{
			"sha256":    snapSHA256,
			"format":    "maven",
			"repo_id":   repoID,
			"path_type": "snapshot-metadata",
		})
		if err != nil {
			return fmt.Errorf("maven worker: storing snapshot metadata blob: %w", err)
		}

		snapMetaPath := groupPath + "/" + artifactID + "/" + snapVersion + "/maven-metadata.xml"
		if err := w.db.UpsertRepoMetadata(ctx, &models.RepoMetadata{
			RepositoryID:  repoID,
			Path:          snapMetaPath,
			BlobSHA256:    snapBlobID,
			BlobSize:      int64(len(snapXML)),
			LastGenerated: time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("maven worker: upserting snapshot metadata for %s: %w", snapVersion, err)
		}
	}

	return nil
}

// computeChecksumsForSHA256 computes checksums and returns only the SHA256 hex value.
func computeChecksumsForSHA256(r *bytes.Reader) (string, error) {
	md5Hex, sha1Hex, sha256Hex, sha512Hex, err := pkgmaven.ComputeChecksums(r)
	_ = md5Hex
	_ = sha1Hex
	_ = sha512Hex

	return sha256Hex, err
}
