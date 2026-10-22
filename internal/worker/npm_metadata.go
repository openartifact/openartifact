// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/pkg/logger"
)

type NPMMetadataWorker struct {
	db    database.Database
	log   *logger.Logger
	sqlDB *sql.DB
	q     *regenQueue[string]
}

func NewNPMMetadataWorker(db database.Database, log *logger.Logger, sqlDB *sql.DB, reg *metrics.Registry) *NPMMetadataWorker {
	w := &NPMMetadataWorker{db: db, log: log, sqlDB: sqlDB}
	w.q = newRegenQueue("npm", log, w.regenerate)
	w.q.setMetrics(reg)

	return w
}

func (w *NPMMetadataWorker) WithDebounce(d time.Duration) *NPMMetadataWorker {
	w.q.setDebounce(d)
	return w
}

// WithRegenLimiter shares a concurrency limiter with the other metadata workers.
func (w *NPMMetadataWorker) WithRegenLimiter(l *RegenLimiter) *NPMMetadataWorker {
	w.q.setLimiter(l)
	return w
}

// NotifyStale schedules a coalesced regeneration of the package's denormalized
// metadata.
func (w *NPMMetadataWorker) NotifyStale(packageID string) {
	w.q.Notify(packageID)
}

// Stop drains pending regenerations and waits for running ones (bounded).
func (w *NPMMetadataWorker) Stop() {
	w.q.Stop()
}

func (w *NPMMetadataWorker) regenerate(ctx context.Context, packageID string) error {
	if w.sqlDB != nil {
		release, err := lockForRegeneration(ctx, w.sqlDB, "npm:"+packageID)
		if err != nil {
			return fmt.Errorf("npm worker: %w", err)
		}
		defer release()
	}

	// GetNPMVersionsByPackage returns live versions only: trashed versions
	// never count towards latest, and a restored one counts again on the next
	// run (the trash API notifies after a restore).
	versions, err := w.db.GetNPMVersionsByPackage(ctx, packageID)
	if err != nil {
		return fmt.Errorf("npm worker: getting versions of %s: %w", packageID, err)
	}

	// With no live version left (everything unpublished or trashed) latest is
	// cleared rather than left pointing at a hidden version.
	latest := computeLatestVersion(versions)

	if err := w.db.UpdateNPMPackageLatestVersion(ctx, packageID, latest); err != nil {
		return fmt.Errorf("npm worker: updating latest version of %s: %w", packageID, err)
	}

	w.log.Infow("npm packument denormalization updated", "package_id", packageID, "latest_version", latest)

	return nil
}

// computeLatestVersion picks the "latest" dist-tag target. Non-deprecated
// stable releases win over non-deprecated prereleases, which win over
// deprecated versions; within a tier the highest semver precedence wins. A
// prerelease therefore never becomes latest while a stable release exists.
func computeLatestVersion(versions []*models.NPMVersion) string {
	var (
		best     string
		bestRank = -1
	)

	for _, v := range versions {
		rank := 0
		if !isDeprecated(v.ManifestJSON) {
			rank = 2
		}

		if !isPrerelease(v.Version) {
			rank++
		}

		if rank > bestRank || (rank == bestRank && compareSemver(v.Version, best) > 0) {
			best, bestRank = v.Version, rank
		}
	}

	return best
}

func isDeprecated(manifestJSON []byte) bool {
	if len(manifestJSON) == 0 {
		return false
	}

	var m map[string]any
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return false
	}

	dep, _ := m["deprecated"].(string)

	return dep != ""
}

func isPrerelease(v string) bool {
	v, _, _ = strings.Cut(v, "+")
	_, pre, _ := strings.Cut(v, "-")

	return pre != ""
}

// compareSemver implements semver 2.0.0 precedence (build metadata ignored).
func compareSemver(a, b string) int {
	aNums, aPre := semverParts(a)
	bNums, bPre := semverParts(b)

	for i := range 3 {
		if c := cmp.Compare(aNums[i], bNums[i]); c != 0 {
			return c
		}
	}

	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	}

	ai, bi := strings.Split(aPre, "."), strings.Split(bPre, ".")
	for i := 0; i < len(ai) && i < len(bi); i++ {
		if c := comparePreIdent(ai[i], bi[i]); c != 0 {
			return c
		}
	}

	return cmp.Compare(len(ai), len(bi))
}

func comparePreIdent(a, b string) int {
	an, aErr := strconv.ParseUint(a, 10, 64)
	bn, bErr := strconv.ParseUint(b, 10, 64)

	switch {
	case aErr == nil && bErr == nil:
		return cmp.Compare(an, bn)
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	}

	return strings.Compare(a, b)
}

func semverParts(v string) ([3]uint64, string) {
	v, _, _ = strings.Cut(strings.TrimPrefix(strings.TrimSpace(v), "v"), "+")
	v, pre, _ := strings.Cut(v, "-")
	parts := strings.SplitN(v, ".", 3)

	var nums [3]uint64

	for i := 0; i < 3 && i < len(parts); i++ {
		nums[i], _ = strconv.ParseUint(parts[i], 10, 64)
	}

	return nums, pre
}
