// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta

import (
	"errors"
	"slices"
	"testing"

	"github.com/openartifact/openartifact/internal/ai/manifest"
	"github.com/openartifact/openartifact/internal/repotype/repotypetest"
)

// licenseFile is a file name of each supported format that Supported accepts.
//
//nolint:gochecknoglobals // immutable test table
var licenseFile = map[string]string{
	"pypi": "x-1.0-py3-none-any.whl", "npm": "x-1.0.tgz", "cargo": "x-1.0.crate", "nuget": "x.1.0.nupkg", "maven": "x-1.0.pom",
	"sbt": "x-1.0.pom",
}

// notSupported are the formats whose licenses are not read: they have no
// license metadata here (or, for rubygems and ivy, no reader yet).
//
//nolint:gochecknoglobals // immutable test table
var notSupported = []string{"docker", "helm", "deb", "rpm", "rubygems", "go", "raw", "conda", "conan", "ivy", "terraform", "apk", "pacman"}

func TestSupportedCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "licensemeta.Supported", []string{"pypi", "npm", "cargo", "nuget", "maven", "sbt"}, notSupported)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		name := licenseFile[format]
		if name == "" {
			name = "x-1.0.pom"
		}

		if got, want := Supported(format, name), slices.Contains(handled, format); got != want {
			t.Errorf("Supported(%q, %q) = %v, want %v", format, name, got, want)
		}
	})
}

func TestExpectedCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "licensemeta.Expected",
		[]string{"pypi", "npm", "cargo", "nuget", "rubygems", "maven", "sbt"},
		[]string{"docker", "helm", "deb", "rpm", "go", "raw", "conda", "conan", "ivy", "terraform", "apk", "pacman"})

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		// A .pom is the only file name every handled format accepts: maven/sbt expect nothing else.
		if got, want := Expected(format, "x-1.0.pom"), slices.Contains(handled, format); got != want {
			t.Errorf("Expected(%q, .pom) = %v, want %v", format, got, want)
		}

		if format == "maven" || format == "sbt" {
			if Expected(format, "x-1.0.jar") {
				t.Errorf("Expected(%q, .jar) = true: the pom is the authority", format)
			}
		}
	})
}

func TestParseCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "licensemeta.parse", []string{"pypi", "npm", "cargo", "nuget", "maven", "sbt"}, notSupported)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		// Empty metadata: a handled parser may reject it, but never as unsupported.
		_, err := parse(format, "x-1.0.pom", []manifest.Part{{Body: ""}})

		if got, want := errors.Is(err, ErrUnsupported), !slices.Contains(handled, format); got != want {
			t.Errorf("parse(%q) unsupported = %v (err %v), want %v", format, got, err, want)
		}
	})
}

// TestReadFormatPlumbing covers what Read does per format before parsing:
// the POM size limit (maven, sbt only) lives inline in Read and is classified
// only; the extractor name is probed.
func TestReadFormatPlumbing(t *testing.T) {
	t.Parallel()

	others := []string{"docker", "npm", "pypi", "helm", "cargo", "rubygems", "nuget", "go", "raw", "conda", "conan", "ivy", "terraform",
		"deb", "rpm", "apk", "pacman"}

	repotypetest.Classified(t, "licensemeta.Read limit", []string{"maven", "sbt"}, others)

	// maven is already the extractor's name, so only sbt is mapped.
	handled := repotypetest.Classified(t, "licensemeta.jvmFormat", []string{"sbt"}, append([]string{"maven"}, others...))

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		want := format
		if slices.Contains(handled, format) {
			want = "maven"
		}

		if got := jvmFormat(format); got != want {
			t.Errorf("jvmFormat(%q) = %q, want %q", format, got, want)
		}
	})
}
