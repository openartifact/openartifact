// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"time"
)

// MinifiedMarker is the value of the "minified" key of a p2 document whose
// version lists are stored as differences (Composer's MetadataMinifier).
const MinifiedMarker = "composer/2.0"

// unsetMarker is what a minified version carries for a key the previous
// version had and this one lacks.
const unsetMarker = "__unset"

// Version is one version entry of a p2 document: the composer.json fields of
// the package at that version plus "version", "version_normalized", "time" and
// "dist". Numbers are decoded as json.Number so they survive a round trip.
type Version map[string]any

// Minify is MetadataMinifier::minify. The input is not modified.
func Minify(versions []Version) []Version {
	out := make([]Version, 0, len(versions))

	var last Version

	for _, v := range versions {
		if last == nil {
			last = maps.Clone(v)
			out = append(out, maps.Clone(v))

			continue
		}

		diff := Version{}

		for k, val := range v {
			if prev, ok := last[k]; !ok || prev == nil || !reflect.DeepEqual(prev, val) {
				diff[k] = val
				last[k] = val
			}
		}

		for k := range last {
			if val, ok := v[k]; !ok || val == nil {
				diff[k] = unsetMarker

				delete(last, k)
			}
		}

		out = append(out, diff)
	}

	return out
}

// document is a p2 document on the wire.
type document struct {
	Minified string               `json:"minified,omitempty"`
	Packages map[string][]Version `json:"packages"`
}

// EncodeDocument renders a p2 document; with minify the version lists are
// stored as differences and the document says so.
func EncodeDocument(pkgs map[string][]Version, minify bool) ([]byte, error) {
	doc := document{Packages: pkgs}

	if minify {
		doc.Minified = MinifiedMarker
		doc.Packages = make(map[string][]Version, len(pkgs))

		for name, versions := range pkgs {
			doc.Packages[name] = Minify(versions)
		}
	}

	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode composer metadata: %w", err)
	}

	return b, nil
}

// entryFields are the composer.json keys a version entry of a repository
// carries (what Composer's ArrayDumper writes for a package): the ones that
// resolve and install it. Scripts, repositories, config and the like belong to
// root packages and are dropped.
var entryFields = [...]string{ //nolint:gochecknoglobals // read-only key list
	"description", "keywords", "homepage", "license", "authors", "type",
	"require", "require-dev", "conflict", "replace", "provide", "suggest",
	"autoload", "autoload-dev", "bin", "extra", "support", "funding",
	"abandoned", "include-path", "target-dir", "php-ext", "default-branch",
}

// Dist says where a version's archive is and how to check it.
type Dist struct {
	// URL is the absolute download URL of the zip.
	URL string
	// SHA1 is the hex SHA-1 of the zip, Composer's "shasum".
	SHA1 string
	// Reference identifies the archive content ("reference" in Composer's
	// lock file and download cache).
	Reference string
}

// Release is the repository's knowledge about one version of a package.
type Release struct {
	// Version is the version as published, Normalized its VersionParser form.
	Version, Normalized string
	// Time is when the version was published.
	Time time.Time
	Dist Dist
}

// timeLayout is the date format Packagist writes in "time".
const timeLayout = "2006-01-02T15:04:05-07:00"

// NewVersion builds the p2 entry of a release from the package's composer.json.
func NewVersion(name string, composerJSON []byte, rel Release) (Version, error) {
	var src map[string]any

	dec := json.NewDecoder(bytes.NewReader(composerJSON))
	dec.UseNumber()

	if err := dec.Decode(&src); err != nil {
		return nil, fmt.Errorf("decode composer.json of %s: %w", name, err)
	}

	v := Version{"name": name}

	for _, k := range entryFields {
		if val, ok := src[k]; ok && val != nil {
			v[k] = val
		}
	}

	v["version"] = rel.Version
	v["version_normalized"] = rel.Normalized
	v["time"] = rel.Time.UTC().Format(timeLayout)
	v["dist"] = map[string]any{"type": "zip", "url": rel.Dist.URL, "reference": rel.Dist.Reference, "shasum": rel.Dist.SHA1}

	return v, nil
}
