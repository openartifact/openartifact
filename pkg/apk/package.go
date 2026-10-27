// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package apk implements the Alpine Linux package format: it parses and
// verifies .apk (v2) files, reads and renders APKINDEX indexes, and builds
// indexes signed the way abuild signs them.
//
// An .apk file is two or three concatenated gzip members: an optional
// signature segment (a tar whose entry is named .SIGN.RSA[256].<key>), the
// control segment (a tar that starts with .PKGINFO) and the data segment (the
// files). abuild leaves the end-of-archive blocks off the signature and
// control tars so a client can read the three segments as one tar stream.
package apk

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha1" //nolint:gosec // the index "C:" checksum is defined as the SHA-1 of the control segment
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
)

// ErrInvalidPackage is wrapped by every error ParsePackage returns.
var ErrInvalidPackage = errors.New("invalid apk package")

// Package is a verified .apk and also one APKINDEX entry.
type Package struct {
	Name, Version, Arch                                   string
	Description, URL, License, Origin, Maintainer, Commit string
	// BuildTime is the PKGINFO builddate (index field t), InstalledSize the
	// PKGINFO size (index field I).
	BuildTime, InstalledSize int64
	Depends, Provides        []string
	InstallIf                []string
	ProviderPriority         int
	// DataHash is the lowercase hex SHA-256 of the data segment (PKGINFO
	// datahash); empty for index entries.
	DataHash string
	// Checksum is the index "C:" value: "Q1" + base64(SHA-1 of the control
	// segment).
	Checksum string
	// Size is the size of the .apk file (index field S).
	Size int64
	// Signed reports that the file starts with an abuild signature segment. The
	// signature is not verified.
	Signed bool
}

// Limits of ParsePackage. They bound the memory and CPU an uploader controls.
const (
	maxControlCompressed   = 4 << 20
	maxControlDecompressed = 8 << 20
	maxPKGINFO             = 1 << 20
	maxControlEntries      = 1000
	maxDataDecompressed    = 8 << 30
	maxDataEntries         = 1_000_000

	sigPrefix   = ".SIGN."
	pkginfoName = ".PKGINFO"
)

type packageLimits struct {
	controlCompressed   int64
	controlDecompressed int64
	pkginfo             int64
	controlEntries      int
	dataDecompressed    int64
	dataEntries         int
}

func defaultPackageLimits() packageLimits {
	return packageLimits{
		controlCompressed:   maxControlCompressed,
		controlDecompressed: maxControlDecompressed,
		pkginfo:             maxPKGINFO,
		controlEntries:      maxControlEntries,
		dataDecompressed:    maxDataDecompressed,
		dataEntries:         maxDataEntries,
	}
}

// ParsePackage parses and verifies an .apk of size bytes: it locates the gzip
// members exactly, reads the control segment, checks that the PKGINFO datahash
// is the SHA-256 of the data segment, fully decompresses the data segment
// (verifying its gzip checksum) and refuses entries that are absolute or climb
// out of their directory. Every error wraps [ErrInvalidPackage].
func ParsePackage(r io.ReaderAt, size int64) (*Package, error) {
	return parsePackage(r, size, defaultPackageLimits())
}

func parsePackage(ra io.ReaderAt, size int64, lim packageLimits) (*Package, error) {
	if size <= 0 {
		return nil, bad("empty file")
	}

	control, signed, err := readHead(ra, size, lim)
	if err != nil {
		return nil, err
	}

	pkginfo, err := controlPKGINFO(control.raw, lim)
	if err != nil {
		return nil, err
	}

	pkg, err := parsePKGINFO(pkginfo)
	if err != nil {
		return nil, err
	}

	sum := sha1.New() //nolint:gosec // see the import
	if err := hashRange(sum, ra, control.start, control.end); err != nil {
		return nil, err
	}

	pkg.Checksum = "Q1" + base64.StdEncoding.EncodeToString(sum.Sum(nil))
	pkg.Size = size
	pkg.Signed = signed

	if err := pkg.validate(); err != nil {
		return nil, bad("%v", err)
	}

	if err := verifyData(ra, control.end, size, pkg.DataHash, lim); err != nil {
		return nil, err
	}

	return pkg, nil
}

// bad returns an error that wraps ErrInvalidPackage.
func bad(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPackage, fmt.Sprintf(format, args...))
}

// short shortens attacker-controlled text for error messages.
func short(s string) string {
	const limit = 64
	if len(s) > limit {
		return s[:limit] + "..."
	}

	return s
}

// member is one decompressed gzip member and the byte range it occupies.
type member struct {
	raw        []byte
	start, end int64
}

// readHead reads the segments in front of the data segment and returns the
// control member. The first member is the signature when its tar starts with a
// .SIGN. entry and the control otherwise.
func readHead(ra io.ReaderAt, size int64, lim packageLimits) (member, bool, error) {
	first, err := readMember(ra, 0, size, lim)
	if err != nil {
		return member{}, false, err
	}

	name, err := firstEntryName(first.raw)
	if err != nil {
		return member{}, false, err
	}

	if !strings.HasPrefix(name, sigPrefix) {
		return first, false, nil
	}

	if err := checkSignatureTar(first.raw, lim); err != nil {
		return member{}, false, err
	}

	if first.end >= size {
		return member{}, false, bad("no control segment after the signature")
	}

	control, err := readMember(ra, first.end, size, lim)
	if err != nil {
		return member{}, false, err
	}

	return control, true, nil
}

// readMember decompresses the gzip member at start into memory, within the
// control segment limits. It never reads past the member: the returned end is
// the offset of the first byte after its gzip trailer.
func readMember(ra io.ReaderAt, start, size int64, lim packageLimits) (member, error) {
	window := min(size-start, lim.controlCompressed)
	sr := io.NewSectionReader(ra, start, window)
	br := bufio.NewReader(sr)

	gz, err := newMemberReader(br)
	if err != nil {
		return member{}, bad("segment at offset %d is not gzip: %v", start, err)
	}

	raw, err := io.ReadAll(io.LimitReader(gz, lim.controlDecompressed+1))
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) && window < size-start {
			return member{}, bad("segment at offset %d exceeds %d compressed bytes", start, lim.controlCompressed)
		}

		return member{}, bad("segment at offset %d: %v", start, err)
	}

	if int64(len(raw)) > lim.controlDecompressed {
		return member{}, bad("segment at offset %d exceeds %d decompressed bytes", start, lim.controlDecompressed)
	}

	return member{raw: raw, start: start, end: memberEnd(sr, br, start)}, nil
}

// newMemberReader opens the gzip member at the current position of br. br is
// a byte reader, so the decompressor does not read past the member's trailer.
func newMemberReader(br *bufio.Reader) (*gzip.Reader, error) {
	gz, err := gzip.NewReader(br)
	if err != nil {
		return nil, err
	}

	gz.Multistream(false)

	return gz, nil
}

// memberEnd returns the offset in the underlying file of the first byte br has
// not handed out yet.
func memberEnd(sr *io.SectionReader, br *bufio.Reader, start int64) int64 {
	read, _ := sr.Seek(0, io.SeekCurrent)

	return start + read - int64(br.Buffered())
}

// firstEntryName returns the name of the first entry of an in-memory tar.
func firstEntryName(raw []byte) (string, error) {
	hdr, err := tar.NewReader(bytes.NewReader(raw)).Next()
	if err != nil {
		return "", bad("first segment holds no tar entry: %v", err)
	}

	return hdr.Name, nil
}

// checkSignatureTar requires every entry of the signature segment to be a
// signature.
func checkSignatureTar(raw []byte, lim packageLimits) error {
	tr := tar.NewReader(bytes.NewReader(raw))

	for n := 1; ; n++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return bad("signature segment: %v", err)
		}

		if n > lim.controlEntries {
			return bad("signature segment holds more than %d entries", lim.controlEntries)
		}

		if !strings.HasPrefix(hdr.Name, sigPrefix) {
			return bad("signature segment entry %q is not a signature", short(hdr.Name))
		}
	}
}

// controlPKGINFO walks the control tar and returns the content of its first
// entry, which must be .PKGINFO. The tar may or may not end with the
// end-of-archive blocks.
func controlPKGINFO(raw []byte, lim packageLimits) ([]byte, error) {
	tr := tar.NewReader(bytes.NewReader(raw))

	var (
		pkginfo []byte
		found   bool
	)

	for n := 1; ; n++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, bad("control segment: %v", err)
		}

		if n > lim.controlEntries {
			return nil, bad("control segment holds more than %d entries", lim.controlEntries)
		}

		switch {
		case n == 1:
			pkginfo, err = readPKGINFO(tr, hdr, lim)
			if err != nil {
				return nil, err
			}

			found = true
		case hdr.Name == pkginfoName:
			return nil, bad("control segment holds more than one %s", pkginfoName)
		}
	}

	if !found {
		return nil, bad("control segment holds no entries")
	}

	return pkginfo, nil
}

func readPKGINFO(tr *tar.Reader, hdr *tar.Header, lim packageLimits) ([]byte, error) {
	if hdr.Name != pkginfoName {
		return nil, bad("control segment starts with %q, not %s", short(hdr.Name), pkginfoName)
	}

	if hdr.Typeflag != tar.TypeReg {
		return nil, bad("%s is not a regular file", pkginfoName)
	}

	if hdr.Size > lim.pkginfo {
		return nil, bad("%s exceeds %d bytes", pkginfoName, lim.pkginfo)
	}

	pkginfo, err := io.ReadAll(tr)
	if err != nil {
		return nil, bad("reading %s: %v", pkginfoName, err)
	}

	return pkginfo, nil
}

// hashRange feeds the bytes [start, end) of ra to h.
func hashRange(h hash.Hash, ra io.ReaderAt, start, end int64) error {
	if _, err := io.Copy(h, io.NewSectionReader(ra, start, end-start)); err != nil {
		return bad("hashing segment at offset %d: %v", start, err)
	}

	return nil
}

// verifyData checks the data segment [start, size): its SHA-256 must be the
// PKGINFO datahash, it must be exactly one gzip member that decompresses
// within the cap with a valid checksum, and its tar entries must have names
// that stay inside the install root.
func verifyData(ra io.ReaderAt, start, size int64, datahash string, lim packageLimits) error {
	if start >= size {
		return bad("no data segment")
	}

	sum := sha256.New()
	if err := hashRange(sum, ra, start, size); err != nil {
		return err
	}

	if hex.EncodeToString(sum.Sum(nil)) != datahash {
		return bad("datahash does not match the data segment")
	}

	end, err := walkData(ra, start, size, lim)
	if err != nil {
		return err
	}

	if end != size {
		return bad("%d bytes after the data segment", size-end)
	}

	return nil
}

// walkData decompresses the gzip member at start to nowhere, walking its tar
// headers, and returns the offset after the member.
func walkData(ra io.ReaderAt, start, size int64, lim packageLimits) (int64, error) {
	const bufSize = 64 << 10

	sr := io.NewSectionReader(ra, start, size-start)
	br := bufio.NewReaderSize(sr, bufSize)

	gz, err := newMemberReader(br)
	if err != nil {
		return 0, bad("data segment is not gzip: %v", err)
	}

	cr := &capReader{r: gz, left: lim.dataDecompressed}
	tr := tar.NewReader(cr)

	for n := 1; ; n++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return 0, bad("data segment: %v", err)
		}

		if n > lim.dataEntries {
			return 0, bad("data segment holds more than %d entries", lim.dataEntries)
		}

		if !safeEntryName(hdr.Name) {
			return 0, bad("data segment entry %q escapes the install root", short(hdr.Name))
		}
	}

	// The tar reader stops at the end-of-archive blocks; the rest of the
	// member (and its checksum) is read here.
	if _, err := io.Copy(io.Discard, cr); err != nil {
		return 0, bad("data segment: %v", err)
	}

	return memberEnd(sr, br, start), nil
}

// safeEntryName reports whether a tar entry name is relative and free of ".."
// components.
func safeEntryName(name string) bool {
	if name == "" || name[0] == '/' || strings.IndexByte(name, 0) >= 0 {
		return false
	}

	for part := range strings.SplitSeq(name, "/") {
		if part == ".." {
			return false
		}
	}

	return true
}

// errTooLarge is returned by capReader once its budget is spent.
var errTooLarge = errors.New("decompressed data exceeds the limit")

// capReader fails with errTooLarge when more than left bytes are read; it
// reports a clean io.EOF when the source ends within the budget.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}

	n, err := c.r.Read(p)
	if int64(n) > c.left {
		c.left = 0

		return 0, errTooLarge
	}

	c.left -= int64(n)

	return n, err
}
