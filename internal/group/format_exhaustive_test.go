// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package group

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// TestServeMergedCoversEveryFormat pins which formats serveMerged merges
// itself. The handled ones need a database or a full member fixture to reach
// their merge code, so they are classified only; the fall-through formats are
// probed.
func TestServeMergedCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "group.serveMerged",
		[]string{
			"maven", "pypi", "npm", "helm", "cargo", "go", "nuget", "rubygems", "conan", "terraform", "conda",
			"deb", "rpm", "pacman", "apk", "nix",
		},
		// docker is served by the OCI path before serveMerged; the others answer from the first member that has the file.
		[]string{"docker", "raw", "ivy", "sbt"},
	)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		if slices.Contains(handled, format) {
			return
		}

		f := newFixture(t, format)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)

		if f.h.serveMerged(httptest.NewRecorder(), req, f.grp, nil, "some/file.txt", f.backend()) {
			t.Errorf("serveMerged(%s) merged a request, but the format is classified as first-hit", format)
		}
	})
}

// TestPackageRefCoversEveryFormat pins the formats whose requests name a
// package for the hosted-wins rule, and probes each with a package request.
func TestPackageRefCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "group.packageRef",
		[]string{
			"npm", "pypi", "go", "cargo", "nuget", "maven", "helm", "ivy", "sbt", "rubygems", "conan", "conda", "terraform",
		},
		// docker has its own reference (ociRef); raw has no versions to hide; deb, rpm, apk and
		// pacman serve generated indexes and are resolved member by member; nix asks hosted members before proxy ones and has no
		// package names to hide.
		[]string{"docker", "raw", "deb", "rpm", "apk", "pacman", "nix"},
	)

	packageRequests := map[string]string{
		"npm":       "lodash",
		"pypi":      "simple/requests/",
		"go":        "example.com/mod/@v/list",
		"cargo":     "index/de/mo/demo",
		"nuget":     "v3/registration/lib/index.json",
		"maven":     "org/acme/lib/1.0/lib-1.0.jar",
		"helm":      "charts/app-1.0.0.tgz",
		"ivy":       "org.acme/lib/1.0/jars/lib.jar",
		"sbt":       "org/acme/lib_2.13/1.0/lib_2.13-1.0.jar",
		"rubygems":  "info/rails",
		"conan":     "v2/conans/zlib/1.3/_/_/latest",
		"conda":     "linux-64/numpy-1.26.0-py311_0.conda",
		"terraform": "aws/versions",
	}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		_, ok := packageRef(format, packageRequests[format])

		if slices.Contains(handled, format) != ok {
			t.Errorf("packageRef(%s, %q) ok = %v, want %v", format, packageRequests[format], ok, !ok)
		}
	})
}

// TestClassifyDebRPMCoversEveryFormat pins the formats classifyDebRPM has a
// path grammar for. rpm is the default branch; the others never reach it (apk
// has its own handler, the rest are not generated-metadata formats).
func TestClassifyDebRPMCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "group.classifyDebRPM",
		[]string{"deb", "rpm", "pacman"},
		[]string{
			"docker", "npm", "pypi", "maven", "helm", "cargo", "rubygems", "nuget", "go", "raw", "conda", "conan",
			"ivy", "sbt", "terraform", "apk", "nix",
		},
	)

	metadata := map[string]string{
		"deb":    "dists/stable/Release",
		"rpm":    "repodata/repomd.xml",
		"pacman": "os/x86_64/core.db",
	}

	for _, format := range handled {
		if got := classifyDebRPM(format, metadata[format]); got.class != classMetadata {
			t.Errorf("classifyDebRPM(%s, %q).class = %v, want metadata", format, metadata[format], got.class)
		}
	}

	// The set of formats whose groups generate metadata is the registry's.
	for _, format := range repotype.Names() {
		if repotype.GeneratedGroupMetadata(format) && format != "apk" && !slices.Contains(handled, format) {
			t.Errorf("classifyDebRPM: %s generates group metadata but has no path grammar", format)
		}
	}
}
