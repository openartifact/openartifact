// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/repotype"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// TestFormatTablesDecideEveryFormat fails when a format is added to the
// registry without deciding which unit tables enabling federation locks.
// Codec and registry agreement is covered by unit_test.go; this pins the
// per-format tables to the same set.
func TestFormatTablesDecideEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "federation.formatTables",
		[]string{"cargo", "docker", "go", "helm", "maven", "npm", "nuget", "pypi", "raw"},
		[]string{"ansible", "apk", "conan", "conda", "deb", "ivy", "nix", "pacman", "rpm", "rubygems", "sbt", "terraform"})

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		if got := len(formatTables[format]) > 0; got != slices.Contains(handled, format) {
			t.Errorf("formatTables[%q] present = %v, classified handled = %v", format, got, !got)
		}

		if got := repotype.FederationSupported(format); got != slices.Contains(handled, format) {
			t.Errorf("FederationSupported(%q) = %v disagrees with formatTables", format, got)
		}

		_, err := CodecFor(format)
		if (err == nil) != slices.Contains(handled, format) {
			t.Errorf("CodecFor(%q) error = %v disagrees with formatTables", format, err)
		}
	})
}

// TestQuarantineKeysDecideEveryFormat fails when a format is added to the
// registry without deciding which artifact paths its federated units are
// quarantined under. The ignored formats use the unit key as the path.
func TestQuarantineKeysDecideEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "federation.quarantineKeys",
		[]string{"cargo", "docker", "go", "helm", "nuget", "pypi"},
		[]string{
			"ansible", "apk", "conan", "conda", "deb", "ivy", "maven", "nix", "npm", "pacman",
			"raw", "rpm", "rubygems", "sbt", "terraform",
		})

	units := map[string]string{
		"pypi":   "name/1.0/name-1.0.whl",
		"helm":   "name@1.0",
		"cargo":  "name@1.0",
		"go":     "example.com/m@v1.0.0",
		"nuget":  "Id@1.0",
		"docker": "m:image@sha256:abc",
	}

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		key := units[format]
		if key == "" {
			key = "some/unit/key"
		}

		got := quarantineKeys(format, key)
		plain := quarantine.PathKeys(key)

		switch {
		case slices.Contains(handled, format) && len(got) <= len(plain):
			t.Errorf("quarantineKeys(%q, %q) = %v, want more spellings than %v", format, key, got, plain)
		case !slices.Contains(handled, format) && !slices.Equal(got, plain):
			t.Errorf("quarantineKeys(%q, %q) = %v, want the unit key spellings %v", format, key, got, plain)
		}
	})
}
