// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	pkgmaven "github.com/openartifact/openartifact/pkg/maven"
)

func BenchmarkGenerateGroupMetadataXML(b *testing.B) {
	updated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, n := range []int{100, 1000, 10000} {
		versions := make([]string, n)
		for i := range versions {
			versions[i] = fmt.Sprintf("%d.%d.%d", i/100, (i/10)%10, i%10)
		}

		b.Run(fmt.Sprintf("versions=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			var size int

			for b.Loop() {
				out, err := GenerateGroupMetadataXML("com.example", "app", versions, versions[n-1], versions[n-1], updated)
				if err != nil {
					b.Fatal(err)
				}

				size = len(out)
			}

			b.SetBytes(int64(size))
		})
	}
}

func BenchmarkGenerateSnapshotMetadataXML(b *testing.B) {
	updated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	coord := pkgmaven.Coordinate{GroupID: "com.example", ArtifactID: "app", Version: "1.0-SNAPSHOT"}

	for _, n := range []int{10, 100, 1000} {
		entries := make([]SnapshotEntry, n)
		for i := range entries {
			entries[i] = SnapshotEntry{
				Extension:  "jar",
				Classifier: "c" + strconv.Itoa(i),
				Value:      "1.0-20260102.030405-" + strconv.Itoa(i),
				Updated:    "20260102030405",
			}
		}

		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if _, err := GenerateSnapshotMetadataXML(coord, "20260102.030405", n, updated, entries); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
