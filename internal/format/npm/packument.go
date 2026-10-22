// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// BuildPackument renders the full packument. Output is the JSON document
// {"_id","dist-tags","name","time","versions"}: every stored manifest is passed
// through with whitespace removed and its dist object rewritten to carry the
// server's tarball URL, integrity and shasum. See packument_json.go.
func BuildPackument(repoName, packageName string, versions []*models.NPMVersion, distTags map[string]string, baseURL string) ([]byte, error) {
	sorted := newestPerVersion(versions)

	w := newPackumentWriter(repoName, packageName, baseURL, estimatePackumentSize(sorted, 320))

	w.buf = append(w.buf, `{"_id":`...)
	w.buf = appendJSONString(w.buf, packageName)
	w.buf = append(w.buf, `,"dist-tags":`...)
	w.distTags(distTags)
	w.buf = append(w.buf, `,"name":`...)
	w.buf = appendJSONString(w.buf, packageName)
	w.buf = append(w.buf, `,"time":`...)
	w.times(sorted, versions)
	w.buf = append(w.buf, `,"versions":{`...)

	for i, v := range sorted {
		if i > 0 {
			w.buf = append(w.buf, ',')
		}

		w.buf = appendJSONString(w.buf, v.Version)
		w.buf = append(w.buf, ':')

		if err := w.version(v, nil); err != nil {
			return nil, err
		}
	}

	w.buf = append(w.buf, '}', '}')

	return w.buf, nil
}

// times appends the "time" object: one entry per version plus created (oldest
// version) and modified (newest), keys in sorted order. versions must be sorted
// by version string without duplicates; all is the unsorted input the
// created/modified bounds are taken over.
func (w *packumentWriter) times(versions, all []*models.NPMVersion) {
	var oldest, newest time.Time

	for _, v := range all {
		if oldest.IsZero() || v.CreatedAt.Before(oldest) {
			oldest = v.CreatedAt
		}

		if newest.IsZero() || v.CreatedAt.After(newest) {
			newest = v.CreatedAt
		}
	}

	w.buf = append(w.buf, '{')
	first := true

	// created and modified sort among the version keys; they also replace a
	// version of the same name, as the map-based encoder did.
	pending := []struct {
		key string
		t   time.Time
		ok  bool
	}{{"created", oldest, !oldest.IsZero()}, {"modified", newest, !newest.IsZero()}}

	emit := func(key string, t time.Time) {
		w.member(&first, key)
		w.buf = appendRFC3339(w.buf, t)
	}

	flushBefore := func(key string) {
		for i := range pending {
			if pending[i].ok && strings.Compare(pending[i].key, key) <= 0 {
				emit(pending[i].key, pending[i].t)
				pending[i].ok = false
			}
		}
	}

	for _, v := range versions {
		flushBefore(v.Version)

		if (v.Version == "created" && !oldest.IsZero()) || (v.Version == "modified" && !newest.IsZero()) {
			continue
		}

		emit(v.Version, v.CreatedAt)
	}

	for i := range pending {
		if pending[i].ok {
			emit(pending[i].key, pending[i].t)
		}
	}

	w.buf = append(w.buf, '}')
}

// BuildVersionManifest renders one version's manifest with the same dist
// rewrite the packument gets: the publisher's dist.tarball/integrity/shasum are
// replaced by the server-computed values, so the single-version endpoint never
// advertises a publisher-chosen tarball URL.
func BuildVersionManifest(repoName, packageName string, v *models.NPMVersion, baseURL string) ([]byte, error) {
	w := newPackumentWriter(repoName, packageName, baseURL, len(v.ManifestJSON)+320)

	if err := w.version(v, nil); err != nil {
		return nil, err
	}

	return w.buf, nil
}
