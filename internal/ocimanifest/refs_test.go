// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ocimanifest

import (
	"slices"
	"testing"
)

func TestParseRefs(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantBlobs     []string
		wantManifests []string
	}{
		{
			name:      "image manifest",
			body:      `{"schemaVersion":2,"config":{"digest":"sha256:c"},"layers":[{"digest":"sha256:l1"},{"digest":"sha256:l2"},{"digest":"sha256:l1"}]}`,
			wantBlobs: []string{"sha256:c", "sha256:l1", "sha256:l2"},
		},
		{
			name:          "index",
			body:          `{"schemaVersion":2,"manifests":[{"digest":"sha256:a"},{"digest":"sha256:b"}]}`,
			wantManifests: []string{"sha256:a", "sha256:b"},
		},
		{
			name:      "artifact manifest with blobs",
			body:      `{"config":{"digest":"sha256:c"},"blobs":[{"digest":"sha256:x"}],"subject":{"digest":"sha256:s"}}`,
			wantBlobs: []string{"sha256:c", "sha256:x"},
		},
		{
			name:      "docker schema 1",
			body:      `{"schemaVersion":1,"fsLayers":[{"blobSum":"sha256:f1"},{"blobSum":"sha256:f2"}]}`,
			wantBlobs: []string{"sha256:f1", "sha256:f2"},
		},
		{name: "not json", body: `not a manifest`},
		{name: "empty digests are ignored", body: `{"config":{"digest":""},"layers":[{}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRefs([]byte(tt.body))
			if !slices.Equal(got.Blobs, tt.wantBlobs) {
				t.Errorf("Blobs = %v, want %v", got.Blobs, tt.wantBlobs)
			}

			if !slices.Equal(got.Manifests, tt.wantManifests) {
				t.Errorf("Manifests = %v, want %v", got.Manifests, tt.wantManifests)
			}
		})
	}
}

func TestPushedBlobsSkipsForeignLayers(t *testing.T) {
	body := `{"config":{"digest":"sha256:cfg"},"layers":[
		{"digest":"sha256:a","mediaType":"application/vnd.oci.image.layer.v1.tar+gzip"},
		{"digest":"sha256:f1","mediaType":"application/vnd.docker.image.rootfs.foreign.diff.tar.gzip"},
		{"digest":"sha256:f2","mediaType":"application/vnd.oci.image.layer.nondistributable.v1.tar+gzip"},
		{"digest":"sha256:f3","urls":["https://example.com/l"]}]}`

	got := PushedBlobs([]byte(body))
	if len(got) != 2 || got[0] != "sha256:cfg" || got[1] != "sha256:a" {
		t.Fatalf("PushedBlobs = %v, want [sha256:cfg sha256:a]", got)
	}

	if refs := ParseRefs([]byte(body)); len(refs.Blobs) != 5 {
		t.Fatalf("ParseRefs must still record every blob for GC, got %v", refs.Blobs)
	}
}
