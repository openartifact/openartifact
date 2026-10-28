// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"errors"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

func TestParseCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(
		t,
		"pkgdeps.parse",
		[]string{
			"npm",
			"pypi",
			"maven",
			"ivy",
			"sbt",
			"nuget",
			"cargo",
			"helm",
			"deb",
			"rpm",
			"pacman",
			"go",
			"rubygems",
			"conda",
			"conan",
		},
		// No dependency manifest is read for these.
		[]string{"docker", "apk", "raw", "terraform", "nix"},
	)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		// Parts without content: a handled parser may reject them, but never as unsupported.
		_, err := parse(format, "x", nil)

		if got, want := errors.Is(err, ErrUnsupported), !slices.Contains(handled, format); got != want {
			t.Errorf("parse(%q) unsupported = %v (err %v), want %v", format, got, err, want)
		}
	})
}
