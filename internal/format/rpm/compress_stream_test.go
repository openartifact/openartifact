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

	"github.com/ulikunitz/xz"
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

	streamed, err := CompressMetadataStream(func(w io.Writer) error { return WritePrimaryXML(w, pkgs) }, 0)
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

			xr, err := xz.NewReader(bytes.NewReader(c.XZData))
			if err != nil {
				t.Fatal(err)
			}

			gotXZ, err := io.ReadAll(xr)
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(gotGz, want) || !bytes.Equal(gotXZ, want) {
				t.Fatal("decompressed content differs from the generated XML")
			}

			if c.OpenSize != int64(len(want)) {
				t.Errorf("OpenSize = %d, want %d", c.OpenSize, len(want))
			}

			if c.GzSize != int64(len(c.GzipData)) || c.XZSize != int64(len(c.XZData)) {
				t.Errorf("sizes %d/%d do not match data %d/%d", c.GzSize, c.XZSize, len(c.GzipData), len(c.XZData))
			}

			if c.GzSHA256 != sha256Hex(c.GzipData) || c.XZSHA256 != sha256Hex(c.XZData) {
				t.Error("checksums do not match the compressed data")
			}
		})
	}
}

func TestCompressMetadataStreamErrors(t *testing.T) {
	boom := errors.New("generator failed")

	if _, err := CompressMetadataStream(func(io.Writer) error { return boom }, 0); !errors.Is(err, boom) {
		t.Errorf("writer error not propagated: %v", err)
	}

	if _, err := CompressMetadataStream(func(io.Writer) error { return nil }, 0); err == nil {
		t.Error("expected an error when nothing was written")
	}
}
