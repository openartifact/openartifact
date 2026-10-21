// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// benchNPMVersions builds n versions of "@scope/example" with a realistic
// ~1 KiB stored manifest each.
func benchNPMVersions(n int) []*models.NPMVersion {
	versions := make([]*models.NPMVersion, n)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := range versions {
		v := fmt.Sprintf("%d.%d.%d", i/10000, (i/100)%100, i%100)
		manifest := fmt.Sprintf(`{
  "name": "@scope/example",
  "version": %q,
  "description": "An example package used for benchmarking packument generation",
  "main": "dist/index.js",
  "types": "dist/index.d.ts",
  "scripts": {"build": "tsc -p .", "test": "jest --coverage", "lint": "eslint src"},
  "repository": {"type": "git", "url": "git+https://example.org/scope/example.git"},
  "keywords": ["example", "benchmark", "packument"],
  "author": "Jane <jane@example.org>",
  "license": "MIT",
  "dependencies": {"lodash": "^4.17.21", "express": "^4.18.2", "zod": "^3.22.0", "chalk": "^5.3.0"},
  "devDependencies": {"typescript": "^5.3.0", "jest": "^29.7.0", "eslint": "^8.56.0"},
  "peerDependencies": {"react": ">=17"},
  "engines": {"node": ">=18"},
  "dist": {"shasum": "ignored", "tarball": "ignored"},
  "_id": "@scope/example@%s",
  "readme": "ERROR: No README data found!"
}`, v, v)

		versions[i] = &models.NPMVersion{
			ID:              strconv.Itoa(i),
			PackageID:       "pkg-1",
			Version:         v,
			ManifestJSON:    []byte(manifest),
			Integrity:       "sha512-" + fmt.Sprintf("%088x", i),
			Shasum:          fmt.Sprintf("%040x", i),
			TarballFilename: "example-" + v + ".tgz",
			CreatedAt:       base.Add(time.Duration(i) * time.Hour),
		}
	}

	return versions
}

func BenchmarkBuildPackument(b *testing.B) {
	distTags := map[string]string{"latest": "1.0.0", "next": "1.1.0-beta.1"}

	for _, n := range []int{100, 1000, 10000} {
		versions := benchNPMVersions(n)

		b.Run(fmt.Sprintf("versions=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			var size int

			for b.Loop() {
				out, err := BuildPackument("myrepo", "@scope/example", versions, distTags, "http://localhost:8080")
				if err != nil {
					b.Fatal(err)
				}

				size = len(out)
			}

			b.SetBytes(int64(size))
		})
	}
}

func BenchmarkBuildAbbreviatedPackument(b *testing.B) {
	distTags := map[string]string{"latest": "1.0.0", "next": "1.1.0-beta.1"}

	for _, n := range []int{100, 1000, 10000} {
		versions := benchNPMVersions(n)

		b.Run(fmt.Sprintf("versions=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			var size int

			for b.Loop() {
				out, err := BuildAbbreviatedPackument("myrepo", "@scope/example", versions, distTags, "http://localhost:8080")
				if err != nil {
					b.Fatal(err)
				}

				size = len(out)
			}

			b.SetBytes(int64(size))
		})
	}
}
