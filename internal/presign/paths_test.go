// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package presign

import "testing"

func TestIsProxyBlobPath(t *testing.T) {
	tests := []struct {
		format, path string
		want         bool
	}{
		{"maven", "com/example/app/1.0/app-1.0.jar", true},
		{"maven", "com/example/app/1.0/app-1.0.pom", true},
		{"maven", "com/example/app/maven-metadata.xml", false},
		{"maven", "com/example/app/1.0-SNAPSHOT/maven-metadata.xml.sha1", false},
		{"maven", "com/example/app/1.0/app-1.0.jar.sha1", false},
		{"maven", "com/example/app/1.0/app-1.0.jar.asc", false},
		{"maven", "archetype-catalog.xml", false},
		{"maven", ".index/nexus-maven-repository-index.gz", false},
		{"deb", "pool/main/h/hello/hello_2.10_amd64.deb", true},
		{"deb", "dists/stable/Release", false},
		{"deb", "dists/stable/main/binary-amd64/Packages.gz", false},
		{"deb", "dists/stable/InRelease", false},
		{"rpm", "Packages/h/hello-2.10-1.x86_64.rpm", true},
		{"rpm", "x86_64/os/Packages/hello.rpm", true},
		{"rpm", "repodata/repomd.xml", false},
		{"rpm", "os/repodata/abc-primary.xml.gz", false},
		{"rpm", "repodata/abc.rpm", false},
		{"raw", "any/file.bin", true},
		{"pypi", "files/requests/requests-2.32.0-py3-none-any.whl", true},
		{"pypi", "files/requests/requests-2.32.0-py3-none-any.whl.metadata", false},
		{"pypi", "simple/requests/", false},
		{"pypi", "simple/", false},
		{"npm", "left-pad/-/left-pad-1.0.0.tgz", true},
		{"npm", "@babel/core/-/core-7.0.0.tgz", true},
		{"npm", "left-pad", false},
		{"npm", "-/install-v1/left-pad", false},
		{"cargo", "api/v1/crates/serde/1.0.0/download", true},
		{"cargo", "index/se/rd/serde", false},
		{"cargo", "config.json", false},
		{"helm", "charts/x-1.0.0.tgz", true},
		{"helm", "charts/x-1.0.0.tgz.prov", false},
		{"helm", "index.yaml", false},
		{"go", "github.com/!azure/x/@v/v1.0.0.zip", true},
		{"go", "github.com/x/y/@v/v1.0.0.mod", false},
		{"go", "github.com/x/y/@v/v1.0.0.info", false},
		{"go", "github.com/x/y/@v/list", false},
		{"go", "github.com/x/y/@latest", false},
		{"nuget", "v3/package/newtonsoft.json/13.0.1/newtonsoft.json.13.0.1.nupkg", true},
		{"nuget", "v3/package/newtonsoft.json/13.0.1/newtonsoft.json.nuspec", false},
		{"nuget", "v3/package/newtonsoft.json/index.json", false},
		{"nuget", "v3/registration/newtonsoft.json/index.json", false},
		{"rubygems", "gems/rake-13.0.0.gem", true},
		{"rubygems", "gems/rake-13.0.0.gem.sig", false},
		{"rubygems", "versions", false},
		{"rubygems", "info/rake", false},
		{"rubygems", "quick/Marshal.4.8/rake-13.0.0.gemspec.rz", false},
		{"conan", "v2/conans/zlib/1.3.1/_/_/revisions/cac0f6daea041b0ccf42934163defb20/files/conan_export.tgz", true},
		{"conan", "v2/conans/zlib/1.3.1/_/_/revisions/cac0f6daea041b0ccf42934163defb20/files/conanfile.py", false},
		{"conan", "v2/conans/zlib/1.3.1/_/_/revisions/cac0f6daea041b0ccf42934163defb20/files", false},
		{"conan", "v2/conans/zlib/1.3.1/_/_/latest", false},
		{"conda", "linux-64/numpy-1.26.4-py312_0.conda", true},
		{"conda", "noarch/six-1.16.0-pyh6c4a22f_0.tar.bz2", true},
		{"conda", "linux-64/repodata.json", false},
		{"conda", "linux-64/repodata.json.zst", false},
		{"conda", "channeldata.json", false},
		{"docker", "v2/x/manifests/latest", false},
		{"", "a", false},
	}

	for _, tt := range tests {
		if got := IsProxyBlobPath(tt.format, tt.path); got != tt.want {
			t.Errorf("IsProxyBlobPath(%q, %q) = %v, want %v", tt.format, tt.path, got, tt.want)
		}
	}
}
