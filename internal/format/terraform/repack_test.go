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
	"io"
	"slices"
	"strings"
	"testing"
)

// entriesOf lists a repacked archive as name -> body, whatever its kind.
func entriesOf(tb testing.TB, data []byte) map[string]string {
	tb.Helper()

	got := map[string]string{}

	switch detectArchive(data) {
	case extZip:
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			tb.Fatal(err)
		}

		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				tb.Fatal(err)
			}

			body, err := io.ReadAll(rc)
			_ = rc.Close()

			if err != nil {
				tb.Fatal(err)
			}

			got[f.Name] = string(body)
		}
	case extTarGz:
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			tb.Fatal(err)
		}

		tr := tar.NewReader(gz)

		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return got
			}

			if err != nil {
				tb.Fatal(err)
			}

			body, err := io.ReadAll(tr)
			if err != nil {
				tb.Fatal(err)
			}

			got[hdr.Name] = string(body)
		}
	default:
		tb.Fatalf("not an archive: %q", data[:min(len(data), 8)])
	}

	return got
}

func repack(data []byte, strip bool, subdir string) ([]byte, error) {
	var out bytes.Buffer

	err := RepackModuleArchive(&out, bytes.NewReader(data), int64(len(data)), strip, subdir)

	return out.Bytes(), err
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

func repoTarball(tb testing.TB, wrapper string) []byte {
	tb.Helper()

	return tarGzOf(tb,
		tarEntry{name: wrapper + "/", typeflag: tar.TypeDir},
		tarEntry{name: wrapper + "/main.tf", body: "root"},
		tarEntry{name: wrapper + "/modules/", typeflag: tar.TypeDir},
		tarEntry{name: wrapper + "/modules/x/", typeflag: tar.TypeDir},
		tarEntry{name: wrapper + "/modules/x/main.tf", body: "x"},
		tarEntry{name: wrapper + "/modules/y/main.tf", body: "y"},
	)
}

func TestRepackStripsTheWrapper(t *testing.T) {
	out, err := repack(repoTarball(t, "repo-abc"), true, "")
	if err != nil {
		t.Fatal(err)
	}

	got := entriesOf(t, out)
	want := []string{"main.tf", "modules/", "modules/x/", "modules/x/main.tf", "modules/y/main.tf"}

	if keys := keysOf(got); !slices.Equal(keys, want) || got["main.tf"] != "root" {
		t.Fatalf("entries %v", got)
	}

	// The same input is the same bytes, and another wrapper name does not show.
	again, err := repack(repoTarball(t, "repo-def"), true, "")
	if err != nil || !bytes.Equal(out, again) {
		t.Fatalf("repack is not deterministic across wrapper names: %v", err)
	}

	// What comes out is itself a valid module archive.
	if _, err := validateModuleArchive(bytes.NewReader(out), int64(len(out))); err != nil {
		t.Fatalf("the repacked archive does not validate: %v", err)
	}
}

func TestRepackSubdir(t *testing.T) {
	out, err := repack(repoTarball(t, "repo-abc"), true, "modules/x")
	if err != nil {
		t.Fatal(err)
	}

	if got := entriesOf(t, out); len(got) != 1 || got["main.tf"] != "x" {
		t.Fatalf("entries %v", got)
	}

	// Without the wrapper selector the subdirectory is relative to the root.
	out, err = repack(repoTarball(t, "repo-abc"), false, "repo-abc/modules")
	if err != nil {
		t.Fatal(err)
	}

	if keys := keysOf(entriesOf(t, out)); !slices.Equal(keys, []string{"x/", "x/main.tf", "y/main.tf"}) {
		t.Fatalf("entries %v", keys)
	}

	// A sibling whose name starts with the subdirectory is not inside it.
	tgz := tarGzOf(t, tarEntry{name: "a/mod/main.tf", body: "in"}, tarEntry{name: "a/module2/main.tf", body: "out"})
	if out, err = repack(tgz, true, "mod"); err != nil {
		t.Fatal(err)
	}

	if got := entriesOf(t, out); len(got) != 1 || got["main.tf"] != "in" {
		t.Fatalf("entries %v", got)
	}
}

func TestRepackZip(t *testing.T) {
	data := zipOf(t,
		[2]string{"wrap/", ""},
		[2]string{"wrap/sub/main.tf", "main"},
		[2]string{"wrap/sub/lib/a.tf", "a"},
		[2]string{"wrap/other.tf", "o"},
	)

	out, err := repack(data, false, "wrap/sub")
	if err != nil {
		t.Fatal(err)
	}

	if detectArchive(out) != extZip {
		t.Fatalf("the output of a zip is %q", detectArchive(out))
	}

	got := entriesOf(t, out)
	if keys := keysOf(got); !slices.Equal(keys, []string{"lib/a.tf", "main.tf"}) || got["lib/a.tf"] != "a" {
		t.Fatalf("entries %v", got)
	}

	out, err = repack(data, true, "sub")
	if err != nil {
		t.Fatal(err)
	}

	if keys := keysOf(entriesOf(t, out)); !slices.Equal(keys, []string{"lib/a.tf", "main.tf"}) {
		t.Fatalf("stripped entries %v", keys)
	}
}

func TestRepackRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   []byte
		strip  bool
		subdir string
	}{
		{"traversal", tarGzOf(t, tarEntry{name: "r/main.tf", body: "x"}, tarEntry{name: "r/../../evil", body: "x"}), true, ""},
		{"absolute name", tarGzOf(t, tarEntry{name: "/etc/x", body: "x"}), false, ""},
		{"backslash", tarGzOf(t, tarEntry{name: `r\x`, body: "x"}), false, ""},
		{"symlink", tarGzOf(t, tarEntry{name: "r/main.tf", body: "x"}, tarEntry{name: "r/l", typeflag: tar.TypeSymlink, link: "main.tf"}), true, ""},
		{"hard link", tarGzOf(t, tarEntry{name: "r/main.tf", body: "x"}, tarEntry{name: "r/l", typeflag: tar.TypeLink, link: "r/main.tf"}), true, ""},
		{"device", tarGzOf(t, tarEntry{name: "r/null", typeflag: tar.TypeChar}), true, ""},
		{"duplicate", tarGzOf(t, tarEntry{name: "r/a", body: "1"}, tarEntry{name: "r/a", body: "2"}), true, ""},
		{"two wrappers", tarGzOf(t, tarEntry{name: "a/x", body: "1"}, tarEntry{name: "b/y", body: "2"}), true, ""},
		{"file at the root", tarGzOf(t, tarEntry{name: "x", body: "1"}), true, ""},
		{"missing subdirectory", repoTarball(t, "r"), true, "nope"},
		{"subdirectory is a file", repoTarball(t, "r"), true, "main.tf"},
		{"no files", tarGzOf(t, tarEntry{name: "r/", typeflag: tar.TypeDir}), true, ""},
		{"not an archive", []byte("<html>"), true, ""},
		{"truncated gzip", repoTarball(t, "r")[:40], true, ""},
		{"zip traversal", zipOf(t, [2]string{"../x", "x"}), false, ""},
		{"unclean subdirectory", repoTarball(t, "r"), true, "a/../b"},
		{"absolute subdirectory", repoTarball(t, "r"), true, "/modules"},
		{"subdirectory with backslash", repoTarball(t, "r"), true, `modules\x`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := repack(tc.data, tc.strip, tc.subdir)
			if err == nil || !IsRepackError(err) {
				t.Fatalf("error %v (%T), want a RepackError", err, err)
			}
		})
	}
}

type failingWriter struct{}

var errDiskFull = errors.New("disk full")

func (failingWriter) Write([]byte) (int, error) { return 0, errDiskFull }

func TestRepackReportsAWriteFailureAsIs(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	tgz := tarGzOf(t, tarEntry{name: "r/a.tf", body: big})

	err := RepackModuleArchive(failingWriter{}, bytes.NewReader(tgz), int64(len(tgz)), true, "")
	if !errors.Is(err, errDiskFull) || IsRepackError(err) {
		t.Fatalf("error %v (%T), want the write error and no RepackError", err, err)
	}

	zp := zipOf(t, [2]string{"r/a.tf", big})

	err = RepackModuleArchive(failingWriter{}, bytes.NewReader(zp), int64(len(zp)), true, "")
	if !errors.Is(err, errDiskFull) || IsRepackError(err) {
		t.Fatalf("zip: error %v (%T), want the write error and no RepackError", err, err)
	}
}

// FuzzRepackModuleArchive feeds arbitrary bytes, selectors and subdirectories
// to the repacker: it must never panic and must either refuse with a
// RepackError or produce an archive of the same kind whose names are clean,
// that validates as a module archive's entries do, and that repacks to itself.
func FuzzRepackModuleArchive(f *testing.F) {
	f.Add(repoTarball(f, "repo-abc"), true, "modules/x")
	f.Add(repoTarball(f, "repo-abc"), false, "")
	f.Add(repoTarball(f, "r"), true, "")
	f.Add(zipOf(f, [2]string{"wrap/sub/main.tf", "m"}, [2]string{"wrap/o.tf", "o"}), false, "wrap/sub")
	f.Add(zipOf(f, [2]string{"a/main.tf", "m"}), true, "")
	f.Add(tarGzOf(f, tarEntry{name: "r/../x", body: "x"}), true, "")
	f.Add(tarGzOf(f, tarEntry{name: "r/l", typeflag: tar.TypeSymlink, link: "x"}), true, "")
	f.Add([]byte("PK\x03\x04"), false, "a")
	f.Add([]byte("\x1f\x8b\x08\x00"), true, "../a")
	f.Add([]byte{}, false, "")

	f.Fuzz(func(t *testing.T, data []byte, strip bool, subdir string) {
		out, err := repack(data, strip, subdir)
		if err != nil {
			if !IsRepackError(err) {
				t.Fatalf("error %T (%v) is not a RepackError", err, err)
			}

			return
		}

		if detectArchive(out) != detectArchive(data) {
			t.Fatalf("input kind %q, output kind %q", detectArchive(data), detectArchive(out))
		}

		set := newEntrySet(archiveLimits{expanded: maxModuleExpanded}, int64(len(out)))

		switch detectArchive(out) {
		case extZip:
			err = readZip(bytes.NewReader(out), int64(len(out)), set)
		default:
			err = readTarGz(bytes.NewReader(out), set)
		}

		if err != nil {
			t.Fatalf("the output does not validate: %v", err)
		}

		if len(set.files) == 0 {
			t.Fatal("the output has no files")
		}

		for name := range set.names {
			if clean, err := cleanEntryName(name); err != nil || clean != name {
				t.Fatalf("output entry %q is not clean", name)
			}
		}

		again, err := repack(out, false, "")
		if err != nil {
			t.Fatalf("the output does not repack: %v", err)
		}

		if detectArchive(out) == extTarGz && !bytes.Equal(out, again) {
			t.Fatal("repacking the output changed it")
		}
	})
}
