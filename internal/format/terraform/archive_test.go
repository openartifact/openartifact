// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"testing"
)

func validate(data []byte) (archiveInfo, error) {
	return validateModuleArchive(bytes.NewReader(data), int64(len(data)))
}

func TestValidateModuleArchive(t *testing.T) {
	const tf = `variable "x" {}`

	symlinkZip := func() []byte {
		var buf bytes.Buffer

		zw := zip.NewWriter(&buf)
		hdr := &zip.FileHeader{Name: "link.tf"}
		hdr.SetMode(fs.ModeSymlink | 0o777)

		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = w.Write([]byte("/etc/passwd"))
		_ = zw.Close()

		return buf.Bytes()
	}()

	manyFiles := func() []byte {
		var buf bytes.Buffer

		zw := zip.NewWriter(&buf)

		// Stored entries: a deflate writer per entry would dominate the test.
		for i := range maxEntries + 1 {
			if _, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("f%d.tf", i), Method: zip.Store}); err != nil {
				t.Fatal(err)
			}
		}

		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}

		return buf.Bytes()
	}()

	for _, tc := range []struct {
		name         string
		data         []byte
		wantExt      string
		wantErr      bool
		wantSemantic bool
	}{
		{name: "zip with root tf", data: zipOf(t, [2]string{"main.tf", tf}), wantExt: "zip"},
		{name: "zip with tf.json", data: zipOf(t, [2]string{"main.tf.json", "{}"}), wantExt: "zip"},
		{name: "zip with dirs", data: zipOf(t, [2]string{"modules/", ""}, [2]string{"main.tf", tf}, [2]string{"modules/a.tf", tf}), wantExt: "zip"},
		{name: "zip single top dir", data: zipOf(t, [2]string{"repo-1.0/main.tf", tf}, [2]string{"repo-1.0/x/y.tf", tf}), wantExt: "zip"},
		{name: "tar.gz with root tf", data: tarGzOf(t, tarEntry{name: "main.tf", body: tf}), wantExt: "tar.gz"},
		{name: "tar.gz dot prefix", data: tarGzOf(t, tarEntry{name: "./", typeflag: tar.TypeDir}, tarEntry{name: "./main.tf", body: tf}), wantExt: "tar.gz"},
		{name: "tar.gz single top dir", data: tarGzOf(t, tarEntry{name: "m/", typeflag: tar.TypeDir}, tarEntry{name: "m/main.tf", body: tf}), wantExt: "tar.gz"},

		{name: "no tf file", data: zipOf(t, [2]string{"README.md", "x"}), wantErr: true, wantSemantic: true},
		{name: "tf only nested deeper", data: zipOf(t, [2]string{"a/b/main.tf", tf}), wantErr: true, wantSemantic: true},
		{name: "tf under two top dirs", data: zipOf(t, [2]string{"a/main.tf", tf}, [2]string{"b/x.txt", "x"}), wantErr: true, wantSemantic: true},
		{name: "readme at root and tf in dir", data: zipOf(t, [2]string{"README", "x"}, [2]string{"a/main.tf", tf}), wantErr: true, wantSemantic: true},
		{name: "empty zip", data: zipOf(t), wantErr: true},
		{name: "only directories", data: zipOf(t, [2]string{"a/", ""}), wantErr: true},
		{name: "not an archive", data: []byte("hello world"), wantErr: true},
		{name: "truncated zip", data: zipOf(t, [2]string{"main.tf", tf})[:20], wantErr: true},
		{name: "truncated gzip", data: tarGzOf(t, tarEntry{name: "main.tf", body: tf})[:30], wantErr: true},
		{name: "gzip of non-tar", data: gzipOf(t, "just text"), wantErr: true},
		{name: "zip traversal", data: zipOf(t, [2]string{"../evil.tf", tf}), wantErr: true},
		{name: "zip nested traversal", data: zipOf(t, [2]string{"a/../../evil.tf", tf}), wantErr: true},
		{name: "zip absolute", data: zipOf(t, [2]string{"/etc/evil.tf", tf}), wantErr: true},
		{name: "zip backslash", data: zipOf(t, [2]string{`a\evil.tf`, tf}), wantErr: true},
		{name: "zip drive letter", data: zipOf(t, [2]string{"C:evil.tf", tf}), wantErr: true},
		{name: "zip duplicate", data: zipOf(t, [2]string{"main.tf", tf}, [2]string{"main.tf", tf}), wantErr: true},
		{name: "zip duplicate after clean", data: zipOf(t, [2]string{"main.tf", tf}, [2]string{"./main.tf", tf}), wantErr: true},
		{name: "zip symlink", data: symlinkZip, wantErr: true},
		{name: "zip too many entries", data: manyFiles, wantErr: true},
		{name: "tar traversal", data: tarGzOf(t, tarEntry{name: "../evil.tf", body: tf}), wantErr: true},
		{name: "tar absolute", data: tarGzOf(t, tarEntry{name: "/evil.tf", body: tf}), wantErr: true},
		{name: "tar symlink", data: tarGzOf(t, tarEntry{name: "main.tf", body: tf}, tarEntry{name: "l", typeflag: tar.TypeSymlink, link: "/etc"}), wantErr: true},
		{name: "tar hardlink", data: tarGzOf(t, tarEntry{name: "main.tf", body: tf}, tarEntry{name: "h", typeflag: tar.TypeLink, link: "main.tf"}), wantErr: true},
		{name: "tar fifo", data: tarGzOf(t, tarEntry{name: "main.tf", body: tf}, tarEntry{name: "p", typeflag: tar.TypeFifo}), wantErr: true},
		{name: "tar duplicate", data: tarGzOf(t, tarEntry{name: "main.tf", body: tf}, tarEntry{name: "main.tf", body: tf}), wantErr: true},
		{name: "tar empty", data: tarGzOf(t), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := validate(tc.data)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted, want an error")
				}

				ae, ok := errors.AsType[*archiveError](err)
				if !ok {
					t.Fatalf("error %T is not an *archiveError", err)
				}

				if ae.semantic != tc.wantSemantic {
					t.Fatalf("semantic = %v, want %v (%v)", ae.semantic, tc.wantSemantic, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("rejected: %v", err)
			}

			if info.ext != tc.wantExt {
				t.Fatalf("ext = %q, want %q", info.ext, tc.wantExt)
			}
		})
	}
}

func gzipOf(t *testing.T, s string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(s))

	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestValidateModuleArchiveCorruptContent(t *testing.T) {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, err := zw.CreateHeader(&zip.FileHeader{Name: "main.tf", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}

	_, _ = w.Write([]byte(`variable "marker_marker_marker" {}`))
	_ = zw.Close()

	data := buf.Bytes()

	// Flip a byte of the stored content: the CRC no longer matches.
	i := bytes.Index(data, []byte("marker_marker_marker"))
	if i < 0 {
		t.Fatal("content not found in the zip")
	}

	data[i] ^= 0xff

	if _, err := validate(data); err == nil {
		t.Fatal("a zip with a failing CRC was accepted")
	}
}

func TestEntrySetLimits(t *testing.T) {
	for _, tc := range []struct {
		name       string
		compressed int64
		sizes      []int64
		wantErr    bool
	}{
		{name: "small", compressed: 100, sizes: []int64{10, 20}},
		{name: "one entry over the entry cap", compressed: 1 << 30, sizes: []int64{maxEntryBytes + 1}, wantErr: true},
		{name: "total over the module cap", compressed: 1 << 30, sizes: []int64{maxModuleExpanded, 1}, wantErr: true},
		{name: "ratio bomb", compressed: 100 << 10, sizes: []int64{bombMinBytes + 1}, wantErr: true},
		{name: "large but sane ratio", compressed: 50 << 20, sizes: []int64{bombMinBytes + 1}},
		{name: "high ratio below the floor", compressed: 1, sizes: []int64{bombMinBytes}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := newEntrySet(archiveLimits{expanded: maxModuleExpanded}, tc.compressed)

			var err error

			for i, size := range tc.sizes {
				if _, err = set.add(fmt.Sprintf("f%d", i), false, size); err != nil {
					break
				}
			}

			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestCleanEntryName(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "main.tf", want: "main.tf"},
		{in: "./main.tf", want: "main.tf"},
		{in: "a/b/", want: "a/b"},
		{in: "./", want: ""},
		{in: "a//b", want: "a/b"},
		{in: "a/./b", want: "a/b"},
		{in: "", wantErr: true},
		{in: "..", wantErr: true},
		{in: "../x", wantErr: true},
		{in: "a/../x", wantErr: true},
		{in: "a/..", wantErr: true},
		{in: "/x", wantErr: true},
		{in: `a\b`, wantErr: true},
		{in: "a\x00b", wantErr: true},
		{in: "c:x", wantErr: true},
		{in: "..x", want: "..x"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := cleanEntryName(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("cleanEntryName(%q) = %q, %v; want %q, err=%v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestValidateProviderArchive(t *testing.T) {
	manifest := func(body string) [2]string { return [2]string{manifestName, body} }

	for _, tc := range []struct {
		name         string
		data         []byte
		wantProtocol []string
		wantErr      bool
		wantSemantic bool
	}{
		{name: "binary at root", data: providerZip(t, "aws", "1.2.3")},
		{name: "windows binary", data: zipOf(t, [2]string{"terraform-provider-aws_v1.2.3.exe", "MZ"})},
		{name: "protocol suffix", data: zipOf(t, [2]string{"terraform-provider-aws_v1.2.3_x5", "x"})},
		{name: "manifest protocols sorted", data: providerZip(t, "aws", "1.2.3", manifest(`{"version":1,"metadata":{"protocol_versions":["6.0","5.0","6.0"]}}`)), wantProtocol: []string{"5.0", "6.0"}},
		{name: "manifest without metadata", data: providerZip(t, "aws", "1.2.3", manifest(`{"version":1}`))},
		{name: "manifest not json", data: providerZip(t, "aws", "1.2.3", manifest(`nope`)), wantErr: true, wantSemantic: true},
		{name: "manifest bad protocol", data: providerZip(t, "aws", "1.2.3", manifest(`{"metadata":{"protocol_versions":["five"]}}`)), wantErr: true, wantSemantic: true},
		{name: "binary missing", data: zipOf(t, [2]string{"LICENSE", "x"}), wantErr: true, wantSemantic: true},
		{name: "binary in a directory", data: zipOf(t, [2]string{"bin/terraform-provider-aws_v1.2.3", "x"}), wantErr: true, wantSemantic: true},
		{name: "binary of another version", data: zipOf(t, [2]string{"terraform-provider-aws_v9.9.9", "x"}), wantErr: true, wantSemantic: true},
		{name: "binary of another type", data: zipOf(t, [2]string{"terraform-provider-gcp_v1.2.3", "x"}), wantErr: true, wantSemantic: true},
		{name: "binary with a junk suffix", data: zipOf(t, [2]string{"terraform-provider-aws_v1.2.3_evil", "x"}), wantErr: true, wantSemantic: true},
		{name: "tar.gz is not a provider archive", data: tarGzOf(t, tarEntry{name: "terraform-provider-aws_v1.2.3", body: "x"}), wantErr: true},
		{name: "traversal", data: zipOf(t, [2]string{"terraform-provider-aws_v1.2.3", "x"}, [2]string{"../x", "x"}), wantErr: true},
		{name: "garbage", data: []byte("PK\x03\x04garbage"), wantErr: true},
		{name: "empty zip", data: zipOf(t), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := validateProviderArchive(bytes.NewReader(tc.data), int64(len(tc.data)), "aws", "1.2.3")

			if tc.wantErr {
				ae, ok := errors.AsType[*archiveError](err)
				if !ok {
					t.Fatalf("err = %v, want an *archiveError", err)
				}

				if ae.semantic != tc.wantSemantic {
					t.Fatalf("semantic = %v, want %v (%v)", ae.semantic, tc.wantSemantic, err)
				}

				return
			}

			if err != nil {
				t.Fatalf("rejected: %v", err)
			}

			if !slices.Equal(info.protocols, tc.wantProtocol) {
				t.Fatalf("protocols = %v, want %v", info.protocols, tc.wantProtocol)
			}
		})
	}
}

func TestIsProviderBinary(t *testing.T) {
	for _, tc := range []struct {
		file string
		want bool
	}{
		{"terraform-provider-aws_v1.0.0", true},
		{"terraform-provider-aws_v1.0.0.exe", true},
		{"terraform-provider-aws_v1.0.0_x5", true},
		{"terraform-provider-aws_v1.0.0_x5.exe", true},
		{"terraform-provider-aws_v1.0.0_x", false},
		{"terraform-provider-aws_v1.0.0_x5a", false},
		{"terraform-provider-aws_v1.0.0.zip", false},
		{"terraform-provider-aws_v1.0.01", false},
		{"terraform-provider-aws_1.0.0", false},
		{"dir/terraform-provider-aws_v1.0.0", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			if got := isProviderBinary(tc.file, "aws", "1.0.0"); got != tc.want {
				t.Fatalf("isProviderBinary(%q) = %v, want %v", tc.file, got, tc.want)
			}
		})
	}
}

func TestNormalizeProtocols(t *testing.T) {
	for _, tc := range []struct {
		in      []string
		want    string
		wantErr bool
	}{
		{in: []string{"5.0"}, want: "5.0"},
		{in: []string{"6.0", "5.0", "5.0"}, want: "5.0 6.0"},
		{in: []string{"5.10"}, want: "5.10"},
		{in: []string{"5"}, wantErr: true},
		{in: []string{"5.0.1"}, wantErr: true},
		{in: []string{"a.b"}, wantErr: true},
		{in: []string{""}, wantErr: true},
		{in: []string{"1.0", "2.0", "3.0", "4.0", "5.0", "6.0", "7.0", "8.0", "9.0"}, wantErr: true},
	} {
		t.Run(strings.Join(tc.in, ","), func(t *testing.T) {
			got, err := normalizeProtocols(tc.in)
			if (err != nil) != tc.wantErr || strings.Join(got, " ") != tc.want {
				t.Fatalf("normalizeProtocols(%v) = %v, %v; want %q, err=%v", tc.in, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestDetectArchive(t *testing.T) {
	for _, tc := range []struct {
		head string
		want string
	}{
		{"PK\x03\x04", "zip"}, {"PK\x05\x06", "zip"}, {"\x1f\x8b\x08\x00", "tar.gz"}, {"", ""}, {"PK", ""}, {"{\"a\"", ""},
	} {
		if got := detectArchive([]byte(tc.head)); got != tc.want {
			t.Errorf("detectArchive(%q) = %q, want %q", tc.head, got, tc.want)
		}
	}
}
