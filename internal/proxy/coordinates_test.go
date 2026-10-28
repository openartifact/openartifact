// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"strings"
	"testing"
)

func TestPackageCoordinates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, format, key string
		wantName, wantVer string
		wantOK            bool
	}{
		{"npm tarball", "npm", "lodash/-/lodash-4.17.20.tgz", "lodash", "4.17.20", true},
		{"npm scoped", "npm", "@babel/core/-/core-7.0.0-beta.1.tgz", "@babel/core", "7.0.0-beta.1", true},
		{"npm packument", "npm", "lodash", "", "", false},
		{"npm abbreviated", "npm", "-/install-v1/@babel/core", "", "", false},
		{"npm wrong file", "npm", "lodash/-/other-1.0.0.tgz", "", "", false},
		{"npm bad scope", "npm", "babel/core/-/core-1.0.0.tgz", "", "", false},

		{"pypi sdist", "pypi", "files/requests/requests-2.31.0.tar.gz", "requests", "2.31.0", true},
		{"pypi sdist dashed", "pypi", "files/my-pkg/my_pkg-1.0.zip", "my-pkg", "1.0", true},
		{"pypi sdist bz2", "pypi", "files/foo/foo-0.1.tar.bz2", "foo", "0.1", true},
		{"pypi wheel", "pypi", "files/numpy/numpy-1.26.0-cp311-cp311-manylinux_2_17_x86_64.whl", "numpy", "1.26.0", true},
		{"pypi wheel underscore", "pypi", "files/my-pkg/my_pkg-2.0-py3-none-any.whl", "my-pkg", "2.0", true},
		{"pypi simple page", "pypi", "simple/requests/", "", "", false},
		{"pypi metadata", "pypi", "files/requests/requests-2.31.0.tar.gz.asc", "", "", false},
		{"pypi mismatched project", "pypi", "files/foo/bar-1.0.tar.gz", "", "", false},

		{"maven jar", "maven", "org/apache/commons/commons-lang3/3.12.0/commons-lang3-3.12.0.jar",
			"org.apache.commons:commons-lang3", "3.12.0", true},
		{"maven pom", "maven", "junit/junit/4.13.2/junit-4.13.2.pom", "junit:junit", "4.13.2", true},
		{"maven classifier", "maven", "g/a/1.0/a-1.0-sources.jar", "g:a", "1.0", true},
		{"maven snapshot", "maven", "g/a/1.0-SNAPSHOT/a-1.0-20240101.120000-3.jar", "g:a", "1.0-SNAPSHOT", true},
		{"maven checksum", "maven", "g/a/1.0/a-1.0.jar.sha1", "", "", false},
		{"maven signature", "maven", "g/a/1.0/a-1.0.jar.asc", "", "", false},
		{"maven metadata", "maven", "g/a/maven-metadata.xml", "", "", false},
		{"maven wrong file", "maven", "g/a/1.0/b-1.0.jar", "", "", false},
		{"maven too short", "maven", "a/1.0/a-1.0.jar", "", "", false},

		{"cargo crate", "cargo", "api/v1/crates/serde/1.0.190/download", "serde", "1.0.190", true},
		{"cargo index", "cargo", "index/se/rd/serde", "", "", false},
		{"cargo config", "cargo", "config.json", "", "", false},

		{"rubygems plain", "rubygems", "gems/rack-3.0.8.gem", "rack", "3.0.8", true},
		{"rubygems dashed name", "rubygems", "gems/activerecord-session-store-2.1.0.gem", "activerecord-session-store", "2.1.0", true},
		{"rubygems platform", "rubygems", "gems/nokogiri-1.15.4-x86_64-linux.gem", "nokogiri", "1.15.4", true},
		{"rubygems prerelease", "rubygems", "gems/foo-1.0.0.pre1.gem", "foo", "1.0.0.pre1", true},
		{"rubygems digit segment", "rubygems", "gems/foo-3d-1.0.gem", "foo-3d", "1.0", true},
		{"rubygems info", "rubygems", "info/rack", "", "", false},
		{"rubygems no version", "rubygems", "gems/rack.gem", "", "", false},
		{"rubygems quick gemspec", "rubygems", "quick/Marshal.4.8/rack-3.0.8.gemspec.rz", "", "", false},

		{"nuget nupkg", "nuget", "v3/package/newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg", "newtonsoft.json", "13.0.3", true},
		{"nuget nuspec", "nuget", "v3/package/newtonsoft.json/13.0.3/newtonsoft.json.nuspec", "", "", false},
		{"nuget versions", "nuget", "v3/package/newtonsoft.json/index.json", "", "", false},
		{"nuget registration", "nuget", "v3/registration/newtonsoft.json/13.0.3.json", "", "", false},
		{"nuget mismatched", "nuget", "v3/package/a/1.0.0/b.1.0.0.nupkg", "", "", false},

		{"go zip", "go", "github.com/gin-gonic/gin/@v/v1.9.1.zip", "github.com/gin-gonic/gin", "v1.9.1", true},
		{"go case encoded", "go", "github.com/!azure/azure-sdk/@v/v1.0.0.zip", "github.com/Azure/azure-sdk", "v1.0.0", true},
		{"go incompatible", "go", "github.com/x/y/@v/v2.0.0+incompatible.zip", "github.com/x/y", "v2.0.0+incompatible", true},
		{"go mod", "go", "github.com/gin-gonic/gin/@v/v1.9.1.mod", "", "", false},
		{"go info", "go", "github.com/gin-gonic/gin/@v/v1.9.1.info", "", "", false},
		{"go list", "go", "github.com/gin-gonic/gin/@v/list", "", "", false},
		{"go latest", "go", "github.com/gin-gonic/gin/@latest", "", "", false},
		{"go bad encoding", "go", "github.com/Azure/x/@v/v1.0.0.zip", "", "", false},

		{"helm chart", "helm", "charts/nginx-15.4.2.tgz", "nginx", "15.4.2", true},
		{"helm dashed name", "helm", "charts/ingress-nginx-4.8.0.tgz", "ingress-nginx", "4.8.0", true},
		{"helm prerelease", "helm", "charts/foo-1.0.0-rc-1.tgz", "foo", "1.0.0-rc-1", true},
		{"helm subdir", "helm", "charts/stable/foo-1.0.0.tgz", "foo", "1.0.0", true},
		{"helm index", "helm", "index.yaml", "", "", false},
		{"helm no version", "helm", "charts/foo.tgz", "", "", false},

		{"unknown format", "docker", "v2/foo/manifests/latest", "", "", false},
		{"empty key", "npm", "", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			name, version, ok := PackageCoordinates(tt.format, tt.key)
			if ok != tt.wantOK || name != tt.wantName || version != tt.wantVer {
				t.Errorf("PackageCoordinates(%q, %q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.format, tt.key, name, version, ok, tt.wantName, tt.wantVer, tt.wantOK)
			}
		})
	}
}

// TestPackageCoordinatesOddInputs asserts arbitrary keys never panic and an
// accepted key always yields a non-empty name and version.
func TestPackageCoordinatesOddInputs(t *testing.T) {
	t.Parallel()

	odd := []string{
		"", "/", "//", "-", "/-/", "@", "@/-/.tgz", "/@v/.zip", "@v/.zip", "a/@v/.zip", "files//", "files/a/", "files/a/.whl",
		"gems/-.gem", "gems/-1-.gem", "gems/.gem", "v3/package///", "api/v1/crates////download", "charts/", "charts/-.tgz",
		"charts/-1.0.0.tgz", "a/b/c/d", "../../x/1/x-1.jar", "a/./1/a-1.jar", "g/a/-SNAPSHOT/a--SNAPSHOT.jar",
		"g/a/1-SNAPSHOT/a-1-.jar", "\x00", "a\\b", strings.Repeat("-", 64), strings.Repeat("a/", 100), "é/-/é-1.tgz",
	}
	formats := []string{"cargo", "docker", "go", "helm", "maven", "npm", "nuget", "pypi", "rubygems", ""}

	for _, f := range formats {
		for _, k := range odd {
			name, version, ok := PackageCoordinates(f, k)
			if ok && (name == "" || version == "") {
				t.Errorf("PackageCoordinates(%q, %q) ok with empty coordinates (%q, %q)", f, k, name, version)
			}
		}
	}
}
