// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pypi

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

// zeroReader yields n zero bytes.
type zeroReader struct{ n int64 }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}

	if int64(len(p)) > z.n {
		p = p[:z.n]
	}

	clear(p)

	z.n -= int64(len(p))

	return len(p), nil
}

// A small gzip whose tar entry inflates past the unpacked cap must be
// rejected instead of being decompressed to the end.
func TestParseSdist_GzipBombRejected(t *testing.T) {
	size := int64(maxPyPIUnpackedBytes) + 1<<20

	var buf bytes.Buffer

	gw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	tw := tar.NewWriter(gw)

	if err := tw.WriteHeader(&tar.Header{Name: "pkg-1.0/big.bin", Mode: 0o644, Size: size, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}

	if _, err := io.Copy(tw, &zeroReader{n: size}); err != nil {
		t.Fatal(err)
	}

	_ = tw.Close()
	_ = gw.Close()

	if _, err := parseSdist(buf.Bytes()); err == nil {
		t.Fatal("expected a decompression bomb to be rejected")
	}
}

func TestParseWheel_OversizedMetadataRejected(t *testing.T) {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	w, err := zw.Create("pkg-1.0.dist-info/METADATA")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.Copy(w, &zeroReader{n: maxPyPIMetadataBytes + 1}); err != nil {
		t.Fatal(err)
	}

	_ = zw.Close()

	if _, err := parseWheel(buf.Bytes()); err == nil {
		t.Fatal("expected oversized METADATA to be rejected")
	}
}
