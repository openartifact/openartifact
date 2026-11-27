// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// sourceFields and binaryFields are the columns R's available.packages reads
// from a repository index (tools:::.get_standard_repository_db_fields): it
// subscripts the PACKAGES.rds matrix by them, so every one must be present
// there, empty ones as NA.
var (
	sourceFields = []string{ //nolint:gochecknoglobals // constant table
		"Package", "Version", "Priority", "Depends", "Imports", "LinkingTo", "Suggests", "Enhances",
		"License", "License_is_FOSS", "License_restricts_use", "OS_type", "Archs", "MD5sum", "NeedsCompilation",
	}
	binaryFields = []string{ //nolint:gochecknoglobals // constant table
		"Package", "Version", "Priority", "Depends", "Imports", "LinkingTo", "Suggests", "Enhances",
		"License", "License_is_FOSS", "License_restricts_use", "OS_type", "Archs", "MD5sum", "Built",
	}
)

// StandardFields returns the index fields of a source (binary false) or binary
// tree, in the order a PACKAGES record lists them. The slice is shared.
func StandardFields(binary bool) []string {
	if binary {
		return binaryFields
	}

	return sourceFields
}

// StoredFields picks the index fields of a package out of its DESCRIPTION,
// folded to one line each and without the empty ones. Package, Version and
// MD5sum are not among them: the first two come from the package's identity and
// the checksum from the stored file.
func StoredFields(desc Record, binary bool) map[string]string {
	out := make(map[string]string, 12)

	for _, name := range StandardFields(binary) {
		switch name {
		case "Package", "Version", "MD5sum":
			continue
		}

		if v := Fold(desc.Get(name)); v != "" {
			out[name] = v
		}
	}

	return out
}

// IndexRecord is the PACKAGES record of one package file: identity, the stored
// fields and the MD5 of the file, in the standard field order.
func IndexRecord(pkg, version, md5 string, fields map[string]string, binary bool) Record {
	std := StandardFields(binary)
	rec := make(Record, 0, len(std))

	for _, name := range std {
		var v string

		switch name {
		case "Package":
			v = pkg
		case "Version":
			v = version
		case "MD5sum":
			v = md5
		default:
			v = fields[name]
		}

		if v != "" {
			rec = append(rec, Field{Name: name, Value: v})
		}
	}

	return rec
}

// ParsePackages reads a PACKAGES document. Every record must name a package and
// its version.
func ParsePackages(r io.Reader, limit int64) ([]Record, error) {
	var recs []Record

	err := ReadRecords(r, limit, func(rec Record) error {
		if rec.Get("Package") == "" || rec.Get("Version") == "" {
			return fmt.Errorf("%w: record without Package or Version", ErrSyntax)
		}

		recs = append(recs, rec)

		return nil
	})

	return recs, err
}

// RenderText is the PACKAGES document of recs: records in the given order,
// fields in the record's order, separated by blank lines. A value that spans
// lines is written with continuation lines.
func RenderText(recs []Record) []byte {
	var b bytes.Buffer

	for i, rec := range recs {
		if i > 0 {
			b.WriteByte('\n')
		}

		for _, f := range rec {
			b.WriteString(f.Name)
			b.WriteString(": ")
			b.WriteString(strings.ReplaceAll(f.Value, "\n", "\n    "))
			b.WriteByte('\n')
		}
	}

	return b.Bytes()
}

// Gzip compresses data as a gzip stream.
func Gzip(data []byte) ([]byte, error) {
	var b bytes.Buffer

	zw := gzip.NewWriter(&b)

	if _, err := zw.Write(data); err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}

	return b.Bytes(), nil
}

// Gunzip decompresses a gzip stream of at most limit decompressed bytes.
func Gunzip(r io.Reader, limit int64) ([]byte, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}

	defer func() { _ = zr.Close() }()

	data, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}

	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}

	return data, nil
}

// R serialization constants (src/main/serialize.c).
const (
	rtypeSym     = 1
	rtypeList    = 2
	rtypeChar    = 9
	rtypeInt     = 13
	rtypeStr     = 16
	rtypeVec     = 19
	rflagAttr    = 1 << 9
	rflagTag     = 1 << 10
	rlevelUTF8   = 8 << 12
	rlevelASCII  = 64 << 12
	rNilValue    = 254
	rWriterVer   = 4<<16 | 6<<8 | 2 // R 4.6.2
	rMinReaderV  = 2<<16 | 3<<8     // R 2.3.0
	rSerialVer   = 2
	rNAStringLen = -1
)

// RenderRDS is PACKAGES.rds for recs: a gzip-compressed version 2 XDR
// serialization of a character matrix (one row per record, one column per
// field, NA for a missing field) with dimnames list(NULL, fields), which is what
// R's write_PACKAGES saves and available.packages reads. The columns are the
// standard fields of the tree followed by every other field the records
// carry, in order of first appearance.
func RenderRDS(recs []Record, binary bool) ([]byte, error) {
	cols := slices.Clone(StandardFields(binary))
	index := make(map[string]int, len(cols))

	for i, c := range cols {
		index[c] = i
	}

	for _, rec := range recs {
		for _, f := range rec {
			if _, ok := index[f.Name]; !ok {
				index[f.Name] = len(cols)
				cols = append(cols, f.Name)
			}
		}
	}

	cells := make([]*string, len(recs)*len(cols))

	for row, rec := range recs {
		for _, f := range rec {
			v := f.Value
			cells[index[f.Name]*len(recs)+row] = &v // column-major
		}
	}

	var w xdr

	w.raw("X\n")
	w.int(rSerialVer)
	w.int(rWriterVer)
	w.int(rMinReaderV)

	// character vector with attributes: dim, dimnames
	w.int(rtypeStr | rflagAttr)
	w.int(len(cells))

	for _, c := range cells {
		w.charsxp(c)
	}

	w.int(rtypeList | rflagTag)
	w.symbol("dim")
	w.int(rtypeInt)
	w.int(2)
	w.int(len(recs))
	w.int(len(cols))

	w.int(rtypeList | rflagTag)
	w.symbol("dimnames")
	w.int(rtypeVec)
	w.int(2)
	w.int(rNilValue)
	w.int(rtypeStr)
	w.int(len(cols))

	for _, c := range cols {
		w.charsxp(&c)
	}

	w.int(rNilValue) // end of the attribute pairlist

	return Gzip(w.buf.Bytes())
}

// xdr writes R's big-endian serialization primitives.
type xdr struct {
	buf bytes.Buffer
}

func (x *xdr) raw(s string) { x.buf.WriteString(s) }

func (x *xdr) int(v int) {
	var b [4]byte

	binary.BigEndian.PutUint32(b[:], uint32(int32(v))) //nolint:gosec // R integers are 32-bit; sizes are far below that
	x.buf.Write(b[:])
}

// charsxp writes one string element; nil is NA_character_.
func (x *xdr) charsxp(s *string) {
	if s == nil {
		x.int(rtypeChar)
		x.int(rNAStringLen)

		return
	}

	flags := rtypeChar

	switch {
	case isASCII(*s):
		flags |= rlevelASCII
	case utf8.ValidString(*s):
		flags |= rlevelUTF8
	}

	x.int(flags)
	x.int(len(*s))
	x.buf.WriteString(*s)
}

func (x *xdr) symbol(name string) {
	x.int(rtypeSym)
	x.int(rtypeChar | rlevelASCII)
	x.int(len(name))
	x.buf.WriteString(name)
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}

	return true
}
