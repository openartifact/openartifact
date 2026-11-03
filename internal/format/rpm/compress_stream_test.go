// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)

	return hex.EncodeToString(h[:])
}

func TestCompressMetadataStreamRoundTrip(t *testing.T) {
	pkgs := benchRPMPackages(120)

	want, err := renderXML(WritePrimaryXML, pkgs)
	if err != nil {
		t.Fatal(err)
	}

	streamed, err := CompressMetadataStream(func(w io.Writer) error { return WritePrimaryXML(w, pkgs) })
	if err != nil {
		t.Fatal(err)
	}

	buffered, err := compressXML(want)
	if err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]*CompressedMetadata{"streamed": streamed, "buffered": buffered} {
		t.Run(name, func(t *testing.T) {
			gr, err := gzip.NewReader(bytes.NewReader(c.GzipData))
			if err != nil {
				t.Fatal(err)
			}

			gotGz, err := io.ReadAll(gr)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(gotGz, want) {
				t.Fatal("decompressed content differs from the generated XML")
			}

			if c.OpenSize != int64(len(want)) {
				t.Errorf("OpenSize = %d, want %d", c.OpenSize, len(want))
			}

			if c.GzSize != int64(len(c.GzipData)) {
				t.Errorf("size %d does not match data %d", c.GzSize, len(c.GzipData))
			}

			if c.GzSHA256 != sha256Hex(c.GzipData) {
				t.Error("checksums do not match the compressed data")
			}
		})
	}
}

func TestCompressMetadataStreamErrors(t *testing.T) {
	boom := errors.New("generator failed")

	if _, err := CompressMetadataStream(func(io.Writer) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("writer error not propagated: %v", err)
	}

	if _, err := CompressMetadataStream(func(io.Writer) error { return nil }); err == nil {
		t.Error("expected an error when nothing was written")
	}
}
