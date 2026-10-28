// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpgkeys

import (
	"slices"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

type recordingRefresher struct {
	mu  sync.Mutex
	ids []string
}

func (r *recordingRefresher) NotifyStale(id string) {
	r.mu.Lock()
	r.ids = append(r.ids, id)
	r.mu.Unlock()
}

// signedWhenServed lists the GPG-signed formats that need no refresh because
// they store no signature: terraform builds SHA256SUMS, its detached signature
// and the signing_keys of the package document from the repository's current
// key on every request (internal/format/terraform
// TestProviderSigningFollowsKeyRotation proves a rotation is picked up).
//
//nolint:gochecknoglobals // immutable test table
var signedWhenServed = []string{"terraform"}

// TestRefreshCoversEveryFormat: a key change must re-sign the metadata of every
// format that stores a signature. terraform is not re-rendered (see
// signedWhenServed) and apk uses its own RSA key, not a GPG key.
func TestRefreshCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "gpgkeys.refresh", []string{"deb", "pacman", "rpm"},
		[]string{"ansible", "apk", "cargo", "conan", "conda", "docker", "go", "helm", "ivy", "maven", "nix", "npm", "nuget",
			"pypi", "raw", "rubygems", "sbt", "terraform"})

	for _, spec := range repotypetest.Specs() {
		if spec.Signing == repotype.SigningGPG && !slices.Contains(handled, spec.Name) && !slices.Contains(signedWhenServed, spec.Name) {
			t.Errorf("format %s is GPG-signed but neither refreshed nor listed in signedWhenServed", spec.Name)
		}
	}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		// rpm marks the repository stale in the database first, which the test does not build.
		if format == "rpm" {
			return
		}

		deb, pacman, rpm := &recordingRefresher{}, &recordingRefresher{}, &recordingRefresher{}
		s := &Service{deb: deb, pacman: pacman, rpm: rpm}
		s.refresh(t.Context(), []models.GPGSigningRepo{{ID: "r1", Format: format}, {ID: "r1", Format: format}})

		want := map[string][]string{}
		if slices.Contains(handled, format) {
			want[format] = []string{"r1"}
		}

		for name, r := range map[string]*recordingRefresher{"deb": deb, "pacman": pacman, "rpm": rpm} {
			if !slices.Equal(r.ids, want[name]) {
				t.Errorf("format %s: %s notified %v, want %v (duplicates collapse)", format, name, r.ids, want[name])
			}
		}
	})
}
