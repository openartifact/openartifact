// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
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

	versions, err := w.db.GetNPMVersionsByPackage(ctx, packageID)
	if err != nil {
		return fmt.Errorf("npm worker: getting versions of %s: %w", packageID, err)
	}

	if len(versions) == 0 {
		return nil
	}

	latest := computeLatestVersion(versions)

	if err := w.db.UpdateNPMPackageLatestVersion(ctx, packageID, latest); err != nil {
		return fmt.Errorf("npm worker: updating latest version of %s: %w", packageID, err)
	}

	w.log.Infow("npm packument denormalization updated", "package_id", packageID, "latest_version", latest)

	return nil
}

func computeLatestVersion(versions []*models.NPMVersion) string {
	var best string

	for _, v := range versions {
		if isDeprecated(v.ManifestJSON) {
			continue
		}

		if best == "" || compareSemver(v.Version, best) > 0 {
			best = v.Version
		}
	}

	if best == "" {
		for _, v := range versions {
			if best == "" || compareSemver(v.Version, best) > 0 {
				best = v.Version
			}
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

func compareSemver(a, b string) int {
	aParts := semverNums(a)
	bParts := semverNums(b)

	for i := range 3 {
		av := aParts[i]

		bv := bParts[i]
		if av < bv {
			return -1
		}

		if av > bv {
			return 1
		}
	}

	return 0
}

func semverNums(v string) [3]int {
	v = strings.SplitN(v, "-", 2)[0]
	v = strings.SplitN(v, "+", 2)[0]
	parts := strings.SplitN(v, ".", 3)

	var nums [3]int

	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		nums[i] = n
	}

	return nums
}
