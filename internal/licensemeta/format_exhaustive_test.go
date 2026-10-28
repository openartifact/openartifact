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
	"cargo": "x-1.0.crate", "ivy": "ivy.xml", "maven": "x-1.0.pom", "npm": "x-1.0.tgz", "nuget": "x.1.0.nupkg",
	"pypi": "x-1.0-py3-none-any.whl", "rubygems": "x-1.0.gem", "sbt": "x-1.0.pom",
}

// supported are the formats whose licenses are read from the package file.
//
//nolint:gochecknoglobals // immutable test table
var supported = []string{"cargo", "ivy", "maven", "npm", "nuget", "pypi", "rubygems", "sbt"}

// notSupported are the formats whose licenses are not read: they have no
// license metadata of their own that a policy could match.
//
//nolint:gochecknoglobals // immutable test table
var notSupported = []string{"ansible", "apk", "conan", "conda", "deb", "docker", "go", "helm", "nix", "pacman", "raw", "rpm", "terraform"}

func TestSupportedCoversEveryFormat(t *testing.T) {
	t.Parallel()

	handled := repotypetest.Classified(t, "licensemeta.Supported", supported, notSupported)

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

	handled := repotypetest.Classified(t, "licensemeta.Expected", supported, notSupported)

	repotypetest.Each(t, func(t *testing.T, format string) {
		t.Helper()

		t.Parallel()

		name := licenseFile[format]
		if name == "" {
			name = "x-1.0.pom"
		}

		got, want := Expected(format, name), slices.Contains(handled, format)
		if got != want {
			t.Errorf("Expected(%q, %q) = %v, want %v", format, name, got, want)
		}

		// A package the policy expects a license from must be one it can read.
		if got && !Supported(format, name) {
			t.Errorf("Expected(%q, %q) but not Supported: the scan could never read it", format, name)
		}
	})
}

// TestDescriptorFormatsExpectOnlyTheDescriptor: ivy, maven and sbt modules hold
// several files; only the descriptor declares the license.
func TestDescriptorFormatsExpectOnlyTheDescriptor(t *testing.T) {
	t.Parallel()

	for format, others := range map[string][]string{
		"ivy":   {"x-1.0.jar", "ivy.xml.sha1", "ivy-1.0.xml.md5", "ivysettings.xml", "xivy.xml", "x-1.0.xml"},
		"maven": {"x-1.0.jar", "x-1.0.pom.sha1", "maven-metadata.xml"},
		"sbt":   {"x_2.13-1.0.jar", "ivy.xml"},
	} {
		for _, name := range others {
			if Expected(format, name) {
				t.Errorf("Expected(%q, %q) = true: it is not the descriptor", format, name)
			}

			if format == "ivy" && Supported(format, name) {
				t.Errorf("Supported(%q, %q) = true: it is not the descriptor", format, name)
			}
		}
	}
}

func TestParseCoversEveryFormat(t *testing.T) {
	t.Parallel()

	// rubygems is read by readGem before the manifest extraction, so parse never sees it.
	handled := repotypetest.Classified(t, "licensemeta.parse", []string{"cargo", "ivy", "maven", "npm", "nuget", "pypi", "sbt"},
		append([]string{"rubygems"}, notSupported...))

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
// the descriptor size limit (ivy, maven, sbt) and the gem reader (rubygems)
// live inline in Read and are classified only; the extractor name is probed.
func TestReadFormatPlumbing(t *testing.T) {
	t.Parallel()

	others := []string{"ansible", "apk", "cargo", "conan", "conda", "deb", "docker", "go", "helm", "nix", "npm", "nuget",
		"pacman", "pypi", "raw", "rpm", "terraform"}

	repotypetest.Classified(t, "licensemeta.Read limit", []string{"ivy", "maven", "sbt"}, append([]string{"rubygems"}, others...))
	repotypetest.Classified(t, "licensemeta.Read gem", []string{"rubygems"}, append([]string{"ivy", "maven", "sbt"}, others...))

	// maven is already the extractor's name, as are ivy and the others, so only sbt is mapped.
	handled := repotypetest.Classified(t, "licensemeta.jvmFormat", []string{"sbt"}, append([]string{"ivy", "maven", "rubygems"}, others...))

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
