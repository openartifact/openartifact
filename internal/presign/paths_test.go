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
		{"helm", "charts/x-1.0.0.tgz", false},
		{"docker", "v2/x/manifests/latest", false},
		{"", "a", false},
	}

	for _, tt := range tests {
		if got := IsProxyBlobPath(tt.format, tt.path); got != tt.want {
			t.Errorf("IsProxyBlobPath(%q, %q) = %v, want %v", tt.format, tt.path, got, tt.want)
		}
	}
}
