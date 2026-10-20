// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/metrics"
	"github.com/openartifact/openartifact/pkg/logger"
)

type NPMMetadataWorker struct {
	db       database.Database
	log      *logger.Logger
	sqlDB    *sql.DB
	reg      *metrics.Registry
	mu       sync.Mutex
	timers   map[string]*time.Timer
	debounce time.Duration
}

func NewNPMMetadataWorker(db database.Database, log *logger.Logger, sqlDB *sql.DB, reg *metrics.Registry) *NPMMetadataWorker {
	return &NPMMetadataWorker{
		db:       db,
		log:      log,
		sqlDB:    sqlDB,
		reg:      reg,
		timers:   make(map[string]*time.Timer),
		debounce: 500 * time.Millisecond,
	}
}

func (w *NPMMetadataWorker) WithDebounce(d time.Duration) *NPMMetadataWorker {
	w.debounce = d
	return w
}

func (w *NPMMetadataWorker) NotifyStale(packageID string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if t, ok := w.timers[packageID]; ok {
		t.Stop()
	}

	var t *time.Timer

	t = time.AfterFunc(w.debounce, func() {
		start := time.Now()

		w.regenerate(packageID)

		if w.reg != nil {
			w.reg.MetadataRegenerationDurationSeconds.WithLabelValues("npm").Observe(time.Since(start).Seconds())
			w.reg.MetadataRegenerationsTotal.WithLabelValues("npm").Inc()
		}

		w.mu.Lock()
		if w.timers[packageID] == t {
			delete(w.timers, packageID)
		}
		w.mu.Unlock()
	})
	w.timers[packageID] = t
}

func (w *NPMMetadataWorker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	for key, t := range w.timers {
		t.Stop()
		delete(w.timers, key)
	}
}

func (w *NPMMetadataWorker) regenerate(packageID string) {
	ctx := context.Background()

	if w.sqlDB != nil {
		release, locked, err := tryAdvisoryLock(ctx, w.sqlDB, "npm:"+packageID)
		if err != nil || !locked {
			w.log.Warnw("npm regenerate skipped: lock unavailable", "package_id", packageID, "error", err)
			return
		}
		defer release()
	}

	versions, err := w.db.GetNPMVersionsByPackage(ctx, packageID)
	if err != nil {
		w.log.Errorw("npm worker: failed to get versions", "package_id", packageID, "error", err)
		return
	}

	if len(versions) == 0 {
		return
	}

	latest := computeLatestVersion(versions)

	if err := w.db.UpdateNPMPackageLatestVersion(ctx, packageID, latest); err != nil {
		w.log.Errorw("npm worker: failed to update latest version", "package_id", packageID, "error", err)
		return
	}

	w.log.Infow("npm packument denormalization updated", "package_id", packageID, "latest_version", latest)
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
