// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// paxBombTarGz is a tar.gz of many PAX records: tar.Reader.Next consumes them
// without reporting an entry, so no entry's size or count sees their bytes.
func paxBombTarGz(t *testing.T, records, recordBytes int) []byte {
	t.Helper()

	var buf bytes.Buffer

	gw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}

	tw := tar.NewWriter(gw)
	pad := strings.Repeat("a", recordBytes)

	for i := range records {
		hdr := &tar.Header{
			Typeflag: tar.TypeReg, Name: "f" + strconv.Itoa(i), Size: 0, Mode: 0o644,
			Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": pad},
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestScanBoundsTarFramingBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("inflates a few hundred megabytes")
	}

	// 250 records of 1000 KiB inflate to ~245 MiB against a 1 MiB limit plus
	// the ~196 MiB framing slack; no entry carries any data.
	data := paxBombTarGz(t, 250, 1000<<10)

	_, err := scan(bytes.NewReader(data), 1<<20)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "contents larger than") {
		t.Fatalf("error %v, want a size refusal", err)
	}
}
