// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package itempath

import "testing"

// TestLocatorFile pins which spellings name a stored file, per format.
func TestLocatorFile(t *testing.T) {
	const repo = "libs"

	multiGroup := File{ID: "id-1", Path: "com.example/lib/1.0/lib-1.0.jar", Filename: "lib-1.0.jar"}
	subdir := File{ID: "id-2", Path: "acme/lib/1.0/jars/lib-1.0.jar", Filename: "jars/lib-1.0.jar"}
	maven := File{ID: "id-3", Path: "g/a/1.0/a-1.0.jar", Filename: "g/a/1.0/a-1.0.jar"}
	pypi := File{ID: "id-4", Path: "demo/1.0/demo-1.0.whl", Filename: "demo-1.0.whl"}
	sbt := File{ID: "id-5", Path: "org.scala/lib_2.13/1.0/lib_2.13-1.0.jar", Filename: "lib_2.13-1.0.jar"}

	tests := []struct {
		name   string
		format string
		path   string
		file   File
		want   bool
	}{
		{"ivy stored path", "ivy", "com.example/lib/1.0/lib-1.0.jar", multiGroup, true},
		{"ivy stored path, leading slash", "ivy", "/com.example/lib/1.0/lib-1.0.jar", multiGroup, true},
		{"ivy CLI form (repository URL path)", "ivy", "com/example/lib/1.0/lib-1.0.jar", multiGroup, true},
		{"ivy CLI form, leading slash", "ivy", "/com/example/lib/1.0/lib-1.0.jar", multiGroup, true},
		{"ivy repos URL of this repository", "ivy", "repos/libs/com/example/lib/1.0/lib-1.0.jar", multiGroup, true},
		{"ivy listed file name", "ivy", "lib-1.0.jar", multiGroup, true},
		{"ivy file id", "ivy", "id-1", multiGroup, true},
		{"ivy type directory in the file name", "ivy", "acme/lib/1.0/jars/lib-1.0.jar", subdir, true},
		{"ivy listed file name with its directory", "ivy", "jars/lib-1.0.jar", subdir, true},
		{"ivy another file", "ivy", "com/example/lib/1.0/other-1.0.jar", multiGroup, false},
		{"ivy repos URL of another repository", "ivy", "repos/other/com/example/lib/1.0/lib-1.0.jar", multiGroup, false},
		{"sbt CLI form", "sbt", "org/scala/lib_2.13/1.0/lib_2.13-1.0.jar", sbt, true},
		{"sbt stored path", "sbt", "org.scala/lib_2.13/1.0/lib_2.13-1.0.jar", sbt, true},
		{"sbt other module", "sbt", "org/scala/other_2.13/1.0/other_2.13-1.0.jar", sbt, false},
		{"maven stored path", "maven", "g/a/1.0/a-1.0.jar", maven, true},
		{"maven leading slash", "maven", "/g/a/1.0/a-1.0.jar", maven, true},
		{"maven another file", "maven", "g/a/1.0/b-1.0.jar", maven, false},
		{"pypi URL under a prefix", "pypi", "packages/demo/demo-1.0.whl", pypi, true},
		{"pypi stored path", "pypi", "demo/1.0/demo-1.0.whl", pypi, true},
		{"pypi percent-encoded", "pypi", "demo/1.0/demo-1.0%2Ewhl", pypi, true},
		{"no path names every file", "pypi", "", pypi, true},
		{"only slashes names every file", "pypi", "//", pypi, true},
		{"another file name", "pypi", "demo/1.0/demo-2.0.whl", pypi, false},
		{"another repository's URL", "pypi", "repos/other/demo/1.0/demo-1.0.whl", pypi, false},
		{"directory only", "pypi", "demo/1.0/", pypi, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := New(tc.format, repo, tc.path).File(tc.file); got != tc.want {
				t.Errorf("New(%q, %q, %q).File(%+v) = %v, want %v", tc.format, repo, tc.path, tc.file, got, tc.want)
			}
		})
	}
}

func TestLocatorDocker(t *testing.T) {
	const repo = "reg"

	m := Manifest{ID: "mid", Image: "team/app", Digest: "sha256:abc", SHA256: "abc", Tags: []string{"v1"}}
	root := Manifest{ID: "rid", Digest: "sha256:def", SHA256: "def"}

	tests := []struct {
		name string
		path string
		m    Manifest
		want bool
	}{
		{"digest", "sha256:abc", m, true},
		{"hex digest", "abc", m, true},
		{"image", "team/app", m, true},
		{"image and tag", "team/app:v1", m, true},
		{"distribution name and tag", "reg/team/app:v1", m, true},
		{"distribution name and digest", "reg/team/app@sha256:abc", m, true},
		{"manifest URL path", "/v2/reg/team/app/manifests/v1", m, true},
		{"tag", "v1", m, true},
		{"manifest id", "mid", m, true},
		{"default image by repository name", "reg", root, true},
		{"unrelated name", "evil:latest", m, false},
		{"another image's tag", "other/app:v1", m, false},
		{"another repository", "other/team/app:v1", m, false},
		{"no path", "", m, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := New("docker", repo, tc.path).Docker(tc.m); got != tc.want {
				t.Errorf("New(docker, %q).Docker(%+v) = %v, want %v", tc.path, tc.m, got, tc.want)
			}
		})
	}
}
