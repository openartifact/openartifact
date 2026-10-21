// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

// Reference implementation of the packument builders: the map-based encoder
// that the byte-level builders replaced. Equivalence tests compare the parsed
// documents.

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func refBuildPackument(repoName, packageName string, versions []*models.NPMVersion, distTags map[string]string, baseURL string) ([]byte, error) {
	versionMap := make(map[string]any, len(versions))
	timeMap := make(map[string]string, len(versions)+2)

	var oldest, newest time.Time

	for _, v := range versions {
		var manifest map[string]any
		if err := json.Unmarshal(v.ManifestJSON, &manifest); err != nil {
			return nil, fmt.Errorf("unmarshal manifest for %s@%s: %w", packageName, v.Version, err)
		}

		dist, _ := manifest["dist"].(map[string]any)
		if dist == nil {
			dist = make(map[string]any)
		}

		dist["tarball"] = fmt.Sprintf("%s/repos/%s/%s/-/%s", baseURL, repoName, packageName, v.TarballFilename)
		dist["integrity"] = v.Integrity
		dist["shasum"] = v.Shasum
		manifest["dist"] = dist

		versionMap[v.Version] = manifest
		timeMap[v.Version] = v.CreatedAt.UTC().Format(time.RFC3339)

		if oldest.IsZero() || v.CreatedAt.Before(oldest) {
			oldest = v.CreatedAt
		}

		if newest.IsZero() || v.CreatedAt.After(newest) {
			newest = v.CreatedAt
		}
	}

	if !oldest.IsZero() {
		timeMap["created"] = oldest.UTC().Format(time.RFC3339)
	}

	if !newest.IsZero() {
		timeMap["modified"] = newest.UTC().Format(time.RFC3339)
	}

	packument := map[string]any{
		"_id":       packageName,
		"name":      packageName,
		"dist-tags": distTags,
		"versions":  versionMap,
		"time":      timeMap,
	}

	return json.Marshal(packument)
}

//nolint:gochecknoglobals // immutable lookup table
var refAbbreviatedFields = map[string]bool{
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

func refBuildAbbreviatedPackument(repoName, packageName string, versions []*models.NPMVersion, distTags map[string]string, baseURL string) ([]byte, error) {
	versionMap := make(map[string]any, len(versions))

	for _, v := range versions {
		var full map[string]any
		if err := json.Unmarshal(v.ManifestJSON, &full); err != nil {
			return nil, fmt.Errorf("unmarshal manifest for %s@%s: %w", packageName, v.Version, err)
		}

		dist, _ := full["dist"].(map[string]any)
		if dist == nil {
			dist = make(map[string]any)
		}

		dist["tarball"] = fmt.Sprintf("%s/repos/%s/%s/-/%s", baseURL, repoName, packageName, v.TarballFilename)
		dist["integrity"] = v.Integrity
		dist["shasum"] = v.Shasum
		full["dist"] = dist

		slim := make(map[string]any, len(refAbbreviatedFields))
		for field := range refAbbreviatedFields {
			if val, ok := full[field]; ok {
				slim[field] = val
			}
		}

		versionMap[v.Version] = slim
	}

	packument := map[string]any{
		"_id":       packageName,
		"name":      packageName,
		"dist-tags": distTags,
		"versions":  versionMap,
	}

	return json.Marshal(packument)
}

// BenchmarkReferencePackument is the map-based baseline for
// BenchmarkBuildPackument / BenchmarkBuildAbbreviatedPackument.
func BenchmarkReferencePackument(b *testing.B) {
	distTags := map[string]string{"latest": "1.0.0", "next": "1.1.0-beta.1"}

	builders := map[string]func(string, string, []*models.NPMVersion, map[string]string, string) ([]byte, error){
		"full":        refBuildPackument,
		"abbreviated": refBuildAbbreviatedPackument,
	}

	for name, build := range builders {
		for _, n := range []int{100, 1000} {
			versions := benchNPMVersions(n)

			b.Run(fmt.Sprintf("%s/versions=%d", name, n), func(b *testing.B) {
				b.ReportAllocs()

				var size int

				for b.Loop() {
					out, err := build("myrepo", "@scope/example", versions, distTags, "http://localhost:8080")
					if err != nil {
						b.Fatal(err)
					}

					size = len(out)
				}

				b.SetBytes(int64(size))
			})
		}
	}
}
