// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package swift_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/openartifact/openartifact/pkg/swift"
)

// testdata/swiftpm-archive-source.zip is what "swift package archive-source"
// (Swift 6.4.3) produced for a package with Package.swift (tools 5.7),
// Package@swift-5.9.swift (tools 5.9) and one source file: a git archive with
// one top-level directory and the revision as the zip comment.
func TestReadArchiveFromSwiftPM(t *testing.T) {
	data, err := os.ReadFile("testdata/swiftpm-archive-source.zip")
	if err != nil {
		t.Fatal(err)
	}

	manifests, err := swift.ReadArchive(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	if len(manifests) != 2 {
		t.Fatalf("manifests = %+v, want Package.swift and Package@swift-5.9.swift", manifests)
	}

	if m := manifests[0]; m.Filename != "Package.swift" || m.SwiftVersion != "" || m.ToolsVersion != "5.7" {
		t.Errorf("manifests[0] = %+v", m)
	}

	if m := manifests[1]; m.Filename != "Package@swift-5.9.swift" || m.SwiftVersion != "5.9" || m.ToolsVersion != "5.9" {
		t.Errorf("manifests[1] = %+v", m)
	}
}
