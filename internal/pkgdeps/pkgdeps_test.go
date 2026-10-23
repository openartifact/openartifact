// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pkgdeps

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/openartifact/openartifact/internal/licensemeta/archivetest"
)

func memSource(format, filename string, data []byte) Source {
	return Source{
		Format:   format,
		Filename: filename,
		Open:     func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
	}
}

// byName indexes dependencies; the test fails on a duplicate name.
func byName(t *testing.T, deps []Dep) map[string]Dep {
	t.Helper()

	m := make(map[string]Dep, len(deps))

	for _, d := range deps {
		if _, dup := m[d.Name]; dup {
			t.Fatalf("duplicate dependency %q in %+v", d.Name, deps)
		}

		m[d.Name] = d
	}

	return m
}

func want(t *testing.T, deps map[string]Dep, name, version, rng string) {
	t.Helper()

	d, ok := deps[name]
	if !ok {
		t.Fatalf("dependency %q missing from %+v", name, deps)
	}

	if d.Version != version || d.Range != rng {
		t.Errorf("%s = version %q range %q, want version %q range %q", name, d.Version, d.Range, version, rng)
	}
}

func read(t *testing.T, format, filename string, data []byte) Result {
	t.Helper()

	res, err := Read(context.Background(), memSource(format, filename, data))
	if err != nil {
		t.Fatalf("Read(%s %s): %v", format, filename, err)
	}

	return res
}

func TestNPMPackageJSONAndShrinkwrap(t *testing.T) {
	t.Parallel()

	pkgJSON := `{"name":"demo","version":"1.0.0",
		"dependencies":{"left-pad":"1.3.0","lodash":"^4.17.0","@babel/core":"~7.1.0","gitdep":"github:a/b","aliased":"npm:real@^2.0.0"},
		"optionalDependencies":{"fsevents":"2.3.2"},
		"devDependencies":{"jest":"^29"}}`

	t.Run("manifest only", func(t *testing.T) {
		t.Parallel()

		res := read(t, "npm", "demo-1.0.0.tgz", archivetest.TarGz(t, archivetest.File("package/package.json", pkgJSON)))
		deps := byName(t, res.Deps)

		want(t, deps, "left-pad", "1.3.0", "")
		want(t, deps, "lodash", "", "^4.17.0")
		want(t, deps, "@babel/core", "", "~7.1.0")
		want(t, deps, "fsevents", "2.3.2", "")
		want(t, deps, "real", "", "^2.0.0")
		want(t, deps, "gitdep", "", "")

		if _, ok := deps["jest"]; ok {
			t.Error("devDependencies must not be listed")
		}
	})

	t.Run("shrinkwrap pins versions and adds transitive packages", func(t *testing.T) {
		t.Parallel()

		lock := `{"lockfileVersion":3,"packages":{
			"":{"name":"demo","version":"1.0.0"},
			"node_modules/lodash":{"version":"4.17.21","license":"MIT"},
			"node_modules/lodash/node_modules/@scope/inner":{"version":"1.2.3"},
			"node_modules/jest":{"version":"29.0.0","dev":true},
			"node_modules/left-pad":{"version":"1.3.0"}}}`

		res := read(t, "npm", "demo-1.0.0.tgz", archivetest.TarGz(t,
			archivetest.File("package/package.json", pkgJSON),
			archivetest.File("package/npm-shrinkwrap.json", lock)))
		deps := byName(t, res.Deps)

		want(t, deps, "lodash", "4.17.21", "^4.17.0")
		want(t, deps, "@scope/inner", "1.2.3", "")
		want(t, deps, "left-pad", "1.3.0", "")
		want(t, deps, "@babel/core", "", "~7.1.0") // not locked: manifest entry stays

		if deps["lodash"].License != "MIT" {
			t.Errorf("lodash license = %q", deps["lodash"].License)
		}

		if _, ok := deps["jest"]; ok {
			t.Error("dev-only lockfile entry must not be listed")
		}
	})

	t.Run("lockfile v1", func(t *testing.T) {
		t.Parallel()

		lock := `{"lockfileVersion":1,"dependencies":{"a":{"version":"1.0.0","dependencies":{"b":{"version":"2.0.0"}}},"d":{"version":"3.0.0","dev":true}}}`
		res := read(t, "npm", "demo-1.0.0.tgz", archivetest.TarGz(t,
			archivetest.File("package/package.json", `{"name":"demo"}`),
			archivetest.File("package/package-lock.json", lock)))
		deps := byName(t, res.Deps)

		want(t, deps, "a", "1.0.0", "")
		want(t, deps, "b", "2.0.0", "")

		if _, ok := deps["d"]; ok {
			t.Error("dev entry listed")
		}
	})
}

func TestPyPIRequiresDist(t *testing.T) {
	t.Parallel()

	meta := "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\n" +
		"Requires-Dist: requests (>=2.0)\n" +
		"Requires-Dist: Flask_Login==0.6.3\n" +
		"Requires-Dist: colorama; sys_platform == 'win32'\n" +
		"Requires-Dist: pytest ; extra == 'test'\n" +
		"Requires-Dist: numpy[extra1]>=1.20,\n  <2\n" +
		"\nRequires-Dist: not-a-header-in-the-body\n"

	res := read(t, "pypi", "demo-1.0-py3-none-any.whl",
		archivetest.Zip(t, archivetest.File("demo-1.0.dist-info/METADATA", meta)))
	deps := byName(t, res.Deps)

	want(t, deps, "requests", "", ">=2.0")
	want(t, deps, "Flask_Login", "0.6.3", "")
	want(t, deps, "colorama", "", "")
	want(t, deps, "numpy", "", ">=1.20, <2")

	for _, n := range []string{"pytest", "not-a-header-in-the-body"} {
		if _, ok := deps[n]; ok {
			t.Errorf("%s must not be listed", n)
		}
	}

	// sdists carry the same headers in PKG-INFO.
	res = read(t, "pypi", "demo-1.0.tar.gz", archivetest.TarGz(t, archivetest.File("demo-1.0/PKG-INFO", meta)))
	want(t, byName(t, res.Deps), "requests", "", ">=2.0")
}

func TestMavenPOMAndJar(t *testing.T) {
	t.Parallel()

	pom := `<project><modelVersion>4.0.0</modelVersion>
		<parent><groupId>org.demo</groupId><artifactId>parent</artifactId><version>2.0</version></parent>
		<artifactId>app</artifactId>
		<properties><guava.version>32.1.2</guava.version><nested>${guava.version}</nested></properties>
		<dependencyManagement><dependencies>
			<dependency><groupId>org.managed</groupId><artifactId>lib</artifactId><version>7.7</version></dependency>
		</dependencies></dependencyManagement>
		<dependencies>
			<dependency><groupId>com.google.guava</groupId><artifactId>guava</artifactId><version>${nested}</version></dependency>
			<dependency><groupId>org.managed</groupId><artifactId>lib</artifactId></dependency>
			<dependency><groupId>org.demo</groupId><artifactId>sibling</artifactId><version>${project.version}</version></dependency>
			<dependency><groupId>org.range</groupId><artifactId>r</artifactId><version>[1.0,2.0)</version></dependency>
			<dependency><groupId>org.unknown</groupId><artifactId>u</artifactId><version>${missing}</version></dependency>
			<dependency><groupId>junit</groupId><artifactId>junit</artifactId><version>4.13</version><scope>test</scope></dependency>
			<dependency><groupId>javax</groupId><artifactId>servlet</artifactId><version>3</version><scope>provided</scope></dependency>
		</dependencies></project>`

	check := func(t *testing.T, res Result) {
		t.Helper()

		deps := byName(t, res.Deps)
		want(t, deps, "com.google.guava:guava", "32.1.2", "")
		want(t, deps, "org.managed:lib", "7.7", "")
		want(t, deps, "org.demo:sibling", "2.0", "")
		want(t, deps, "org.range:r", "", "[1.0,2.0)")
		want(t, deps, "org.unknown:u", "", "")

		for _, n := range []string{"junit:junit", "javax:servlet"} {
			if _, ok := deps[n]; ok {
				t.Errorf("%s must not be listed (test/provided)", n)
			}
		}
	}

	t.Run("pom", func(t *testing.T) { t.Parallel(); check(t, read(t, "maven", "app-2.0.pom", []byte(pom))) })
	t.Run("sbt pom", func(t *testing.T) { t.Parallel(); check(t, read(t, "sbt", "app-2.0.pom", []byte(pom))) })
	t.Run("jar embeds its pom", func(t *testing.T) {
		t.Parallel()

		jar := archivetest.Zip(t,
			archivetest.File("META-INF/MANIFEST.MF", "Manifest-Version: 1.0\n"),
			archivetest.File("META-INF/maven/org.demo/app/pom.xml", pom))
		check(t, read(t, "maven", "app-2.0.jar", jar))
	})
}

func TestIvyXML(t *testing.T) {
	t.Parallel()

	ivy := `<ivy-module version="2.0"><info organisation="o" module="m"/><dependencies>
		<dependency org="org.slf4j" name="slf4j-api" rev="1.7.36" conf="compile->default"/>
		<dependency org="org.r" name="r" rev="latest.integration"/>
		<dependency org="org.k" name="k" rev="[1.0,2.0)"/></dependencies></ivy-module>`

	res := read(t, "ivy", "ivy.xml", []byte(ivy))
	deps := byName(t, res.Deps)

	want(t, deps, "org.slf4j:slf4j-api", "1.7.36", "")
	want(t, deps, "org.k:k", "", "[1.0,2.0)")
	// ivy revisions without range syntax are versions or dynamic revisions; both are kept verbatim.
	want(t, deps, "org.r:r", "latest.integration", "")
}

func TestCargoTomlAndLock(t *testing.T) {
	t.Parallel()

	toml := `[package]
name = "demo"
version = "0.1.0"

[dependencies]
serde = "1.0"
exact = "=2.0.1"
renamed = { package = "real-name", version = "0.3" }
local = { path = "../local" }

[build-dependencies]
cc = "1"

[target.'cfg(windows)'.dependencies]
winapi = "0.3"

[dev-dependencies]
criterion = "0.5"
`
	lock := "version = 3\n\n[[package]]\nname = \"demo\"\nversion = \"0.1.0\"\n\n[[package]]\nname = \"serde\"\nversion = \"1.0.188\"\n\n[[package]]\nname = \"serde_derive\"\nversion = \"1.0.188\"\n"

	res := read(t, "cargo", "demo-0.1.0.crate", archivetest.TarGz(t,
		archivetest.File("demo-0.1.0/Cargo.toml", toml),
		archivetest.File("demo-0.1.0/Cargo.lock", lock)))
	deps := byName(t, res.Deps)

	want(t, deps, "serde", "1.0.188", "1.0")
	want(t, deps, "serde_derive", "1.0.188", "")
	want(t, deps, "exact", "2.0.1", "")
	want(t, deps, "real-name", "", "0.3")
	want(t, deps, "cc", "", "1")
	want(t, deps, "winapi", "", "0.3")

	for _, n := range []string{"criterion", "demo"} {
		if _, ok := deps[n]; ok {
			t.Errorf("%s must not be listed", n)
		}
	}
}

func TestGoModAndZip(t *testing.T) {
	t.Parallel()

	mod := "module example.com/demo\n\ngo 1.22\n\nrequire (\n\tgolang.org/x/text v0.3.0\n\tgithub.com/pkg/errors v0.9.1 // indirect\n)\n"

	res := read(t, "go", "v1.0.0.mod", []byte(mod))
	deps := byName(t, res.Deps)
	want(t, deps, "golang.org/x/text", "v0.3.0", "")
	want(t, deps, "github.com/pkg/errors", "v0.9.1", "")

	res = read(t, "go", "v1.0.0.zip", archivetest.Zip(t,
		archivetest.File("example.com/demo@v1.0.0/main.go", "package main"),
		archivetest.File("example.com/demo@v1.0.0/go.mod", mod)))
	want(t, byName(t, res.Deps), "golang.org/x/text", "v0.3.0", "")
}

func TestNuGetNuspec(t *testing.T) {
	t.Parallel()

	nuspec := `<?xml version="1.0"?><package xmlns="http://schemas.microsoft.com/packaging/2013/05/nuspec.xsd"><metadata>
		<id>Demo</id><version>1.0.0</version>
		<dependencies>
			<group targetFramework="net6.0">
				<dependency id="Newtonsoft.Json" version="13.0.1"/>
				<dependency id="Pinned" version="[2.0.0]"/>
			</group>
			<group targetFramework="net48"><dependency id="Newtonsoft.Json" version="12.0.0"/></group>
			<dependency id="Flat" version="[1.0,2.0)"/>
		</dependencies></metadata></package>`

	res := read(t, "nuget", "demo.1.0.0.nupkg", archivetest.Zip(t, archivetest.File("Demo.nuspec", nuspec)))
	deps := byName(t, res.Deps)

	want(t, deps, "Newtonsoft.Json", "", "13.0.1")
	want(t, deps, "Pinned", "2.0.0", "")
	want(t, deps, "Flat", "", "[1.0,2.0)")
}

func TestRubyGemspecDependencies(t *testing.T) {
	t.Parallel()

	spec := `--- !ruby/object:Gem::Specification
name: demo-gem
version: !ruby/object:Gem::Version
  version: 0.9.0
dependencies:
- !ruby/object:Gem::Dependency
  name: rack
  requirement: !ruby/object:Gem::Requirement
    requirements:
    - - ">="
      - !ruby/object:Gem::Version
        version: '2.0'
    - - "<"
      - !ruby/object:Gem::Version
        version: '4'
  type: :runtime
- !ruby/object:Gem::Dependency
  name: pinned
  requirement: !ruby/object:Gem::Requirement
    requirements:
    - - "="
      - !ruby/object:Gem::Version
        version: 1.2.3
  type: :runtime
- !ruby/object:Gem::Dependency
  name: rspec
  requirement: !ruby/object:Gem::Requirement
    requirements:
    - - ">="
      - !ruby/object:Gem::Version
        version: '0'
  type: :development
`

	res := read(t, "rubygems", "demo-gem-0.9.0.gem", archivetest.Gem(t, spec))
	deps := byName(t, res.Deps)

	want(t, deps, "rack", "", ">= 2.0, < 4")
	want(t, deps, "pinned", "1.2.3", "")

	if _, ok := deps["rspec"]; ok {
		t.Error("development dependency listed")
	}
}

func TestHelmChartAndLock(t *testing.T) {
	t.Parallel()

	chart := "apiVersion: v2\nname: demo\nversion: 0.2.0\ndependencies:\n- name: redis\n  version: ~17.0.0\n  repository: https://charts.example\n- name: pg\n  version: 12.1.0\n"
	req := "dependencies:\n- name: legacy\n  version: \">=1.0.0\"\n"
	lock := "dependencies:\n- name: redis\n  version: 17.0.9\n  repository: https://charts.example\n"

	res := read(t, "helm", "demo-0.2.0.tgz", archivetest.TarGz(t,
		archivetest.File("demo/Chart.yaml", chart),
		archivetest.File("demo/requirements.yaml", req),
		archivetest.File("demo/Chart.lock", lock),
		archivetest.File("demo/charts/sub/Chart.yaml", "name: sub\ndependencies:\n- name: hidden\n")))
	deps := byName(t, res.Deps)

	want(t, deps, "redis", "17.0.9", "~17.0.0")
	want(t, deps, "pg", "12.1.0", "")
	want(t, deps, "legacy", "", ">=1.0.0")

	if _, ok := deps["hidden"]; ok {
		t.Error("subchart dependency listed")
	}
}

// condaBz2 is the fixture of internal/ai/manifest: info/index.json depends on
// "python >=3.9".
const condaBz2 = "QlpoOTFBWSZTWfQNIXEAAOxfosyAUAf/syAiNAr//99qCCABAAgIMAD6sRW1U9QeoDTaJoZNAyZpqYEyYgBoiaGU8ppo00A0AANAaaaNAyU00Rppppo9IBoNA0PUADQ0aOMJqKGmdQBLjwJBDol6lhgBRjIi+axIIQAt5JAAAMcmDscO2ZEh20X4TLFmI3IEXpQwKtfJYZ67UvUriY5OjbJlMyAANwlSgSeDUdJMYHSTW+UFFCk36gOJLUbCQ5SFQMIpuUaZzYU7yV4FFoF+mjY/MVNA3tQh3elYS/mNI9XWCC0UVCKokW2CJoUayqf+fbOIOkirhsR2dzuBJSAowt4wbYC+jakGpwvX4YZLDSeOWOX3JWILhLjQhX4hkjX+LK8EyIKoo3AsZyCB/F3JFOFCQ9A0hcQ="

func TestCondaIndexDepends(t *testing.T) {
	t.Parallel()

	bz, err := base64.StdEncoding.DecodeString(condaBz2)
	if err != nil {
		t.Fatal(err)
	}

	res := read(t, "conda", "demo-conda-1.2.3-h0_0.tar.bz2", bz)
	want(t, byName(t, res.Deps), "python", "", ">=3.9")

	// .conda: a zip holding info-<pkg>.tar.zst.
	index := `{"name":"demo","depends":["python >=3.9","numpy ==1.26.4","zlib","openssl 3.*"]}`
	tarData := archivetestTar(t, "info/index.json", index)

	var zbuf bytes.Buffer

	zw, err := zstd.NewWriter(&zbuf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := zw.Write(tarData); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	res = read(t, "conda", "demo-1.2.3-0.conda", archivetest.Zip(t,
		archivetest.File("metadata.json", "{}"),
		archivetest.File("info-demo-1.2.3-0.tar.zst", zbuf.String())))
	deps := byName(t, res.Deps)

	want(t, deps, "python", "", ">=3.9")
	want(t, deps, "numpy", "1.26.4", "")
	want(t, deps, "zlib", "", "")
	want(t, deps, "openssl", "", "3.*")
}

func archivetestTar(t *testing.T, name, body string) []byte {
	t.Helper()

	gz := archivetest.TarGz(t, archivetest.File(name, body))

	return ungzip(t, gz)
}

func TestDebDepends(t *testing.T) {
	t.Parallel()

	control := "Package: demo\nVersion: 1.0-1ubuntu2\nArchitecture: amd64\nMaintainer: A <a@ubuntu.com>\n" +
		"Pre-Depends: dpkg (>= 1.16)\n" +
		"Depends: libc6 (>= 2.31), libssl3:any (= 3.0.2-0ubuntu1) | libssl1.1,\n zlib1g, ${misc:Depends}, foo [amd64]\n" +
		"Recommends: ignored-pkg\nDescription: a demo\n"

	res := read(t, "deb", "demo_1.0_amd64.deb", debBytes(t, control))
	deps := byName(t, res.Deps)

	if res.Namespace != "ubuntu" {
		t.Errorf("namespace = %q, want ubuntu", res.Namespace)
	}

	want(t, deps, "dpkg", "", ">= 1.16")
	want(t, deps, "libc6", "", ">= 2.31")
	want(t, deps, "libssl3", "3.0.2-0ubuntu1", "")
	want(t, deps, "zlib1g", "", "")
	want(t, deps, "foo", "", "")

	for _, n := range []string{"ignored-pkg", "libssl1.1", "${misc:Depends}"} {
		if _, ok := deps[n]; ok {
			t.Errorf("%s must not be listed", n)
		}
	}

	res = read(t, "deb", "demo_1.0_amd64.deb", debBytes(t, "Package: demo\nVersion: 1.0\nArchitecture: amd64\nDepends: a\n"))
	if res.Namespace != "debian" {
		t.Errorf("namespace = %q, want debian", res.Namespace)
	}
}

func TestRPMRequires(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "..", "pkg", "rpm", "testdata", "weakdeps.rpm"))
	if err != nil {
		t.Fatal(err)
	}

	res := read(t, "rpm", "oa-weakdep-test-1.0-1.noarch.rpm", data)
	deps := byName(t, res.Deps)

	for name := range deps {
		if strings.HasPrefix(name, "rpmlib(") || strings.HasPrefix(name, "/") {
			t.Errorf("non-package requirement %q listed", name)
		}
	}

	if len(deps) == 0 {
		t.Fatal("no requirements read from the rpm")
	}

	if _, ok := deps["shadow-utils"]; !ok {
		t.Errorf("shadow-utils missing from %+v", deps)
	}
}

func TestRPMRequireLines(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		line     string
		ok       bool
		ver, rng string
	}{
		{"bash", true, "", ""},
		{"glibc EQ 2.38", true, "2.38", ""},
		{"openssl-libs GE 3.0", true, "", ">= 3.0"},
		{"rpmlib(PayloadIsZstd)", false, "", ""},
		{"/bin/sh", false, "", ""},
		{"libc.so.6()(64bit)", false, "", ""},
		{"(foo if bar)", false, "", ""},
	} {
		d, ok := rpmRequire(tc.line)
		if ok != tc.ok || d.Version != tc.ver || d.Range != tc.rng {
			t.Errorf("rpmRequire(%q) = %+v, %v", tc.line, d, ok)
		}
	}

	for vendor, want := range map[string]string{"Fedora Project": "fedora", "Red Hat, Inc.": "redhat", "SUSE LLC": "opensuse", "Acme Corp": "acme", "": ""} {
		if got := rpmNamespace(vendor, "1.0-1"); got != want {
			t.Errorf("rpmNamespace(%q) = %q, want %q", vendor, got, want)
		}
	}

	if got := rpmNamespace("", "1.0-1.el9"); got != "redhat" {
		t.Errorf("el9 namespace = %q", got)
	}
}

func TestConanRecipe(t *testing.T) {
	t.Parallel()

	recipe := "from conan import ConanFile\nclass P(ConanFile):\n    requires = \"zlib/1.2.13\", \"bzip2/[>=1.0 <2]\"\n" +
		"    def requirements(self):\n        self.requires(\"openssl/3.1.0@acme/stable#abc\")\n        self.tool_requires(\"cmake/3.27\")\n" +
		"        self.test_requires(\"gtest/1.14\")\n"

	res, err := Read(context.Background(), Source{
		Format: "conan", Filename: "demo/1.0@_/_#rev",
		Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("manifest")), nil },
		Sibling: func(name string) (io.ReadCloser, error) {
			if name == "conanfile.py" {
				return io.NopCloser(strings.NewReader(recipe)), nil
			}

			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	deps := byName(t, res.Deps)
	want(t, deps, "zlib", "1.2.13", "")
	want(t, deps, "bzip2", "", "[>=1.0 <2]")
	want(t, deps, "openssl", "3.1.0", "")

	for _, n := range []string{"cmake", "gtest"} {
		if _, ok := deps[n]; ok {
			t.Errorf("%s must not be listed", n)
		}
	}
}

func TestHostileArchivesAreRefused(t *testing.T) {
	t.Parallel()

	t.Run("two package.json candidates", func(t *testing.T) {
		t.Parallel()

		data := archivetest.TarGz(t,
			archivetest.File("a/package.json", `{"dependencies":{"x":"1.0.0"}}`),
			archivetest.File("b/package.json", `{"dependencies":{"y":"1.0.0"}}`))

		_, err := Read(context.Background(), memSource("npm", "x.tgz", data))
		if !errors.Is(err, ErrUnsafe) {
			t.Fatalf("err = %v, want ErrUnsafe", err)
		}
	})

	t.Run("manifest over the member limit", func(t *testing.T) {
		t.Parallel()

		huge := `{"dependencies":{"x":"1.0.0"},"pad":"` + strings.Repeat("a", memberLimit+1) + `"}`

		_, err := Read(context.Background(), memSource("npm", "x.tgz", archivetest.TarGz(t, archivetest.File("p/package.json", huge))))
		if !errors.Is(err, ErrUnsafe) {
			t.Fatalf("err = %v, want ErrUnsafe", err)
		}
	})

	t.Run("unsupported file", func(t *testing.T) {
		t.Parallel()

		if _, err := Read(context.Background(), memSource("npm", "readme.txt", nil)); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("err = %v, want ErrUnsupported", err)
		}

		if _, err := Read(context.Background(), memSource("docker", "x", nil)); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("docker err = %v, want ErrUnsupported", err)
		}
	})

	t.Run("dependency count is bounded", func(t *testing.T) {
		t.Parallel()

		var b strings.Builder

		b.WriteString(`{"dependencies":{`)

		for i := range MaxDeps + 50 {
			if i > 0 {
				b.WriteByte(',')
			}

			b.WriteString(`"p` + strings.Repeat("x", 1) + itoa(i) + `":"1.0.0"`)
		}

		b.WriteString("}}")

		res := read(t, "npm", "x.tgz", archivetest.TarGz(t, archivetest.File("p/package.json", b.String())))
		if len(res.Deps) != MaxDeps {
			t.Errorf("len = %d, want %d", len(res.Deps), MaxDeps)
		}
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var d []byte

	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}

	return string(d)
}
