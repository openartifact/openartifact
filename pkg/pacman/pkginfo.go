// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pacman reads Arch Linux packages and writes pacman repository
// databases (what repo-add does): the .PKGINFO of a package, the sync database
// tarball of "desc" and "files" entries, version comparison (vercmp) and the
// OpenPGP detached signatures pacman verifies.
package pacman

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Field and size limits. They keep what a package can put into one database row
// and one desc entry bounded.
const (
	// MaxPkgInfoBytes bounds the .PKGINFO of a package.
	MaxPkgInfoBytes = 1 << 20
	// MaxNameLen bounds package names, versions, architectures and each
	// dependency-like value.
	MaxNameLen = 255
	// MaxValueLen bounds a free-text value (description, URL, packager).
	MaxValueLen = 4096
	// MaxListEntries bounds each multi-valued field.
	MaxListEntries = 5000
)

// Validation errors.
var (
	ErrPkgInfoTooLarge = errors.New("pacman: .PKGINFO too large")
	ErrMissingField    = errors.New("pacman: .PKGINFO misses a required field")
	ErrInvalidField    = errors.New("pacman: invalid .PKGINFO field")
)

// PkgInfo is the parsed .PKGINFO of a package.
type PkgInfo struct {
	Name        string
	Base        string
	Version     string // [epoch:]pkgver-pkgrel
	Description string
	URL         string
	Packager    string
	Arch        string
	BuildDate   int64
	InstalledSz int64

	Licenses     []string
	Groups       []string
	Replaces     []string
	Conflicts    []string
	Provides     []string
	Depends      []string
	OptDepends   []string
	MakeDepends  []string
	CheckDepends []string
}

// ParsePkgInfo parses the text of a .PKGINFO. The name, version and
// architecture are required and validated; everything else is optional.
func ParsePkgInfo(r io.Reader) (*PkgInfo, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxPkgInfoBytes+1))
	if err != nil {
		return nil, fmt.Errorf("pacman: reading .PKGINFO: %w", err)
	}

	if len(data) > MaxPkgInfoBytes {
		return nil, ErrPkgInfoTooLarge
	}

	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: not UTF-8", ErrInvalidField)
	}

	info := &PkgInfo{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 64<<10), MaxPkgInfoBytes+1)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		if err := info.set(strings.TrimSpace(key), strings.TrimSpace(value)); err != nil {
			return nil, err
		}
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("pacman: scanning .PKGINFO: %w", err)
	}

	return info, info.validate()
}

func appendBounded(list *[]string, key, value string) error {
	if len(*list) >= MaxListEntries {
		return fmt.Errorf("%w: too many %s entries", ErrInvalidField, key)
	}

	if value != "" {
		*list = append(*list, value)
	}

	return nil
}

func (p *PkgInfo) set(key, value string) error {
	if len(value) > MaxValueLen {
		return fmt.Errorf("%w: %s is too long", ErrInvalidField, key)
	}

	switch key {
	case "pkgname":
		p.Name = value
	case "pkgbase":
		p.Base = value
	case "pkgver":
		p.Version = value
	case "pkgdesc":
		p.Description = value
	case "url":
		p.URL = value
	case "packager":
		p.Packager = value
	case "arch":
		p.Arch = value
	case "builddate":
		return parseNonNegative(key, value, &p.BuildDate)
	case "size":
		return parseNonNegative(key, value, &p.InstalledSz)
	case "license":
		return appendBounded(&p.Licenses, key, value)
	case "group":
		return appendBounded(&p.Groups, key, value)
	case "replaces":
		return appendBounded(&p.Replaces, key, value)
	case "conflict":
		return appendBounded(&p.Conflicts, key, value)
	case "provides":
		return appendBounded(&p.Provides, key, value)
	case "depend":
		return appendBounded(&p.Depends, key, value)
	case "optdepend":
		return appendBounded(&p.OptDepends, key, value)
	case "makedepend":
		return appendBounded(&p.MakeDepends, key, value)
	case "checkdepend":
		return appendBounded(&p.CheckDepends, key, value)
	}

	return nil
}

func parseNonNegative(key, value string, dst *int64) error {
	if value == "" {
		return nil
	}

	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("%w: %s %q", ErrInvalidField, key, value)
	}

	*dst = n

	return nil
}

func (p *PkgInfo) validate() error {
	for _, f := range []struct{ key, val string }{{"pkgname", p.Name}, {"pkgver", p.Version}, {"arch", p.Arch}} {
		if f.val == "" {
			return fmt.Errorf("%w: %s", ErrMissingField, f.key)
		}
	}

	if !ValidName(p.Name) {
		return fmt.Errorf("%w: pkgname %q", ErrInvalidField, p.Name)
	}

	if p.Base == "" {
		p.Base = p.Name
	} else if !ValidName(p.Base) {
		return fmt.Errorf("%w: pkgbase %q", ErrInvalidField, p.Base)
	}

	if !ValidVersion(p.Version) {
		return fmt.Errorf("%w: pkgver %q", ErrInvalidField, p.Version)
	}

	if !ValidArch(p.Arch) {
		return fmt.Errorf("%w: arch %q", ErrInvalidField, p.Arch)
	}

	for _, list := range [][]string{
		p.Licenses, p.Groups, p.Replaces, p.Conflicts, p.Provides,
		p.Depends, p.OptDepends, p.MakeDepends, p.CheckDepends,
	} {
		for _, v := range list {
			if len(v) > MaxNameLen*4 {
				return fmt.Errorf("%w: value too long", ErrInvalidField)
			}
		}
	}

	return nil
}

// ValidName reports whether s is a legal package name: lowercase letters,
// digits and "@._+-", not starting with a hyphen or a dot (the makepkg rule).
func ValidName(s string) bool {
	if s == "" || len(s) > MaxNameLen || s[0] == '-' || s[0] == '.' {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && !isDigit(c) && !strings.ContainsRune("@._+-", rune(c)) {
			return false
		}
	}

	return true
}

// ValidVersion reports whether s is a legal "[epoch:]pkgver-pkgrel": no
// whitespace, slashes or control characters, a numeric epoch when present and
// a release.
func ValidVersion(s string) bool {
	if s == "" || len(s) > MaxNameLen {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if c <= ' ' || c >= 0x7f || c == '/' {
			return false
		}
	}

	if e, _, ok := strings.Cut(s, ":"); ok {
		if e == "" {
			return false
		}

		for i := range len(e) {
			if !isDigit(e[i]) {
				return false
			}
		}
	}

	// pkgver-pkgrel: the release is mandatory, and the file name is built from it.
	i := strings.LastIndexByte(s, '-')

	return i > 0 && i < len(s)-1
}

// ArchAny is the architecture of a package that runs on every architecture.
const ArchAny = "any"

// ValidArch reports whether s is a legal architecture ("any", "x86_64", ...).
func ValidArch(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}

	for i := range len(s) {
		c := s[i]
		if !isAlnum(c) && c != '_' && c != '-' {
			return false
		}
	}

	return true
}
