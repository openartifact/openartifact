// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"
)

// RepackError is a module archive the proxy refuses to repack: malformed,
// unsafe (an entry that escapes the archive root, a link, a device file), over
// the same limits an uploaded module is validated against, or without the
// directory that was asked for. Any other error from RepackModuleArchive is a
// failure to write the output.
type RepackError struct{ msg string }

func (e *RepackError) Error() string { return e.msg }

// IsRepackError reports whether err is a RepackError.
func IsRepackError(err error) bool {
	_, ok := errors.AsType[*RepackError](err)

	return ok
}

// repackPlan says which entries of an archive are kept and how their names are
// rewritten.
type repackPlan struct {
	strip  bool   // drop the single top-level directory of the archive
	subdir string // keep only the entries below this directory ("" = all)

	top    string
	topSet bool
}

// rel maps the cleaned name of an entry to its name in the output. keep is
// false for entries that are dropped: the wrapper directory itself, the
// directory selected by subdir, and everything outside it.
func (p *repackPlan) rel(clean string, isDir bool) (rel string, keep bool, err error) {
	rel = clean

	if p.strip {
		first, rest, nested := strings.Cut(clean, "/")
		if !nested && !isDir {
			return "", false, malformed("the file %q sits next to the top-level directory of the archive", clean)
		}

		if p.topSet && first != p.top {
			return "", false, malformed("the archive has more than one top-level entry (%q and %q)", p.top, first)
		}

		p.top, p.topSet = first, true

		if !nested {
			return "", false, nil
		}

		rel = rest
	}

	if p.subdir == "" {
		return rel, true, nil
	}

	if rel == p.subdir {
		if !isDir {
			return "", false, malformed("the selected subdirectory %q is a file", p.subdir)
		}

		return "", false, nil
	}

	rest, below := strings.CutPrefix(rel, p.subdir+"/")
	if !below {
		return "", false, nil
	}

	return rest, true, nil
}

// repackFailure turns the error of a copy into the kind the caller must see: a write
// error of the output is returned as it is, anything else came from reading
// the archive and is a RepackError.
func repackFailure(out *errWriter, format string, args ...any) error {
	if out.err != nil {
		return out.err
	}

	return &RepackError{msg: fmt.Sprintf(format, args...)}
}

// errWriter remembers the first error its destination returned.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil && e.err == nil {
		e.err = err
	}

	return n, err
}

// RepackModuleArchive writes to dst a copy of the module archive in src (a zip
// or a gzip-compressed tar, told apart by their magic bytes) that holds only
// what a Terraform client should extract as the module directory.
//
// With stripWrapper the single top-level directory of the archive (the
// "repo-<sha>/" of a source tarball) is removed from every name; the archive
// must have exactly one. A non-empty subdir keeps only the entries below that
// directory, relative to the archive root, or to the wrapper directory when
// stripWrapper is set, and removes the prefix. The output has the kind of the
// input, keeps the order of the entries and, for a gzip tar, is the same bytes
// for the same input.
//
// The input is read with the limits an uploaded module is validated against
// (entry count, expanded size, expansion ratio, entry names, no links or
// special files), counted over every entry of the archive, not only the kept
// ones. Refused input is a RepackError; the output may be partly written then.
func RepackModuleArchive(dst io.Writer, src io.ReaderAt, size int64, stripWrapper bool, subdir string) error {
	if subdir != "" {
		if clean, err := cleanEntryName(subdir); err != nil || clean != subdir {
			return &RepackError{msg: fmt.Sprintf("subdirectory %q is not a clean relative path", subdir)}
		}
	}

	plan := &repackPlan{strip: stripWrapper, subdir: subdir}
	out := &errWriter{w: dst}

	head := make([]byte, 4)
	n, _ := src.ReadAt(head, 0)

	var err error

	switch detectArchive(head[:n]) {
	case extZip:
		err = repackZip(out, src, size, plan)
	case extTarGz:
		err = repackTarGz(out, io.NewSectionReader(src, 0, size), size, plan)
	default:
		err = malformed("the body is neither a zip nor a gzip-compressed tar archive")
	}

	if ae, ok := errors.AsType[*archiveError](err); ok {
		return &RepackError{msg: ae.msg}
	}

	return err
}

// repackTarGz is RepackModuleArchive for a gzip tar. Names are checked and
// counted by the same entrySet an upload goes through.
func repackTarGz(out *errWriter, src io.Reader, compressed int64, plan *repackPlan) error {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return malformed("not a readable gzip stream: %v", err)
	}
	defer func() { _ = gz.Close() }()

	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)
	set := newEntrySet(archiveLimits{expanded: maxModuleExpanded}, compressed)
	tr := tar.NewReader(gz)
	kept := 0

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return malformed("not a readable tar archive: %v", err)
		}

		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}

		isDir := hdr.Typeflag == tar.TypeDir
		if !isDir && hdr.Typeflag != tar.TypeReg {
			return malformed("entry %q is a link or special file", hdr.Name)
		}

		if !isDir && hdr.Size < 0 {
			return malformed("entry %q has a negative size", hdr.Name)
		}

		clean, err := set.add(hdr.Name, isDir, hdr.Size)
		if err != nil {
			return err
		}

		if clean == "" {
			continue
		}

		name, keep, err := plan.rel(clean, isDir)
		if err != nil {
			return err
		}

		if !keep {
			continue
		}

		th := &tar.Header{
			Typeflag: hdr.Typeflag,
			Name:     name,
			Mode:     hdr.Mode & 0o777,
			ModTime:  hdr.ModTime.Truncate(time.Second),
		}

		if isDir {
			th.Name += "/"
		} else {
			th.Size = hdr.Size
			kept++
		}

		if err := tw.WriteHeader(th); err != nil {
			return err
		}

		if isDir {
			continue
		}

		if _, err := io.Copy(tw, tr); err != nil {
			return repackFailure(out, "an entry is corrupt: %v", err)
		}
	}

	if kept == 0 {
		return plan.empty()
	}

	// Read the gzip trailer, bounded by what the limits could still allow, so a
	// truncated or padded stream is refused.
	room := set.limits.expanded - set.total + 1<<20
	if _, err := io.Copy(io.Discard, io.LimitReader(gz, room)); err != nil {
		return malformed("the gzip stream is corrupt: %v", err)
	}

	if err := tw.Close(); err != nil {
		return err
	}

	return gw.Close()
}

// repackZip is RepackModuleArchive for a zip.
func repackZip(out *errWriter, src io.ReaderAt, size int64, plan *repackPlan) error {
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return malformed("not a readable zip archive: %v", err)
	}

	if len(zr.File) > maxEntries {
		return malformed("more than %d entries", maxEntries)
	}

	set := newEntrySet(archiveLimits{expanded: maxModuleExpanded}, size)
	zw := zip.NewWriter(out)
	kept := 0

	for _, f := range zr.File {
		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 || mode&(fs.ModeDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeCharDevice) != 0 {
			return malformed("entry %q is not a regular file or directory", f.Name)
		}

		isDir := f.FileInfo().IsDir()

		clean, err := set.add(f.Name, isDir, int64(min(f.UncompressedSize64, uint64(maxEntryBytes)+1)))
		if err != nil {
			return err
		}

		if clean == "" {
			continue
		}

		name, keep, err := plan.rel(clean, isDir)
		if err != nil {
			return err
		}

		if !keep {
			continue
		}

		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: f.Modified.UTC().Truncate(2 * time.Second)}
		if isDir {
			hdr.Name += "/"
			hdr.SetMode(fs.ModeDir | mode.Perm())
		} else {
			hdr.SetMode(mode.Perm())
			kept++
		}

		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}

		if isDir {
			continue
		}

		if err := copyZipEntry(w, f); err != nil {
			return repackFailure(out, "%v", err)
		}
	}

	if kept == 0 {
		return plan.empty()
	}

	return zw.Close()
}

// copyZipEntry copies one zip entry, checking it has the size it declares.
func copyZipEntry(w io.Writer, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("entry %q cannot be read: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	want := int64(f.UncompressedSize64) //nolint:gosec // bounded by maxEntryBytes in set.add

	n, err := io.Copy(w, io.LimitReader(rc, want+1))
	if err != nil {
		return fmt.Errorf("entry %q is corrupt: %w", f.Name, err)
	}

	if n != want {
		return fmt.Errorf("entry %q does not match its declared size", f.Name)
	}

	return nil
}

// empty is the error of a plan that kept no file.
func (p *repackPlan) empty() error {
	switch {
	case p.subdir != "":
		return malformed("the archive has no files in the directory %q", p.subdir)
	case p.strip:
		return malformed("the archive has no files in its top-level directory")
	}

	return malformed("the archive has no files")
}
