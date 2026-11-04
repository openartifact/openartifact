// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
)

// rootBombTarGz builds a tar.gz whose entries are all named "./": entrySet
// never counts them, so only the bound on the inflated stream stops them.
func rootBombTarGz(tb testing.TB, entries int, size int64) []byte {
	tb.Helper()

	var buf bytes.Buffer

	gw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		tb.Fatal(err)
	}

	tw := tar.NewWriter(gw)

	for range entries {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: ".", Size: size, Mode: 0o644}); err != nil {
			tb.Fatal(err)
		}

		if _, err := io.CopyN(tw, zeroReader{}, size); err != nil {
			tb.Fatal(err)
		}
	}

	if err := tw.Close(); err != nil {
		tb.Fatal(err)
	}

	if err := gw.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)

	return len(p), nil
}

func TestReadTarGzRefusesUncountedEntriesPastTheLimit(t *testing.T) {
	data := rootBombTarGz(t, 1, 100<<20)

	set := newEntrySet(archiveLimits{expanded: 1 << 20}, int64(len(data)))

	err := readTarGz(bytes.NewReader(data), set)

	var ae *archiveError
	if !errors.As(err, &ae) || !strings.Contains(ae.msg, "expands to more than") {
		t.Fatalf("error %v, want an expansion refusal", err)
	}
}

func TestReadTarGzKeepsTheLimitForCountedEntries(t *testing.T) {
	data := tarGzOf(t, tarEntry{name: "r/main.tf", body: "root"}, tarEntry{name: "r/b.tf", body: "x"})

	set := newEntrySet(archiveLimits{expanded: 1 << 20}, int64(len(data)))
	if err := readTarGz(bytes.NewReader(data), set); err != nil {
		t.Fatal(err)
	}
}

func TestRepackRefusesUncountedEntriesPastTheLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("inflates a gigabyte")
	}

	// Two "./" entries, together past maxModuleExpanded and the framing slack.
	data := rootBombTarGz(t, 2, maxModuleExpanded/2+tarOverheadSlack)

	_, err := repack(data, false, "")
	if err == nil || !IsRepackError(err) || !strings.Contains(err.Error(), "expands to more than") {
		t.Fatalf("error %v (%T), want an expansion refusal", err, err)
	}
}
