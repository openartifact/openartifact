// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"fmt"
	"strings"
	"testing"
)

func benchPOM(deps int) string {
	var sb strings.Builder

	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId>
  <artifactId>app</artifactId>
  <version>2.0.0</version>
  <packaging>jar</packaging>
  <name>My App</name>
  <description>A test app</description>
  <url>https://example.com</url>
  <licenses><license><name>Apache 2.0</name><url>https://www.apache.org/licenses/LICENSE-2.0</url></license></licenses>
  <dependencies>
`)

	for i := range deps {
		fmt.Fprintf(&sb, `    <dependency>
      <groupId>org.example.group%d</groupId>
      <artifactId>artifact-%d</artifactId>
      <version>1.%d.0</version>
      <scope>compile</scope>
    </dependency>
`, i%20, i, i)
	}

	sb.WriteString("  </dependencies>\n</project>\n")

	return sb.String()
}

func BenchmarkParsePOM(b *testing.B) {
	for _, deps := range []int{10, 100, 1000} {
		src := benchPOM(deps)

		b.Run(fmt.Sprintf("deps=%d", deps), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(src)))

			for b.Loop() {
				if _, err := ParsePOM(strings.NewReader(src)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// benchMetadata builds an artifact-level maven-metadata.xml with n versions
// (offset shifts the version numbers so documents overlap partially).
func benchMetadata(n, offset int) *Metadata {
	versions := make([]string, 0, n)

	for i := range n {
		v := fmt.Sprintf("%d.%d.%d", (i+offset)/100, ((i+offset)/10)%10, (i+offset)%10)
		if i%7 == 0 {
			v += "-SNAPSHOT"
		}

		versions = append(versions, v)
	}

	return &Metadata{
		GroupID:    "com.example",
		ArtifactID: "app",
		Versioning: &Versioning{Versions: versions, LastUpdated: "20260101120000"},
	}
}

func BenchmarkMergeMetadata(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		docs := []*Metadata{benchMetadata(n, 0), benchMetadata(n, n/2), benchMetadata(n, n)}

		b.Run(fmt.Sprintf("versions=%d/members=3", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				if _, err := MergeMetadata(docs...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkMetadataParseMarshal(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		doc := benchMetadata(n, 0)

		raw, err := doc.Marshal()
		if err != nil {
			b.Fatal(err)
		}

		b.Run(fmt.Sprintf("versions=%d/Marshal", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))

			for b.Loop() {
				if _, err := doc.Marshal(); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run(fmt.Sprintf("versions=%d/Parse", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))

			for b.Loop() {
				if _, err := ParseMetadata(raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCompareVersions(b *testing.B) {
	b.ReportAllocs()

	for b.Loop() {
		_ = CompareVersions("1.10.0-beta-2", "1.10.0-rc1")
	}
}

// benchVersions builds n distinct versions in shuffled order, mixing release,
// qualifier and snapshot shapes as seen in real maven-metadata.xml files.
func benchVersions(n int) []string {
	versions := make([]string, 0, n)

	for i := range n {
		v := fmt.Sprintf("%d.%d.%d", i/100, (i/10)%10, i%10)

		switch i % 5 {
		case 1:
			v += "-SNAPSHOT"
		case 2:
			v += "-RC1"
		case 3:
			v += ".Final"
		}

		versions = append(versions, v)
	}

	// Deterministic shuffle: stride permutation so the input is not presorted.
	out := make([]string, n)
	for i, v := range versions {
		out[(i*7919)%n] = v
	}

	return out
}

// BenchmarkSortVersions orders a metadata version list in Maven order.
func BenchmarkSortVersions(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		src := benchVersions(n)
		work := make([]string, n)

		b.Run(fmt.Sprintf("versions=%d", n), func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				copy(work, src)
				SortVersions(work)
			}
		})
	}
}
