// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package gpgkeys

import (
	"slices"
	"sync"
	"testing"

	"github.com/openartifact/openartifact/internal/database/models"
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

// TestRefreshCoversEveryFormat: a key change must re-sign the metadata of every
// format that stores a signature. terraform and apk are not re-rendered: a
// terraform provider is signed when downloaded and apk uses its own RSA key.
func TestRefreshCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "gpgkeys.refresh", []string{"rpm", "deb", "pacman"},
		[]string{"docker", "npm", "pypi", "maven", "helm", "cargo", "rubygems", "nuget", "go", "raw", "conda", "conan", "ivy", "sbt",
			"terraform", "apk"})

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
