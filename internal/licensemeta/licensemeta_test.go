// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package licensemeta_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/licensemeta"
	"github.com/openartifact/openartifact/internal/licensemeta/archivetest"
)

type member = archivetest.Member

func file(name, body string) member { return archivetest.File(name, body) }

func buildZip(t *testing.T, members ...member) []byte {
	t.Helper()

	return archivetest.Zip(t, members...)
}

func buildTarGz(t *testing.T, members ...member) []byte {
	t.Helper()

	return archivetest.TarGz(t, members...)
}

func read(t *testing.T, format, filename string, data []byte) (licensemeta.Result, error) {
	t.Helper()

	return licensemeta.ReadBytes(context.Background(), format, filename, data)
}

func TestPyPIWheelPrecedence(t *testing.T) {
	t.Parallel()

	const (
		expr       = "Metadata-Version: 2.4\nName: demo\nVersion: 1.0\nLicense-Expression: MIT OR Apache-2.0\nLicense: BSD\nClassifier: License :: OSI Approved :: BSD License\n\nLicense: not a header\n"
		short      = "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nLicense: BSD\nClassifier: License :: OSI Approved :: BSD License\n"
		textual    = "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nLicense: Permission is hereby granted,\n        free of charge, to any person obtaining a copy\nClassifier: License :: OSI Approved :: MIT License\nClassifier: License :: OSI Approved\n"
		unknown    = "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nLicense: UNKNOWN\nClassifier: License :: OSI Approved :: Apache Software License\nClassifier: Programming Language :: Python\n"
		noLicenses = "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\n"
	)

	tests := []struct {
		name, metadata, source string
		want                   []string
	}{
		{"PEP 639 expression wins over License and classifiers", expr, "License-Expression", []string{"MIT OR Apache-2.0"}},
		{"short License wins over classifiers", short, "License", []string{"BSD"}},
		{"license text falls through to classifiers", textual, "classifier", []string{"License :: OSI Approved :: MIT License"}},
		{"UNKNOWN falls through to classifiers", unknown, "classifier", []string{"License :: OSI Approved :: Apache Software License"}},
		{"no license fields", noLicenses, "", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			wheel := buildZip(t,
				file("demo/__init__.py", ""),
				file("demo-1.0.dist-info/METADATA", tc.metadata),
				file("demo-1.0.dist-info/WHEEL", "Wheel-Version: 1.0\n"),
			)

			res, err := read(t, "pypi", "demo-1.0-py3-none-any.whl", wheel)
			noError(t, err)
			equal(t, tc.want, res.Licenses)
			equal(t, tc.source, res.Source)
		})
	}
}

func TestPyPISdists(t *testing.T) {
	t.Parallel()

	const pkgInfo = "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nLicense: MIT\n"

	t.Run("tar.gz", func(t *testing.T) {
		t.Parallel()

		data := buildTarGz(t,
			file("demo-1.0/setup.py", "from setuptools import setup\n"),
			file("demo-1.0/demo.egg-info/PKG-INFO", "Metadata-Version: 2.1\nLicense: GPL\n"),
			file("demo-1.0/PKG-INFO", pkgInfo),
		)

		res, err := read(t, "pypi", "demo-1.0.tar.gz", data)
		noError(t, err)
		equal(t, []string{"MIT"}, res.Licenses, "the top-level PKG-INFO, not the egg-info copy")
	})

	t.Run("zip", func(t *testing.T) {
		t.Parallel()

		res, err := read(t, "pypi", "demo-1.0.zip", buildZip(t, file("demo-1.0/PKG-INFO", pkgInfo)))
		noError(t, err)
		equal(t, []string{"MIT"}, res.Licenses)
	})
}

func TestNPMTarball(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, pkgJSON string
		want          []string
	}{
		{"license string", `{"name":"a","license":"MIT"}`, []string{"MIT"}},
		{"license object", `{"name":"a","license":{"type":"ISC","url":"x"}}`, []string{"ISC"}},
		{"legacy licenses array", `{"name":"a","licenses":[{"type":"MIT"},"Apache-2.0"]}`, []string{"MIT", "Apache-2.0"}},
		{"license supersedes licenses", `{"name":"a","license":"MIT","licenses":[{"type":"GPL-3.0"}]}`, []string{"MIT"}},
		{"none", `{"name":"a"}`, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data := buildTarGz(t,
				file("package/index.js", "module.exports = 1\n"),
				file("package/node_modules/dep/package.json", `{"license":"GPL-3.0-only"}`),
				file("package/package.json", tc.pkgJSON),
			)

			res, err := read(t, "npm", "a-1.0.0.tgz", data)
			noError(t, err)
			equal(t, tc.want, res.Licenses)
		})
	}
}

func TestCargoCrate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, toml string
		want       []string
	}{
		{"expression", "[package]\nname = \"c\"\nversion = \"1.0.0\"\nlicense = \"MIT OR Apache-2.0\"\n", []string{"MIT OR Apache-2.0"}},
		{"legacy slash", "[package]\nname = \"c\"\nlicense = \"MIT/Apache-2.0\"\n", []string{"MIT OR Apache-2.0"}},
		{"license-file only", "[package]\nname = \"c\"\nlicense-file = \"LICENSE\"\n", nil},
		{"no license", "[package]\nname = \"c\"\n", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data := buildTarGz(t,
				file("c-1.0.0/src/lib.rs", ""),
				file("c-1.0.0/Cargo.toml", tc.toml),
			)

			res, err := read(t, "cargo", "c-1.0.0.crate", data)
			noError(t, err)
			equal(t, tc.want, res.Licenses)
		})
	}

	t.Run("malformed Cargo.toml is an error, not a license", func(t *testing.T) {
		t.Parallel()

		_, err := read(t, "cargo", "c-1.0.0.crate", buildTarGz(t, file("c-1.0.0/Cargo.toml", "[package\nlicense = ")))
		hasError(t, err)
		isFalse(t, licensemeta.Unsafe(err))
	})
}

func TestNuGetPackage(t *testing.T) {
	t.Parallel()

	nuspec := func(license string) string {
		return `<?xml version="1.0"?><package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata>` +
			`<id>Demo</id><version>1.0.0</version>` + license + `</metadata></package>`
	}

	tests := []struct {
		name, nuspec string
		want         []string
	}{
		{"expression", nuspec(`<license type="expression">MIT</license>`), []string{"MIT"}},
		{"expression wins over licenseUrl", nuspec(`<license type="expression">MIT</license><licenseUrl>https://licenses.nuget.org/GPL-3.0-only</licenseUrl>`), []string{"MIT"}},
		{"nuget.org licenseUrl", nuspec(`<licenseUrl>https://licenses.nuget.org/Apache-2.0%20OR%20MIT</licenseUrl>`), []string{"Apache-2.0 OR MIT"}},
		{"canonical licenseUrl", nuspec(`<licenseUrl>http://www.apache.org/licenses/LICENSE-2.0.html</licenseUrl>`), []string{"Apache-2.0"}},
		{"unknown licenseUrl names no license", nuspec(`<licenseUrl>https://example.com/eula</licenseUrl>`), nil},
		{"file license", nuspec(`<license type="file">LICENSE.txt</license>`), nil},
		{"none", nuspec(``), nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			data := buildZip(t,
				file("lib/net8.0/Demo.dll", "MZ"),
				file("Demo.nuspec", tc.nuspec),
			)

			res, err := read(t, "nuget", "demo.1.0.0.nupkg", data)
			noError(t, err)
			equal(t, tc.want, res.Licenses)
		})
	}
}

func TestMavenPOM(t *testing.T) {
	t.Parallel()

	const pom = `<project><modelVersion>4.0.0</modelVersion><groupId>g</groupId><artifactId>a</artifactId><version>1</version>` +
		`<licenses><license><name>The Apache Software License, Version 2.0</name></license>` +
		`<license><url>https://opensource.org/licenses/MIT</url></license></licenses></project>`

	want := []string{"The Apache Software License, Version 2.0", "MIT"}

	t.Run("pom file", func(t *testing.T) {
		t.Parallel()

		res, err := read(t, "maven", "a-1.pom", []byte(pom))
		noError(t, err)
		equal(t, want, res.Licenses)
	})

	t.Run("pom embedded in a jar", func(t *testing.T) {
		t.Parallel()

		jar := buildZip(t, file("META-INF/MANIFEST.MF", "Manifest-Version: 1.0\n"), file("META-INF/maven/g/a/pom.xml", pom))

		res, err := read(t, "maven", "a-1.jar", jar)
		noError(t, err)
		equal(t, want, res.Licenses)
	})
}

func TestUnsupportedAndEmpty(t *testing.T) {
	t.Parallel()

	_, err := read(t, "pypi", "demo-1.0.exe", []byte("MZ"))
	errorIs(t, err, licensemeta.ErrUnsupported)

	_, err = read(t, "docker", "layer.tar", nil)
	errorIs(t, err, licensemeta.ErrUnsupported)

	_, err = read(t, "pypi", "demo-1.0-py3-none-any.whl", buildZip(t, file("demo/__init__.py", "")))
	errorIs(t, err, licensemeta.ErrNoMetadata)

	_, err = read(t, "pypi", "demo-1.0-py3-none-any.whl", []byte("not a zip"))
	hasError(t, err)
	isFalse(t, licensemeta.Unsafe(err), "a file that is not an archive is unreadable, not unsafe")
}

func TestHostileArchives(t *testing.T) {
	t.Parallel()

	const metadata = "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nLicense: MIT\n"

	t.Run("path traversal and absolute member names never match", func(t *testing.T) {
		t.Parallel()

		wheel := buildZip(t,
			file("../evil-1.0.dist-info/METADATA", metadata),
			file("/abs-1.0.dist-info/METADATA", metadata),
			file("a/b/evil-1.0.dist-info/METADATA", metadata),
			file("..\\win-1.0.dist-info\\METADATA", metadata),
		)

		_, err := read(t, "pypi", "demo-1.0-py3-none-any.whl", wheel)
		errorIs(t, err, licensemeta.ErrNoMetadata)

		tgz := buildTarGz(t,
			file("../package/package.json", `{"license":"MIT"}`),
			file("/package/package.json", `{"license":"MIT"}`),
		)

		_, err = read(t, "npm", "a-1.0.0.tgz", tgz)
		errorIs(t, err, licensemeta.ErrNoMetadata)
	})

	t.Run("two METADATA files are ambiguous", func(t *testing.T) {
		t.Parallel()

		wheel := buildZip(t,
			file("demo-1.0.dist-info/METADATA", metadata),
			file("other-2.0.dist-info/METADATA", strings.Replace(metadata, "MIT", "GPL-3.0-only", 1)),
		)

		_, err := read(t, "pypi", "demo-1.0-py3-none-any.whl", wheel)
		errorIs(t, err, licensemeta.ErrUnsafe)
	})

	t.Run("two package.json files are ambiguous", func(t *testing.T) {
		t.Parallel()

		tgz := buildTarGz(t,
			file("package/package.json", `{"license":"MIT"}`),
			file("other/package.json", `{"license":"GPL-3.0-only"}`),
		)

		_, err := read(t, "npm", "a-1.0.0.tgz", tgz)
		errorIs(t, err, licensemeta.ErrUnsafe)
	})

	t.Run("two nuspec files are ambiguous", func(t *testing.T) {
		t.Parallel()

		_, err := read(t, "nuget", "d.1.0.0.nupkg", buildZip(t, file("a.nuspec", "<package/>"), file("b.nuspec", "<package/>")))
		errorIs(t, err, licensemeta.ErrUnsafe)
	})

	t.Run("a metadata file over the limit is refused", func(t *testing.T) {
		t.Parallel()

		big := "{\"license\":\"MIT\",\"padding\":\"" + strings.Repeat("a", 2<<20) + "\"}"

		_, err := read(t, "npm", "a-1.0.0.tgz", buildTarGz(t, file("package/package.json", big)))
		errorIs(t, err, licensemeta.ErrUnsafe)

		wheel := buildZip(t, file("demo-1.0.dist-info/METADATA", metadata+"Description: "+strings.Repeat("a", 2<<20)+"\n"))

		_, err = read(t, "pypi", "demo-1.0-py3-none-any.whl", wheel)
		errorIs(t, err, licensemeta.ErrUnsafe, "headers padded past the limit")
	})

	t.Run("a zip bomb member is refused without being inflated", func(t *testing.T) {
		t.Parallel()

		// 64 MiB of zeros deflates to ~64 KiB: far over the member limit.
		wheel := buildZip(t, file("demo-1.0.dist-info/METADATA", metadata+"\n"+strings.Repeat("\x00", 64<<20)))

		res, err := read(t, "pypi", "demo-1.0-py3-none-any.whl", wheel)
		noError(t, err, "only the header block is read; the body is never inflated past it")
		equal(t, []string{"MIT"}, res.Licenses)

		bomb := buildZip(t, file("demo-1.0.dist-info/METADATA", strings.Repeat("A", 64<<20)))

		_, err = read(t, "pypi", "demo-1.0-py3-none-any.whl", bomb)
		errorIs(t, err, licensemeta.ErrUnsafe)
	})

	t.Run("bulk data ahead of package.json is skipped, not stored", func(t *testing.T) {
		t.Parallel()

		tgz := buildTarGz(t,
			file("package/data.bin", strings.Repeat("\x00", 8<<20)),
			file("package/package.json", `{"license":"MIT"}`),
		)

		res, err := read(t, "npm", "a-1.0.0.tgz", tgz)
		noError(t, err)
		equal(t, []string{"MIT"}, res.Licenses, "the 8 MiB member is streamed past, not kept")
	})
}

func TestReadHonoursContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	data := buildTarGz(t, file("package/package.json", `{"license":"MIT"}`))

	_, err := licensemeta.ReadBytes(ctx, "npm", "a-1.0.0.tgz", data)
	hasError(t, err)
	errorIs(t, err, context.Canceled)
}

func TestParsePackageJSONFallback(t *testing.T) {
	t.Parallel()

	equal(t, []string{"MIT"}, licensemeta.ParsePackageJSON([]byte(`{"license":"MIT"}`)))
	equal(t, []string(nil), licensemeta.ParsePackageJSON([]byte(`not json`)))
}

func noError(t *testing.T, err error, _ ...string) {
	t.Helper()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func hasError(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected an error")
	}
}

func errorIs(t *testing.T, err, target error, _ ...string) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

func equal(t *testing.T, want, got any, msg ...string) {
	t.Helper()

	if !reflect.DeepEqual(want, got) {
		t.Errorf("got %#v, want %#v %s", got, want, strings.Join(msg, " "))
	}
}

func isFalse(t *testing.T, v bool, msg ...string) {
	t.Helper()

	if v {
		t.Errorf("expected false: %s", strings.Join(msg, " "))
	}
}

func TestExpected(t *testing.T) {
	tests := []struct {
		format, filename string
		want             bool
	}{
		{"pypi", "demo-1.0-py3-none-any.whl", true},
		{"pypi", "demo-1.0.egg", true},
		{"npm", "demo-1.0.0.tgz", true},
		{"cargo", "demo-0.1.0.crate", true},
		{"nuget", "Demo.1.0.0.nupkg", true},
		{"rubygems", "demo-1.0.0.gem", true},
		{"maven", "demo-1.0.0.POM", true},
		{"sbt", "demo_2.13-1.0.0.pom", true},
		{"maven", "demo-1.0.0.jar", false},
		{"maven", "demo-1.0.0-sources.jar", false},
		{"maven", "demo-1.0.0.pom.sha1", false},
		{"maven", "maven-metadata.xml", false},
		{"raw", "notes.txt", false},
		{"docker", "sha256:abc", false},
	}

	for _, tc := range tests {
		equal(t, tc.want, licensemeta.Expected(tc.format, tc.filename), tc.format+" "+tc.filename)
	}
}
