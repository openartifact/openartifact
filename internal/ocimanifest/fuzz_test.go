// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ocimanifest

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// FuzzParseRefs feeds arbitrary manifest bodies, pushed by a client or relayed
// from an upstream, to the reference extraction behind registry GC: the lists
// hold each digest once and never an empty one, are bounded by the body, are
// deterministic, and the blobs a client must push are among the referenced
// blobs.
func FuzzParseRefs(f *testing.F) {
	for _, s := range []string{
		`{"schemaVersion":2,"config":{"digest":"sha256:c"},"layers":[{"digest":"sha256:l1"},{"digest":"sha256:l1"},{"digest":"","urls":["http://x"]}]}`,
		`{"schemaVersion":2,"manifests":[{"digest":"sha256:m1"},{"digest":"sha256:m2"}]}`,
		`{"schemaVersion":2,"blobs":[{"digest":"sha256:b","mediaType":"application/vnd.oci.image.layer.nondistributable.v1.tar"}]}`,
		`{"schemaVersion":1,"fsLayers":[{"blobSum":"sha256:f"},{"blobSum":"sha256:f"}]}`,
		`{"layers":[{"digest":"sha256:x","mediaType":"application/vnd.docker.image.rootfs.foreign.diff.tar.gzip"}]}`,
		`{"config":null,"layers":null}`, `{"layers":{}}`, `[]`, `null`, `{`, ``,
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		refs := ParseRefs(body)
		pushed := PushedBlobs(body)

		if !json.Valid(body) && (len(refs.Blobs) != 0 || len(refs.Manifests) != 0 || len(pushed) != 0) {
			t.Fatalf("%q is not JSON but references %+v, pushed %v", body, refs, pushed)
		}

		for _, list := range [][]string{refs.Blobs, refs.Manifests, pushed} {
			seen := map[string]struct{}{}

			for _, d := range list {
				if d == "" {
					t.Fatalf("empty digest in %v", list)
				}

				if _, dup := seen[d]; dup {
					t.Fatalf("digest %q listed twice in %v", d, list)
				}

				seen[d] = struct{}{}
			}

			// Every digest needs a quoted value of its own in the body.
			if len(list) > len(body)/2 {
				t.Fatalf("%d digests out of a %d byte body", len(list), len(body))
			}
		}

		for _, d := range pushed {
			if !slices.Contains(refs.Blobs, d) {
				t.Fatalf("pushed blob %q is not a referenced blob of %+v", d, refs)
			}
		}

		if !reflect.DeepEqual(refs, ParseRefs(body)) || !slices.Equal(pushed, PushedBlobs(body)) {
			t.Fatalf("extraction from %q is not deterministic", body)
		}
	})
}
