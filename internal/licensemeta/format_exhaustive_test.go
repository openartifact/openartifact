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
	"sbt": "x-1.0.pom", "rubygems": "x-1.0.gem", "ivy": "ivy.xml",
}

// supported are the formats whose licenses are read from the package file.
//
//nolint:gochecknoglobals // immutable test table
var supported = []string{"pypi", "npm", "cargo", "nuget", "maven", "sbt", "rubygems", "ivy"}

// notSupported are the formats whose licenses are not read: they have no
// license metadata of their own that a policy could match.
//
//nolint:gochecknoglobals // immutable test table
var notSupported = []string{"docker", "helm", "deb", "rpm", "go", "raw", "conda", "conan", "terraform", "apk", "pacman"}

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

// TestDescriptorFormatsExpectOnlyTheDescriptor: maven, sbt and ivy modules hold
// several files; only the descriptor declares the license.
func TestDescriptorFormatsExpectOnlyTheDescriptor(t *testing.T) {
	t.Parallel()

	for format, others := range map[string][]string{
		"maven": {"x-1.0.jar", "x-1.0.pom.sha1", "maven-metadata.xml"},
		"sbt":   {"x_2.13-1.0.jar", "ivy.xml"},
		"ivy":   {"x-1.0.jar", "ivy.xml.sha1", "ivy-1.0.xml.md5", "ivysettings.xml", "xivy.xml", "x-1.0.xml"},
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
	handled := repotypetest.Classified(t, "licensemeta.parse", []string{"pypi", "npm", "cargo", "nuget", "maven", "sbt", "ivy"},
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
// the descriptor size limit (maven, sbt, ivy) and the gem reader (rubygems)
// live inline in Read and are classified only; the extractor name is probed.
func TestReadFormatPlumbing(t *testing.T) {
	t.Parallel()

	others := []string{"docker", "npm", "pypi", "helm", "cargo", "nuget", "go", "raw", "conda", "conan", "terraform",
		"deb", "rpm", "apk", "pacman"}

	repotypetest.Classified(t, "licensemeta.Read limit", []string{"maven", "sbt", "ivy"}, append([]string{"rubygems"}, others...))
	repotypetest.Classified(t, "licensemeta.Read gem", []string{"rubygems"}, append([]string{"maven", "sbt", "ivy"}, others...))

	// maven is already the extractor's name, as are ivy and the others, so only sbt is mapped.
	handled := repotypetest.Classified(t, "licensemeta.jvmFormat", []string{"sbt"}, append([]string{"maven", "ivy", "rubygems"}, others...))

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
