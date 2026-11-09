// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"errors"
	"testing"
)

func TestValidateDockerManifestDigests(t *testing.T) {
	const (
		body  = "aa"
		layer = "bb"
	)

	ids := map[string]string{body: "id-body", layer: "id-layer"}

	tests := []struct {
		name    string
		digest  string
		blob    string
		wantErr bool
	}{
		{"matching", "sha256:" + body, "sha256:" + layer, false},
		{"manifest digest not body hash", "sha256:cc", "sha256:" + layer, true},
		{"blob digest not content hash", "sha256:" + body, "sha256:cc", true},
		{"foreign algorithm", "sha512:" + body, "sha256:" + layer, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &dockerManifestData{
				Image:  "app",
				Digest: tt.digest,
				BlobID: body,
				Blobs:  []dockerBlobData{{Digest: tt.blob, SHA256: layer, Size: 1}},
			}

			_, _, err := validateDockerManifest(d, "app", tt.digest, ids)
			if tt.wantErr != errors.Is(err, ErrInvalid) || (!tt.wantErr && err != nil) {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
