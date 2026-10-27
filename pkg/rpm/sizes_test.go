// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Both fixtures are built from testdata/sizes.spec by rpmbuild 6.0.1, once as
// a format-4 package (sizes in the signature header) and once as format 6
// (sizes in the main header).
type sizeFixture struct {
	file          string
	wantInstalled int64
	wantArchive   int64
}

func sizeFixtures() []sizeFixture {
	return []sizeFixture{
		{"testdata/sizes-v4.rpm", 100012, 100696},
		{"testdata/sizes-v6.rpm", 100012, 100204},
	}
}

func TestParseRPMReadsInstalledAndArchiveSize(t *testing.T) {
	for _, fx := range sizeFixtures() {
		t.Run(fx.file, func(t *testing.T) {
			data, err := os.ReadFile(fx.file)
			if err != nil {
				t.Fatal(err)
			}

			pkg, err := ParseRPM(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("ParseRPM: %v", err)
			}

			if pkg.InstalledSize != fx.wantInstalled {
				t.Errorf("InstalledSize = %d, want %d", pkg.InstalledSize, fx.wantInstalled)
			}

			if pkg.ArchiveSize != fx.wantArchive {
				t.Errorf("ArchiveSize = %d, want %d", pkg.ArchiveSize, fx.wantArchive)
			}

			// The file size is not a header tag; callers set it, so the
			// parser must not pass the installed size off as the package size.
			if pkg.PackageSize != 0 {
				t.Errorf("PackageSize = %d, want 0 (set by the caller)", pkg.PackageSize)
			}

			meta := pkg.GetMetadata()
			if meta["installedsize"] != fx.wantInstalled || meta["archivesize"] != fx.wantArchive {
				t.Errorf("metadata sizes = %v/%v", meta["installedsize"], meta["archivesize"])
			}
		})
	}
}

// rpmQuery runs `rpm -qp --queryformat` against the package.
func rpmQuery(t *testing.T, rpmBin, file, format string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, rpmBin, "-qp", "--queryformat", format, file).CombinedOutput()
	if err != nil {
		t.Fatalf("rpm -qp %s: %v\n%s", file, err, out)
	}

	return string(out)
}

// TestSizesMatchRPMClient compares the parser with what the real rpm client
// reports for the same files.
func TestSizesMatchRPMClient(t *testing.T) {
	rpmBin, err := exec.LookPath("rpm")
	if err != nil {
		t.Skip("rpm not installed")
	}

	for _, fx := range sizeFixtures() {
		t.Run(fx.file, func(t *testing.T) {
			data, err := os.ReadFile(fx.file)
			if err != nil {
				t.Fatal(err)
			}

			pkg, err := ParseRPM(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("ParseRPM: %v", err)
			}

			installed, err := strconv.ParseInt(rpmQuery(t, rpmBin, fx.file, "%{LONGSIZE}"), 10, 64)
			if err != nil {
				t.Fatalf("rpm LONGSIZE: %v", err)
			}

			if pkg.InstalledSize != installed {
				t.Errorf("InstalledSize = %d, rpm says %d", pkg.InstalledSize, installed)
			}

			// ARCHIVESIZE exists for format 4; rpm 6 packages expose the
			// uncompressed payload size as PAYLOADSIZEALT instead.
			archiveStr := rpmQuery(t, rpmBin, fx.file, "%{ARCHIVESIZE}")
			if strings.Contains(archiveStr, "none") {
				archiveStr = rpmQuery(t, rpmBin, fx.file, "%{PAYLOADSIZEALT}")
			}

			archive, err := strconv.ParseInt(archiveStr, 10, 64)
			if err != nil {
				t.Skipf("this rpm does not expose an archive size tag (%q)", archiveStr)
			}

			if pkg.ArchiveSize != archive {
				t.Errorf("ArchiveSize = %d, rpm says %d", pkg.ArchiveSize, archive)
			}

			files := strings.Fields(rpmQuery(t, rpmBin, fx.file, "[%{FILENAMES} ]"))
			if len(files) != len(pkg.Files) {
				t.Fatalf("parsed %d files, rpm lists %d", len(pkg.Files), len(files))
			}

			for i, f := range pkg.Files {
				if f.Path != files[i] {
					t.Errorf("file %d = %q, rpm says %q", i, f.Path, files[i])
				}
			}
		})
	}
}

func TestHeaderGetNumber(t *testing.T) {
	var c craft

	c.int32s(1, -2) // 0xFFFFFFFE: read as unsigned
	c.entries = append(c.entries, TagEntry{Tag: 2, Type: TagTypeINT64, Offset: int32(c.data.Len()), Count: 1})
	c.data.Write([]byte{0, 0, 0, 1, 0, 0, 0, 0})
	c.entries = append(c.entries, TagEntry{Tag: 3, Type: TagTypeINT64, Offset: int32(c.data.Len()), Count: 1})
	c.data.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})

	h := &Header{Data: c.data.Bytes(), Entries: c.entries}

	if v, ok := h.GetNumber(1); !ok || v != 0xFFFFFFFE {
		t.Errorf("INT32 = %d,%v", v, ok)
	}

	if v, ok := h.GetNumber(2); !ok || v != 1<<32 {
		t.Errorf("INT64 = %d,%v", v, ok)
	}

	if _, ok := h.GetNumber(3); ok {
		t.Error("negative INT64 must be rejected")
	}

	if _, ok := h.GetNumber(99); ok {
		t.Error("absent tag reported present")
	}
}

// TestExtraDataCarriesSizes: sizes persisted by MarshalExtra override the
// installed_size/archive_size columns on read (older rows hold 0 there), while
// extra data written before sizes were stored leaves the columns alone and is
// flagged for a backfill.
func TestExtraDataCarriesSizes(t *testing.T) {
	src := &Package{InstalledSize: 100012, ArchiveSize: 100696}

	extra, err := src.MarshalExtra()
	if err != nil {
		t.Fatal(err)
	}

	row := &Package{InstalledSize: 0, ArchiveSize: 0} // stale columns

	if err := row.UnmarshalExtra(extra); err != nil {
		t.Fatal(err)
	}

	if !row.SizesParsed || row.InstalledSize != 100012 || row.ArchiveSize != 100696 {
		t.Errorf("sizes after round trip = %d/%d parsed=%v, want 100012/100696 parsed", row.InstalledSize, row.ArchiveSize, row.SizesParsed)
	}

	legacy := &Package{InstalledSize: 7, ArchiveSize: 9}

	if err := legacy.UnmarshalExtra([]byte(`{"weak_deps":true}`)); err != nil {
		t.Fatal(err)
	}

	if legacy.SizesParsed || legacy.InstalledSize != 7 || legacy.ArchiveSize != 9 {
		t.Errorf(
			"legacy extra = %d/%d parsed=%v, want the columns untouched and not parsed",
			legacy.InstalledSize,
			legacy.ArchiveSize,
			legacy.SizesParsed,
		)
	}
}
