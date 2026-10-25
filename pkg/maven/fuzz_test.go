// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package maven

import (
	"bytes"
	"testing"
)

// FuzzParseRepositoryPath feeds arbitrary repository paths; a successful parse
// must also round-trip through the coordinate validation without panicking.
func FuzzParseRepositoryPath(f *testing.F) {
	f.Add("org/apache/maven/maven-core/3.9.0/maven-core-3.9.0.jar")
	f.Add("a/b/1/b-1-sources.jar")
	f.Add("../../etc/passwd")
	f.Add("a/b")
	f.Add("")

	f.Fuzz(func(_ *testing.T, p string) {
		_, _, version, filename, err := ParseRepositoryPath(p)
		if err != nil {
			return
		}

		_ = IsSnapshot(version)
		_, _, _, _ = ParseSnapshotTimestamp(filename)
	})
}

// FuzzParsePOM feeds arbitrary XML to the POM parser.
func FuzzParsePOM(f *testing.F) {
	f.Add(`<project><groupId>g</groupId><artifactId>a</artifactId><version>1</version></project>`)
	f.Add(`<project><parent><groupId>g</groupId></parent>`)
	f.Add("")

	f.Fuzz(func(_ *testing.T, in string) {
		_, _ = ParsePOM(bytes.NewReader([]byte(in)))
	})
}

// FuzzParseMetadata feeds arbitrary maven-metadata.xml documents.
func FuzzParseMetadata(f *testing.F) {
	f.Add([]byte(`<metadata><groupId>g</groupId><artifactId>a</artifactId><versioning><versions><version>1</version></versions></versioning></metadata>`))
	f.Add([]byte("<metadata>"))

	f.Fuzz(func(_ *testing.T, in []byte) {
		if m, err := ParseMetadata(in); err == nil {
			_, _ = MergeMetadata(m, m)
		}
	})
}

// FuzzCompareVersions feeds arbitrary version pairs to the comparator.
func FuzzCompareVersions(f *testing.F) {
	f.Add("1.0", "1.0-SNAPSHOT")
	f.Add("", "1..2")

	f.Fuzz(func(_ *testing.T, a, b string) {
		_ = CompareVersions(a, b)
	})
}
