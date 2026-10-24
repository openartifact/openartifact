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
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

const pypiFuzzMetadata = "Metadata-Version: 2.1\nName: demo\nVersion: 1.0\nLicense: MIT\nRequires-Dist: requests\n\n" +
	"Long description\n"

func pypiFuzzWheel(name, body string) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
	_, _ = w.Write([]byte(body))
	_ = zw.Close()

	return buf.Bytes()
}

func pypiFuzzSdist(name, body string) []byte {
	var buf bytes.Buffer

	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gw.Close()

	return buf.Bytes()
}

// FuzzExtractPyPI feeds an arbitrary file to the METADATA / PKG-INFO
// extractor under the file names of the PyPI distribution kinds, strict and
// lenient: whatever comes back is bounded, clean text.
func FuzzExtractPyPI(f *testing.F) {
	f.Add(pypiFuzzWheel("demo-1.0.dist-info/METADATA", pypiFuzzMetadata), "demo-1.0-py3-none-any.whl", false)
	f.Add(pypiFuzzWheel("demo-1.0.dist-info/METADATA", pypiFuzzMetadata), "demo-1.0-py3-none-any.whl", true)
	f.Add(pypiFuzzWheel("demo-1.0/PKG-INFO", pypiFuzzMetadata), "demo-1.0.zip", true)
	f.Add(pypiFuzzWheel("./a/../demo-1.0.dist-info/METADATA", pypiFuzzMetadata), "demo.egg", true)
	f.Add(pypiFuzzWheel("/abs.dist-info/METADATA", pypiFuzzMetadata), "demo.whl", true)
	f.Add(pypiFuzzSdist("demo-1.0/PKG-INFO", pypiFuzzMetadata), "demo-1.0.tar.gz", false)
	f.Add(pypiFuzzSdist("demo-1.0/PKG-INFO", "Name: \x00\xff\xfe\n"), "demo-1.0.tgz", true)
	f.Add(pypiFuzzSdist("PKG-INFO", pypiFuzzMetadata), "demo-1.0.tar.gz", true)
	f.Add([]byte("BZh9"), "demo-1.0.tar.bz2", true)
	f.Add([]byte{0x1f, 0x8b}, "demo-1.0.tar.gz", false)
	f.Add([]byte{}, "", false)

	f.Fuzz(func(t *testing.T, data []byte, filename string, strict bool) {
		src := Source{
			Format:      "pypi",
			Filename:    filename,
			Open:        func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
			MemberLimit: 4096,
			Strict:      strict,
		}

		parts, err := ExtractParts(context.Background(), src)
		if err != nil {
			pypiFuzzCheckError(t, src, err)

			return
		}

		if len(parts) == 0 {
			t.Fatal("ExtractParts succeeded without a part")
		}

		for _, p := range parts {
			if p.Label != "METADATA" && p.Label != "PKG-INFO" {
				t.Fatalf("unexpected part label %q", p.Label)
			}

			if !utf8.ValidString(p.Body) || strings.Contains(p.Body, "\x00") || strings.TrimSpace(p.Body) == "" {
				t.Fatalf("part %q is not clean non-empty text: %q", p.Label, p.Body)
			}

			// The member limit plus a trimmed description, with every invalid
			// byte possibly widened to U+FFFD.
			if len(p.Body) > 3*(4096+len("\n\n")+pyBodyBytes) {
				t.Fatalf("part is %d bytes, over the member limit", len(p.Body))
			}
		}

		text, err := Extract(context.Background(), src)
		if err != nil {
			t.Fatalf("ExtractParts succeeded but Extract failed: %v", err)
		}

		if len(text) > MaxBytes || !utf8.ValidString(text) || strings.Contains(text, "\x00") {
			t.Fatalf("Extract returned %d bytes of unclean text", len(text))
		}
	})
}

func pypiFuzzCheckError(t *testing.T, src Source, err error) {
	t.Helper()

	switch {
	case errors.Is(err, ErrUnsupported):
		if Supported(src.Format, src.Filename) {
			t.Fatalf("ExtractParts(%q) = ErrUnsupported but Supported is true", src.Filename)
		}
	case errors.Is(err, ErrAmbiguous):
		if !src.Strict {
			t.Fatal("a lenient read must not fail as ambiguous")
		}
	case errors.Is(err, ErrTooLarge), errors.Is(err, ErrNotFound):
	default:
		if !Supported(src.Format, src.Filename) {
			t.Fatalf("ExtractParts(%q) = %v for an unsupported file name", src.Filename, err)
		}
	}
}

// FuzzReadPyText feeds arbitrary METADATA bytes to the header reader: a
// strict read returns exactly the header block and fails once it outgrows
// the limit, a lenient one returns a bounded prefix.
func FuzzReadPyText(f *testing.F) {
	f.Add([]byte(pypiFuzzMetadata), int64(64))
	f.Add([]byte("Name: a\r\n\r\nbody"), int64(1))
	f.Add([]byte("no newline"), int64(4))
	f.Add([]byte("\n\n\n"), int64(0))
	f.Add([]byte{}, int64(10))

	f.Fuzz(func(t *testing.T, data []byte, limit int64) {
		limit = int64(uint64(limit)%8192) + 1

		strictText, strictErr := readPyText(bytes.NewReader(data), limits{n: limit, strict: true})
		if strictErr != nil {
			if !errors.Is(strictErr, ErrTooLarge) {
				t.Fatalf("strict read of in-memory data failed: %v", strictErr)
			}

			return
		}

		if int64(len(strictText)) > limit {
			t.Fatalf("strict read returned %d bytes over the limit %d", len(strictText), limit)
		}

		if !bytes.HasPrefix(data, []byte(strictText)) {
			t.Fatalf("strict read %q is not a prefix of the input", strictText)
		}

		// The header block ends at the first blank line, which is included:
		// no earlier line is blank.
		lines := strings.SplitAfter(strictText, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}

		for i, l := range lines[:max(len(lines)-1, 0)] {
			if strings.TrimRight(l, "\r\n") == "" {
				t.Fatalf("strict read %q goes past the blank line %d", strictText, i)
			}
		}

		lenient, err := readPyText(bytes.NewReader(data), limits{n: limit})
		if err != nil {
			t.Fatalf("lenient read failed: %v", err)
		}

		if int64(len(lenient)) > limit+int64(len("\n\n"))+pyBodyBytes {
			t.Fatalf("lenient read returned %d bytes for limit %d", len(lenient), limit)
		}
	})
}
