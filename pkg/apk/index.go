// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package apk

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
)

// ErrInvalidIndex is wrapped by every error ParseIndex and BuildIndex return
// for malformed or over-limit input.
var ErrInvalidIndex = errors.New("invalid apk index")

// Limits of ParseIndex.
const (
	maxIndexBytes   = 512 << 20
	maxIndexEntries = 2_000_000
	maxIndexLine    = 1 << 20
	maxIndexMembers = 16
	maxIndexTarEnts = 1000

	indexFileName = "APKINDEX"
	descFileName  = "DESCRIPTION"
)

type indexLimits struct {
	bytes   int64
	entries int
	line    int
	members int
}

func defaultIndexLimits() indexLimits {
	return indexLimits{bytes: maxIndexBytes, entries: maxIndexEntries, line: maxIndexLine, members: maxIndexMembers}
}

func badIndex(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidIndex, fmt.Sprintf(format, args...))
}

// RenderIndex renders packages as APKINDEX text: one stanza per package, in
// the order given, each ending with a blank line. Fields that are empty or zero
// are left out, except the mandatory C, P, V and A. Control characters in a
// value are replaced by a space so that a value can never start a new line or
// field; [BuildIndex] refuses such packages outright instead.
func RenderIndex(pkgs []Package) []byte {
	var b bytes.Buffer

	for i := range pkgs {
		writeStanza(&b, &pkgs[i])
	}

	return b.Bytes()
}

func writeStanza(b *bytes.Buffer, p *Package) {
	putField(b, 'C', p.Checksum)
	putField(b, 'P', p.Name)
	putField(b, 'V', p.Version)
	putField(b, 'A', p.Arch)
	putNumber(b, 'S', p.Size)
	putNumber(b, 'I', p.InstalledSize)
	putOptional(b, 'T', p.Description)
	putOptional(b, 'U', p.URL)
	putOptional(b, 'L', p.License)
	putOptional(b, 'o', p.Origin)
	putOptional(b, 'm', p.Maintainer)
	putNumber(b, 't', p.BuildTime)
	putOptional(b, 'c', p.Commit)
	putList(b, 'D', p.Depends)
	putList(b, 'p', p.Provides)
	putList(b, 'i', p.InstallIf)
	putNumber(b, 'k', int64(p.ProviderPriority))
	b.WriteByte('\n')
}

func putField(b *bytes.Buffer, key byte, val string) {
	b.WriteByte(key)
	b.WriteByte(':')
	b.WriteString(flatten(val))
	b.WriteByte('\n')
}

func putOptional(b *bytes.Buffer, key byte, val string) {
	if val != "" {
		putField(b, key, val)
	}
}

func putNumber(b *bytes.Buffer, key byte, n int64) {
	if n > 0 {
		b.WriteByte(key)
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(n, 10))
		b.WriteByte('\n')
	}
}

func putList(b *bytes.Buffer, key byte, list []string) {
	if len(list) > 0 {
		putField(b, key, strings.Join(list, " "))
	}
}

// flatten replaces control characters by a space.
func flatten(s string) string {
	if strings.IndexFunc(s, unicode.IsControl) < 0 {
		return s
	}

	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}

		return r
	}, s)
}

// ParseIndex reads an APKINDEX: either the plain text or an APKINDEX.tar.gz,
// told apart by the gzip magic. The archive is read as abuild writes it: any
// number of gzip members, those whose tar starts with a .SIGN. entry (the
// signature) skipped, and exactly one holding an APKINDEX entry. Signatures
// are not verified here. Input is bounded: 512 MiB
// decompressed, 2 000 000 entries and 1 MiB per line. Every entry must carry
// the C, P, V and A fields and valid values, otherwise the whole index is
// refused; unknown field letters are ignored. Control characters in the free
// text fields (T, U, L, o, m, c) are replaced by a space: a few Alpine
// descriptions contain tabs. Errors wrap [ErrInvalidIndex].
func ParseIndex(r io.Reader) ([]Package, error) {
	return parseIndex(r, defaultIndexLimits())
}

func parseIndex(r io.Reader, lim indexLimits) ([]Package, error) {
	br := bufio.NewReaderSize(r, 64<<10)

	magic, err := br.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, badIndex("reading: %v", err)
	}

	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		return parseIndexArchive(br, lim)
	}

	return parseIndexText(&capReader{r: br, left: lim.bytes}, lim)
}

// parseIndexArchive walks the gzip members of an APKINDEX.tar.gz.
func parseIndexArchive(br *bufio.Reader, lim indexLimits) ([]Package, error) {
	var (
		pkgs  []Package
		found bool
	)

	budget := lim.bytes

	for n := 0; ; n++ {
		if _, err := br.Peek(1); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, badIndex("reading: %v", err)
		}

		if n >= lim.members {
			return nil, badIndex("more than %d gzip members", lim.members)
		}

		got, kind, err := parseIndexMember(br, &budget, lim)
		if err != nil {
			return nil, err
		}

		if kind != memberIndex {
			continue
		}

		if found {
			return nil, badIndex("more than one %s", indexFileName)
		}

		pkgs, found = got, true
	}

	if !found {
		return nil, badIndex("no %s entry", indexFileName)
	}

	return pkgs, nil
}

type memberKind int

const (
	memberSignature memberKind = iota
	memberIndex
)

// parseIndexMember reads one gzip member and returns its entries when it is
// the index member. A member whose tar starts with a .SIGN. entry is a
// signature and is skipped. budget is the decompressed bytes still allowed.
func parseIndexMember(br *bufio.Reader, budget *int64, lim indexLimits) ([]Package, memberKind, error) {
	gz, err := newMemberReader(br)
	if err != nil {
		return nil, 0, badIndex("gzip member: %v", err)
	}

	cr := &capReader{r: gz, left: *budget}
	defer func() { *budget = cr.left }()

	pkgs, kind, err := walkIndexTar(tar.NewReader(cr), lim)
	if err != nil {
		return nil, 0, err
	}

	// Read what is left of the member, which also verifies its checksum.
	if _, err := io.Copy(io.Discard, cr); err != nil {
		return nil, 0, badIndexRead("gzip member", err)
	}

	return pkgs, kind, nil
}

// walkIndexTar looks for the APKINDEX entry of one member's tar.
func walkIndexTar(tr *tar.Reader, lim indexLimits) ([]Package, memberKind, error) {
	var (
		pkgs  []Package
		found bool
	)

	for n := 1; ; n++ {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, 0, badIndexRead("tar", err)
		}

		if n > maxIndexTarEnts {
			return nil, 0, badIndex("tar holds more than %d entries", maxIndexTarEnts)
		}

		if n == 1 && strings.HasPrefix(hdr.Name, sigPrefix) {
			return nil, memberSignature, nil
		}

		if hdr.Name != indexFileName || hdr.Typeflag != tar.TypeReg {
			continue
		}

		if found {
			return nil, 0, badIndex("more than one %s entry in a member", indexFileName)
		}

		if pkgs, err = parseIndexText(tr, lim); err != nil {
			return nil, 0, err
		}

		found = true
	}

	if !found {
		return nil, 0, badIndex("a member holds no %s entry", indexFileName)
	}

	return pkgs, memberIndex, nil
}

// badIndexRead reports an error from reading a compressed index, keeping the
// limit error recognisable.
func badIndexRead(what string, err error) error {
	if errors.Is(err, errTooLarge) {
		return badIndex("%s: decompressed data exceeds the %d byte limit", what, maxIndexBytes)
	}

	return badIndex("%s: %v", what, err)
}

// parseIndexText reads APKINDEX text from r.
func parseIndexText(r io.Reader, lim indexLimits) ([]Package, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), lim.line+2)
	sc.Split(scanLines)

	var (
		pkgs []Package
		cur  stanza
		line int
	)

	flush := func() error {
		if cur.lines == 0 {
			return nil
		}

		if len(pkgs) >= lim.entries {
			return badIndex("more than %d entries", lim.entries)
		}

		p, err := cur.finish()
		if err != nil {
			return badIndex("entry %d: %v", len(pkgs)+1, err)
		}

		pkgs = append(pkgs, p)
		cur = stanza{}

		return nil
	}

	for sc.Scan() {
		line++

		text := sc.Bytes()
		if len(text) == 0 {
			if err := flush(); err != nil {
				return nil, err
			}

			continue
		}

		if len(text) > lim.line {
			return nil, badIndex("line %d is longer than %d bytes", line, lim.line)
		}

		if err := cur.add(text); err != nil {
			return nil, badIndex("line %d: %v", line, err)
		}
	}

	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, badIndex("a line is longer than %d bytes", lim.line)
		}

		return nil, badIndexRead("index", err)
	}

	if err := flush(); err != nil {
		return nil, err
	}

	return pkgs, nil
}

// stanza collects the fields of the entry being read.
type stanza struct {
	pkg   Package
	seen  [256]bool
	lines int
}

// scanLines splits on '\n' only: a '\r' stays in the line and is refused as a
// control character.
func scanLines(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i], nil
	}

	if atEOF {
		return len(data), data, nil
	}

	return 0, nil, nil
}

var errIndexLine = errors.New("bad index line")

// add stores one "X:value" line. Unknown letters are ignored; a known letter
// may appear once per entry.
func (s *stanza) add(line []byte) error {
	if len(line) < 2 || line[1] != ':' {
		return fmt.Errorf("%w: not of the form X:value", errIndexLine)
	}

	s.lines++

	key, val := line[0], string(line[2:])

	known, err := s.pkg.setIndexField(key, val)
	if err != nil {
		return err
	}

	if !known {
		return nil
	}

	if s.seen[key] {
		return fmt.Errorf("%w: field %c appears twice", errIndexLine, key)
	}

	s.seen[key] = true

	return nil
}

// setIndexField stores one index field and reports whether the letter is one
// of the fields of an index entry.
func (p *Package) setIndexField(key byte, val string) (bool, error) {
	switch key {
	case 'C':
		p.Checksum = val
	case 'P':
		p.Name = val
	case 'V':
		p.Version = val
	case 'A':
		p.Arch = val
	case 'T':
		p.Description = flatten(val)
	case 'U':
		p.URL = flatten(val)
	case 'L':
		p.License = flatten(val)
	case 'o':
		p.Origin = flatten(val)
	case 'm':
		p.Maintainer = flatten(val)
	case 'c':
		p.Commit = flatten(val)
	case 'D', 'p', 'i':
		p.setIndexList(key, val)
	case 'S', 'I', 't', 'k':
		return true, p.setIndexNumber(key, val)
	default:
		return false, nil
	}

	return true, nil
}

func (p *Package) setIndexList(key byte, val string) {
	tokens := strings.Fields(val)

	switch key {
	case 'D':
		p.Depends = tokens
	case 'p':
		p.Provides = tokens
	default:
		p.InstallIf = tokens
	}
}

func (p *Package) setIndexNumber(key byte, val string) error {
	n, ok := parseCount(val)
	if !ok {
		return fmt.Errorf("%w: field %c is not a valid number", errIndexLine, key)
	}

	switch key {
	case 'S':
		p.Size = n
	case 'I':
		p.InstalledSize = n
	case 't':
		p.BuildTime = n
	default:
		if n > maxProviderPrio {
			return fmt.Errorf("%w: field %c is not a valid number", errIndexLine, key)
		}

		p.ProviderPriority = int(n)
	}

	return nil
}

// finish checks that the mandatory fields are there and the values valid.
func (s *stanza) finish() (Package, error) {
	for _, key := range [...]byte{'C', 'P', 'V', 'A'} {
		if !s.seen[key] {
			return Package{}, fmt.Errorf("%w: missing field %c", errIndexLine, key)
		}
	}

	if err := s.pkg.validate(); err != nil {
		return Package{}, err
	}

	return s.pkg, nil
}
