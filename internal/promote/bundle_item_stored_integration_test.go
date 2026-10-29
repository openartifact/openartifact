// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package promote

import (
	"context"
	"strings"
	"testing"
)

// TestBundleItemStored pins the lookup that decides whether a repository backs
// a bundle item: the digest must be stored in that repository and the declared
// path must locate it.
func TestBundleItemStored(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	src := h.repo("maven", false)
	other := h.repo("maven", false)
	dock := h.repo("docker", false)

	blobID, sha := h.blob("jar bytes")
	_, otherSHA := h.blob("something else")
	fileID := h.file(src.ID, blobID, fileSeed{pkg: "g:a", version: "1.0", path: "g/a/1.0/a-1.0.jar", filename: "a-1.0.jar"})

	gone := h.file(src.ID, blobID, fileSeed{pkg: "g:a", version: "1.0", path: "g/a/1.0/gone.jar", filename: "gone.jar"})
	h.exec(`UPDATE package_files SET deleted_at = now() WHERE id = $1`, gone)

	mBlob, mSHA := h.blob("manifest bytes")
	seed := seedDockerManifest(h, dock.ID, mBlob, dockerSpec{image: "app", blobs: []string{"layer"}, tags: []string{"v1"}})

	tests := []struct {
		name   string
		repo   string
		name2  string
		format string
		sha    string
		path   string
		want   bool
	}{
		{"exact path", src.ID, src.Name, "maven", sha, "g/a/1.0/a-1.0.jar", true},
		{"leading slash", src.ID, src.Name, "maven", sha, "/g/a/1.0/a-1.0.jar", true},
		{"filename", src.ID, src.Name, "maven", sha, "a-1.0.jar", true},
		{"file id", src.ID, src.Name, "maven", sha, fileID, true},
		{"last element of a longer path", src.ID, src.Name, "maven", sha, "pool/main/a-1.0.jar", true},
		{"no path", src.ID, src.Name, "maven", sha, "", true},
		{"another path", src.ID, src.Name, "maven", sha, "g/a/1.0/b-1.0.jar", false},
		{"deleted file", src.ID, src.Name, "maven", sha, "g/a/1.0/gone.jar", false},
		{"digest of another blob", src.ID, src.Name, "maven", otherSHA, "g/a/1.0/a-1.0.jar", false},
		{"repository that does not hold it", other.ID, other.Name, "maven", sha, "g/a/1.0/a-1.0.jar", false},
		{"docker digest", dock.ID, dock.Name, "docker", mSHA, seed.digest, true},
		{"docker image", dock.ID, dock.Name, "docker", mSHA, "app", true},
		{"docker tag", dock.ID, dock.Name, "docker", mSHA, "app:v1", true},
		{"docker distribution name", dock.ID, dock.Name, "docker", mSHA, dock.Name + "/app:v1", true},
		{"docker unrelated path", dock.ID, dock.Name, "docker", mSHA, "evil:latest", false},
		{"docker digest not in the repository", src.ID, src.Name, "docker", mSHA, "app", false},
		{"unknown digest", src.ID, src.Name, "maven", strings.Repeat("0", 64), "g/a/1.0/a-1.0.jar", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := h.pg.BundleItemStored(ctx, tc.repo, tc.name2, tc.format, tc.sha, tc.path)
			if err != nil {
				t.Fatalf("BundleItemStored: %v", err)
			}

			if got != tc.want {
				t.Errorf("BundleItemStored(%s, %q) = %v, want %v", tc.name2, tc.path, got, tc.want)
			}
		})
	}
}
