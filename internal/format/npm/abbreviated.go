// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"github.com/openartifact/openartifact/internal/database/models"
)

//nolint:gochecknoglobals // immutable lookup table
var abbreviatedFields = map[string]bool{
	"name":                 true,
	"version":              true,
	"dependencies":         true,
	"optionalDependencies": true,
	"devDependencies":      true,
	"bundleDependencies":   true,
	"peerDependencies":     true,
	"peerDependenciesMeta": true,
	"bin":                  true,
	"directories":          true,
	"dist":                 true,
	"engines":              true,
	"_hasShrinkwrap":       true,
	"deprecated":           true,
}

// isAbbreviatedField reports whether a raw manifest key survives in the
// install-v1 (abbreviated) form.
func isAbbreviatedField(raw []byte) bool {
	if hasEscape(raw) {
		return abbreviatedFields[decodeKey(raw)]
	}

	return abbreviatedFields[string(raw)]
}

// BuildAbbreviatedPackument renders the application/vnd.npm.install-v1+json
// packument: {"_id","dist-tags","name","versions"} with each version reduced to
// abbreviatedFields and its dist rewritten as in BuildPackument.
func BuildAbbreviatedPackument(
	repoName, packageName string,
	versions []*models.NPMVersion,
	distTags map[string]string,
	origin string,
) ([]byte, error) {
	sorted := newestPerVersion(versions)

	w := newPackumentWriter(repoName, packageName, origin, estimatePackumentSize(sorted, 320)*2/3)

	w.buf = append(w.buf, `{"_id":`...)
	w.buf = appendJSONString(w.buf, packageName)
	w.buf = append(w.buf, `,"dist-tags":`...)
	w.distTags(distTags)
	w.buf = append(w.buf, `,"name":`...)
	w.buf = appendJSONString(w.buf, packageName)
	w.buf = append(w.buf, `,"versions":{`...)

	for i, v := range sorted {
		if i > 0 {
			w.buf = append(w.buf, ',')
		}

		w.buf = appendJSONString(w.buf, v.Version)
		w.buf = append(w.buf, ':')

		if err := w.version(v, isAbbreviatedField); err != nil {
			return nil, err
		}
	}

	w.buf = append(w.buf, '}', '}')

	return w.buf, nil
}
