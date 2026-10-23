// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

type file struct {
	name string
	data string
}

func tarBytes(t *testing.T, files []file) []byte {
	t.Helper()

	var buf bytes.Buffer

	tw := tar.NewWriter(&buf)

	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}

		if _, err := tw.Write([]byte(f.data)); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func tarGz(t *testing.T, files []file) []byte {
	t.Helper()

	return gzipBytes(t, tarBytes(t, files))
}

func zipBytes(t *testing.T, files []file) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := w.Write([]byte(f.data)); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

// debBytes builds a minimal .deb: an ar archive with debian-binary, a gzip
// control archive and an empty data archive.
func debBytes(t *testing.T, control string) []byte {
	t.Helper()

	member := func(name string, data []byte) []byte {
		var b bytes.Buffer

		hdr := []byte(strings.Repeat(" ", 60))
		copy(hdr[0:], name+"/")
		copy(hdr[16:], "0")
		copy(hdr[28:], "0")
		copy(hdr[34:], "0")
		copy(hdr[40:], "100644")
		copy(hdr[48:], itoa(len(data)))
		copy(hdr[58:], "`\n")
		b.Write(hdr)
		b.Write(data)

		if len(data)%2 == 1 {
			b.WriteByte('\n')
		}

		return b.Bytes()
	}

	var out bytes.Buffer

	out.WriteString("!<arch>\n")
	out.Write(member("debian-binary", []byte("2.0\n")))
	out.Write(member("control.tar.gz", tarGz(t, []file{{"./control", control}})))
	out.Write(member("data.tar.gz", tarGz(t, nil)))

	return out.Bytes()
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

// condaBz2 is a small .tar.bz2 holding lib/libdemo.so, info/index.json and
// info/about.json (bzip2 has no encoder in the standard library, so the
// fixture is embedded).
const condaBz2 = "QlpoOTFBWSZTWfQNIXEAAOxfosyAUAf/syAiNAr//99qCCABAAgIMAD6sRW1U9QeoDTaJoZNAyZpqYEyYgBoiaGU8ppo00A0AANAaaaNAyU00Rppppo9IBoNA0PUADQ0aOMJqKGmdQBLjwJBDol6lhgBRjIi+axIIQAt5JAAAMcmDscO2ZEh20X4TLFmI3IEXpQwKtfJYZ67UvUriY5OjbJlMyAANwlSgSeDUdJMYHSTW+UFFCk36gOJLUbCQ5SFQMIpuUaZzYU7yV4FFoF+mjY/MVNA3tQh3elYS/mNI9XWCC0UVCKokW2CJoUayqf+fbOIOkirhsR2dzuBJSAowt4wbYC+jakGpwvX4YZLDSeOWOX3JWILhLjQhX4hkjX+LK8EyIKoo3AsZyCB/F3JFOFCQ9A0hcQ="

func zstdBytes(t *testing.T, data []byte) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func memSource(format, filename string, data []byte) Source {
	return Source{
		Format:   format,
		Filename: filename,
		Open:     func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
	}
}

func TestExtractFormats(t *testing.T) {
	t.Parallel()

	rpmData, err := os.ReadFile(filepath.Join("..", "..", "..", "pkg", "rpm", "testdata", "weakdeps.rpm"))
	if err != nil {
		t.Fatal(err)
	}

	bz, err := base64.StdEncoding.DecodeString(condaBz2)
	if err != nil {
		t.Fatal(err)
	}

	condaInfo := tarBytes(t, []file{
		{"info/index.json", `{"name":"demo-conda","version":"1.2.3","license":"MIT"}`},
		{"info/about.json", `{"summary":"A demo conda package"}`},
	})
	conda := zipBytes(t, []file{
		{"pkg-demo-1.2.3-0.tar.zst", "ignored"},
		{"info-demo-1.2.3-0.tar.zst", string(zstdBytes(t, condaInfo))},
	})

	gemMeta := gzipBytes(t, []byte("--- !ruby/object:Gem::Specification\nname: demo-gem\nversion: !ruby/object:Gem::Version\n  version: 0.9.0\nlicenses:\n- MIT\n"))
	gem := tarBytes(t, []file{{"metadata.gz", string(gemMeta)}, {"data.tar.gz", string(tarGz(t, nil))}})

	tests := []struct {
		name     string
		format   string
		filename string
		data     []byte
		want     []string
		notWant  []string
	}{
		{"npm", "npm", "left-pad-1.0.0.tgz", tarGz(t, []file{
			{"package/index.js", "module.exports = 1"},
			{"package/package.json", `{"name":"left-pad","version":"1.0.0","license":"WTFPL"}`},
		}), []string{"package.json", `"name":"left-pad"`, "WTFPL"}, []string{"module.exports"}},
		{"pypi wheel", "pypi", "demo-1.0-py3-none-any.whl", zipBytes(t, []file{
			{"demo/__init__.py", "x = 1"},
			{"demo-1.0.dist-info/METADATA", "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nLicense: MIT\nRequires-Dist: requests\n\n" + strings.Repeat("long description ", 2000)},
		}), []string{"Name: demo", "Requires-Dist: requests", "long description"}, []string{"x = 1"}},
		{"pypi sdist", "pypi", "demo-1.0.tar.gz", tarGz(t, []file{
			{"demo-1.0/setup.py", "setup()"},
			{"demo-1.0/PKG-INFO", "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\n"},
		}), []string{"Name: demo", "Version: 1.0"}, []string{"setup()"}},
		{"maven pom", "maven", "org/acme/demo/1.0/demo-1.0.pom", []byte("<project><artifactId>demo</artifactId></project>"), []string{"<artifactId>demo</artifactId>"}, nil},
		{"maven jar pom", "maven", "org/acme/demo/1.0/demo-1.0.jar", zipBytes(t, []file{
			{"META-INF/MANIFEST.MF", "Manifest-Version: 1.0\n"},
			{"META-INF/maven/org.acme/demo/pom.xml", "<project><artifactId>embedded</artifactId></project>"},
			{"org/acme/Demo.class", "CAFEBABE"},
		}), []string{"embedded", "Manifest-Version"}, []string{"CAFEBABE"}},
		{"ivy xml", "ivy", "ivy.xml", []byte(`<ivy-module version="2.0"><info organisation="acme" module="demo"/></ivy-module>`), []string{`module="demo"`}, nil},
		{"sbt pom", "sbt", "demo_2.13-1.0.pom", []byte("<project><artifactId>demo_2.13</artifactId></project>"), []string{"demo_2.13"}, nil},
		{"nuget", "nuget", "Demo.1.0.0.nupkg", zipBytes(t, []file{
			{"lib/net8.0/Demo.dll", "MZ"},
			{"Demo.nuspec", "<package><metadata><id>Demo</id><version>1.0.0</version></metadata></package>"},
		}), []string{"<id>Demo</id>"}, []string{"MZ"}},
		{"cargo", "cargo", "demo-0.1.0.crate", tarGz(t, []file{
			{"demo-0.1.0/src/lib.rs", "fn main() {}"},
			{"demo-0.1.0/Cargo.toml", "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n"},
		}), []string{`name = "demo"`}, []string{"fn main"}},
		{"helm", "helm", "demo-0.2.0.tgz", tarGz(t, []file{
			{"demo/Chart.yaml", "apiVersion: v2\nname: demo\nversion: 0.2.0\n"},
			{"demo/charts/sub/Chart.yaml", "name: sub-chart-must-not-win\n"},
			{"demo/requirements.yaml", "dependencies:\n- name: redis\n"},
		}), []string{"name: demo", "redis"}, []string{"sub-chart-must-not-win"}},
		{"deb", "deb", "demo_1.0_amd64.deb", debBytes(t, "Package: demo\nVersion: 1.0\nArchitecture: amd64\nDepends: libc6 (>= 2.31)\nDescription: a demo\n"),
			[]string{"Package: demo", "Depends: libc6 (>= 2.31)", "Description: a demo"}, nil},
		{"rpm", "rpm", "weakdeps.rpm", rpmData, []string{"Name: ", "Version: ", "Architecture: "}, nil},
		{"gomod mod", "go", "v1.0.0.mod", []byte("module example.com/demo\n\ngo 1.22\n"), []string{"module example.com/demo"}, nil},
		{"gomod zip", "go", "v1.0.0.zip", zipBytes(t, []file{
			{"example.com/demo@v1.0.0/main.go", "package main"},
			{"example.com/demo@v1.0.0/go.mod", "module example.com/demo\n\nrequire golang.org/x/text v0.3.0\n"},
		}), []string{"module example.com/demo", "golang.org/x/text"}, []string{"package main"}},
		{"rubygems", "rubygems", "demo-gem-0.9.0.gem", gem, []string{"name: demo-gem", "0.9.0", "MIT"}, nil},
		{"conda tar.bz2", "conda", "demo-conda-1.2.3-h0_0.tar.bz2", bz, []string{"demo-conda", "python >=3.9", "A demo conda package"}, []string{"xxxxxxxx"}},
		{"conda .conda", "conda", "demo-conda-1.2.3-0.conda", conda, []string{"demo-conda", "A demo conda package"}, []string{"ignored"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Extract(context.Background(), memSource(tc.format, tc.filename, tc.data))
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}

			if len(got) > MaxBytes {
				t.Errorf("result is %d bytes, over the %d limit", len(got), MaxBytes)
			}

			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}

			for _, w := range tc.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in:\n%s", w, got)
				}
			}
		})
	}
}

func TestPyPIMetadataBodyTrimmed(t *testing.T) {
	t.Parallel()

	wheel := zipBytes(t, []file{{"demo-1.0.dist-info/METADATA", "Name: demo\n\n" + strings.Repeat("z", 50_000)}})

	got, err := Extract(context.Background(), memSource("pypi", "demo-1.0-py3-none-any.whl", wheel))
	if err != nil {
		t.Fatal(err)
	}

	if len(got) > 3000 {
		t.Errorf("long description not trimmed: %d bytes", len(got))
	}
}

func TestExtractErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  Source
		want error
	}{
		{"docker unsupported", memSource("docker", "sha256:abc", []byte("{}")), ErrUnsupported},
		{"raw unsupported", memSource("raw", "notes.txt", []byte("hi")), ErrUnsupported},
		{"maven checksum unsupported", memSource("maven", "demo-1.0.jar.sha1", []byte("abc")), ErrUnsupported},
		{"maven metadata xml unsupported", memSource("maven", "maven-metadata.xml", []byte("<metadata/>")), ErrUnsupported},
		{"npm without package.json", memSource("npm", "x.tgz", tarGz(t, []file{{"package/readme.md", "hi"}})), ErrNotFound},
		{"nested package.json does not count", memSource("npm", "x.tgz", tarGz(t, []file{{"package/node_modules/y/package.json", "{}"}})), ErrNotFound},
		{"empty manifest", memSource("go", "v1.mod", []byte("  \n")), ErrNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Extract(context.Background(), tc.src); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestExtractCorruptArchive(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ format, name string }{
		{"npm", "x.tgz"}, {"nuget", "x.nupkg"}, {"deb", "x.deb"}, {"rpm", "x.rpm"}, {"pypi", "x.whl"}, {"rubygems", "x.gem"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			t.Parallel()

			_, err := Extract(context.Background(), memSource(tc.format, tc.name, []byte("this is not an archive")))
			if err == nil || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want a read error", err)
			}
		})
	}
}

func TestDecompressionBombIsCapped(t *testing.T) {
	// maxScanBytes is package state: run serially.
	old := maxScanBytes
	maxScanBytes = 1 << 20

	t.Cleanup(func() { maxScanBytes = old })

	big := strings.Repeat("\x00", 4<<20)
	data := tarGz(t, []file{{"package/big.bin", big}, {"package/package.json", `{"name":"late"}`}})

	_, err := Extract(context.Background(), memSource("npm", "bomb.tgz", data))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestOversizedMemberIsTruncatedNotRejected(t *testing.T) {
	t.Parallel()

	pkg := `{"name":"huge","description":"` + strings.Repeat("a", 500_000) + `"}`

	got, err := Extract(context.Background(), memSource("npm", "huge.tgz", tarGz(t, []file{{"package/package.json", pkg}})))
	if err != nil {
		t.Fatal(err)
	}

	if len(got) > MaxBytes || !strings.Contains(got, `"name":"huge"`) {
		t.Errorf("got %d bytes, want a truncated manifest within %d", len(got), MaxBytes)
	}
}

func TestCancelledContextAbortsExtraction(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	data := tarGz(t, []file{{"package/package.json", `{"name":"x"}`}})

	if _, err := Extract(ctx, memSource("npm", "x.tgz", data)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// fileSource serves the blob from an *os.File, which supports random access
// without spooling; plainSource hides that so openAt has to spool.
func TestZipRandomAccessAndSpooling(t *testing.T) {
	t.Parallel()

	data := zipBytes(t, []file{{"Demo.nuspec", "<id>Demo</id>"}})
	path := filepath.Join(t.TempDir(), "demo.nupkg")

	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	fromFile := Source{Format: "nuget", Filename: "demo.nupkg", Open: func() (io.ReadCloser, error) { return os.Open(path) }}

	for name, src := range map[string]Source{"file": fromFile, "spooled": memSource("nuget", "demo.nupkg", data)} {
		got, err := Extract(context.Background(), src)
		if err != nil || !strings.Contains(got, "<id>Demo</id>") {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
}

func TestConanRecipeSiblings(t *testing.T) {
	t.Parallel()

	src := memSource("conan", "demo/1.0@_/_#abc", []byte("1700000000\nconanfile.py: abc\n"))
	src.Sibling = func(name string) (io.ReadCloser, error) {
		if name == "conanfile.py" {
			return io.NopCloser(strings.NewReader("class Demo(ConanFile):\n    name = \"demo\"\n")), nil
		}

		return nil, nil
	}

	got, err := Extract(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "class Demo(ConanFile)") || strings.Contains(got, "1700000000") {
		t.Errorf("got %q, want the recipe and not the manifest", got)
	}
}

func TestAutoEligibleLimitsJVMFormatsToDescriptors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		format, file string
		want         bool
	}{
		{"maven", "org/a/b/1.0/b-1.0.pom", true},
		{"maven", "org/a/b/1.0/b-1.0.jar", false},
		{"sbt", "org/a/b/1.0/b-1.0.jar", false},
		{"ivy", "ivy.xml", true},
		{"npm", "x-1.0.tgz", true},
		{"npm", "x.json", false},
		{"docker", "sha256:abc", false},
		{"raw", "a.txt", false},
	}

	for _, tc := range tests {
		if got := AutoEligible(tc.format, tc.file); got != tc.want {
			t.Errorf("AutoEligible(%s, %s) = %v, want %v", tc.format, tc.file, got, tc.want)
		}
	}
}

func TestLockfilesAreReadOnlyWhenDepsIsSet(t *testing.T) {
	t.Parallel()

	data := tarGz(t, []file{
		{"package/package.json", `{"name":"demo"}`},
		{"package/npm-shrinkwrap.json", `{"lockfileVersion":3}`},
	})

	src := memSource("npm", "demo-1.0.0.tgz", data)

	parts, err := ExtractParts(context.Background(), src)
	if err != nil || len(parts) != 1 || parts[0].Label != "package.json" {
		t.Fatalf("without Deps: parts = %+v, err = %v", parts, err)
	}

	src.Deps = true

	parts, err = ExtractParts(context.Background(), src)
	if err != nil || len(parts) != 2 || parts[0].Label != "package.json" || parts[1].Label != "npm-shrinkwrap.json" {
		t.Fatalf("with Deps: parts = %+v, err = %v", parts, err)
	}
}
