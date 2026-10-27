// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package compactindex parses, validates, merges and renders the RubyGems
// compact index (https://guides.rubygems.org/rubygems-org-compact-index-api/):
// the versions file, the per-gem info files and the names file that bundler and
// gem read, plus the request paths of a gem server. Proxy and group
// repositories of the rubygems format are built on it.
package compactindex

import (
	"bufio"
	"bytes"
	"crypto/md5" //nolint:gosec // the compact index defines MD5 as the info file checksum
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Size limits. The largest real info file is a few MiB (thousands of
// versions); rubygems.org's versions file is tens of MiB.
const (
	// MaxInfoBytes bounds one info file read into memory.
	MaxInfoBytes = 64 << 20
	// MaxLineBytes bounds one line of any compact index file.
	MaxLineBytes = 4 << 20
	// MaxVersionsBytes bounds a versions file read into memory.
	MaxVersionsBytes = 512 << 20

	// maxFieldLen bounds a gem name and a version field ("1.0.0-x86_64-linux").
	maxFieldLen = 255
)

// ErrMalformed is returned for a body that is not a compact index file.
var ErrMalformed = errors.New("malformed compact index")

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// InfoLine is one line of a gem's info file: "<version>[-<platform>]
// <dependencies>|<requirements>". Raw is the line exactly as read, without its
// newline, so merging and re-rendering never rewrite what a member said.
type InfoLine struct {
	Raw string
	// Version is the first field: the version, followed by "-<platform>" for a
	// platform gem. It equals the gem's file name without "<name>-" and ".gem".
	Version string
	// Checksum is the lowercase hex SHA-256 of the .gem file, "" if the line
	// carries none.
	Checksum string
}

// ParseInfo reads a gem info file: "---" and one line per version.
func ParseInfo(r io.Reader) ([]InfoLine, error) {
	var lines []InfoLine

	err := scanBody(r, func(line string) error {
		l, err := parseInfoLine(line)
		if err != nil {
			return err
		}

		lines = append(lines, l)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return lines, nil
}

// ParseInfoBytes is ParseInfo for a body in memory.
func ParseInfoBytes(body []byte) ([]InfoLine, error) { return ParseInfo(bytes.NewReader(body)) }

func parseInfoLine(line string) (InfoLine, error) {
	version, rest, _ := strings.Cut(line, " ")
	if !ValidVersionField(version) {
		return InfoLine{}, malformed("info line version %q", clip(version))
	}

	l := InfoLine{Raw: line, Version: version}

	// Old servers list the dependencies only; the checksum is then absent.
	_, reqs, _ := strings.Cut(rest, "|")

	for pair := range strings.SplitSeq(reqs, ",") {
		key, value, _ := strings.Cut(pair, ":")
		if key != "checksum" {
			continue
		}

		if !ValidSHA256(value) {
			return InfoLine{}, malformed("info line checksum of %q", clip(version))
		}

		l.Checksum = value
	}

	return l, nil
}

// RenderInfo writes lines as an info file.
func RenderInfo(lines []InfoLine) []byte {
	var n int
	for _, l := range lines {
		n += len(l.Raw) + 1
	}

	out := make([]byte, 0, len("---\n")+n)
	out = append(out, "---\n"...)

	for _, l := range lines {
		out = append(out, l.Raw...)
		out = append(out, '\n')
	}

	return out
}

// MergeInfo is the union of the info lines of several sources by version field:
// the first source that lists a version provides its line, and the lines keep
// the order of their first appearance.
func MergeInfo(sources ...[]InfoLine) []InfoLine {
	seen := make(map[string]struct{})

	var out []InfoLine

	for _, src := range sources {
		for _, l := range src {
			if _, dup := seen[l.Version]; dup {
				continue
			}

			seen[l.Version] = struct{}{}

			out = append(out, l)
		}
	}

	return out
}

// FindInfo returns the line of version (the info "version[-platform]" field).
func FindInfo(lines []InfoLine, version string) (InfoLine, bool) {
	for _, l := range lines {
		if l.Version == version {
			return l, true
		}
	}

	return InfoLine{}, false
}

// VerifyInfo checks that r is a well-formed info file.
func VerifyInfo(r io.Reader) error {
	return scanBody(r, func(line string) error {
		_, err := parseInfoLine(line)
		return err
	})
}

// InfoMD5 is the checksum the versions file lists for an info file: the
// lowercase hex MD5 of its bytes.
func InfoMD5(info []byte) string {
	sum := md5.Sum(info) //nolint:gosec // defined by the compact index protocol

	return hex.EncodeToString(sum[:])
}

// Entry is the state of one gem in a versions file: its live versions (yanked
// ones removed) and the MD5 of its info file as of the last line that named it.
type Entry struct {
	Name     string
	Versions []string
	InfoMD5  string
}

// Versions is a parsed versions file, one entry per gem.
type Versions struct {
	// CreatedAt is the "created_at" header value, "" if absent.
	CreatedAt string
	Entries   []Entry
}

// ParseVersions reads a versions file: a header ending at "---" and then lines
// "<name> <version>,<version>,-<yanked> <info md5>". A gem may appear on several
// lines (the file is append-only): their versions accumulate, a "-" version
// removes it, and the last MD5 wins. Entries keep the order of first appearance.
func ParseVersions(r io.Reader) (*Versions, error) {
	var (
		v     = &Versions{}
		index = make(map[string]int)
		sc    = newScanner(r)
	)

	for sc.Scan() {
		line := sc.Text()
		if line == "---" {
			break
		}

		if key, value, ok := strings.Cut(line, ":"); ok && key == "created_at" {
			v.CreatedAt = strings.TrimSpace(value)
		}
	}

	if err := scanErr(sc); err != nil {
		return nil, err
	}

	if sc.Text() != "---" {
		return nil, malformed("versions file has no --- separator")
	}

	for sc.Scan() {
		name, tokens, md5sum, err := parseVersionsLine(sc.Text())
		if err != nil {
			return nil, err
		}

		i, ok := index[name]
		if !ok {
			i = len(v.Entries)
			index[name] = i
			v.Entries = append(v.Entries, Entry{Name: name})
		}

		e := &v.Entries[i]
		e.InfoMD5 = md5sum

		for _, tok := range tokens {
			if bare, yanked := strings.CutPrefix(tok, "-"); yanked {
				e.Versions = slices.DeleteFunc(e.Versions, func(x string) bool { return x == bare })
			} else if !slices.Contains(e.Versions, tok) {
				e.Versions = append(e.Versions, tok)
			}
		}
	}

	if err := scanErr(sc); err != nil {
		return nil, err
	}

	return v, nil
}

func parseVersionsLine(line string) (string, []string, string, error) {
	parts := strings.Split(line, " ")
	if len(parts) != 3 {
		return "", nil, "", malformed("versions line %q", clip(line))
	}

	name, versions, md5sum := parts[0], parts[1], parts[2]
	if !ValidName(name) || !validMD5(md5sum) {
		return "", nil, "", malformed("versions line %q", clip(line))
	}

	tokens := strings.Split(versions, ",")
	for _, tok := range tokens {
		if !ValidVersionField(strings.TrimPrefix(tok, "-")) {
			return "", nil, "", malformed("versions line %q", clip(line))
		}
	}

	return name, tokens, md5sum, nil
}

// VerifyVersions checks that r is a well-formed versions file without holding it
// in memory.
func VerifyVersions(r io.Reader) error {
	sc := newScanner(r)
	sawSep := false

	for sc.Scan() {
		line := sc.Text()

		if !sawSep {
			sawSep = line == "---"
			continue
		}

		if _, _, _, err := parseVersionsLine(line); err != nil {
			return err
		}
	}

	if err := scanErr(sc); err != nil {
		return err
	}

	if !sawSep {
		return malformed("versions file has no --- separator")
	}

	return nil
}

// Render writes the versions file: the entries sorted by name, one line each;
// gems without a live version are left out.
func (v *Versions) Render() []byte {
	entries := slices.SortedFunc(slices.Values(v.Entries), func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })

	var b bytes.Buffer

	if v.CreatedAt != "" {
		b.WriteString("created_at: " + v.CreatedAt + "\n")
	}

	b.WriteString("---\n")

	for _, e := range entries {
		if len(e.Versions) == 0 {
			continue
		}

		b.WriteString(e.Name + " " + strings.Join(e.Versions, ",") + " " + e.InfoMD5 + "\n")
	}

	return b.Bytes()
}

// ParseNames reads a names file: "---" and one gem name per line.
func ParseNames(r io.Reader) ([]string, error) {
	var names []string

	err := scanBody(r, func(line string) error {
		if !ValidName(line) {
			return malformed("names line %q", clip(line))
		}

		names = append(names, line)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return names, nil
}

// VerifyNames checks that r is a well-formed names file without holding it in
// memory.
func VerifyNames(r io.Reader) error {
	return scanBody(r, func(line string) error {
		if !ValidName(line) {
			return malformed("names line %q", clip(line))
		}

		return nil
	})
}

// RenderNames writes the sorted, de-duplicated names as a names file.
func RenderNames(names []string) []byte {
	sorted := slices.Compact(slices.Sorted(slices.Values(names)))

	var b bytes.Buffer

	b.WriteString("---\n")

	for _, n := range sorted {
		b.WriteString(n + "\n")
	}

	return b.Bytes()
}

// scanBody calls fn for every line after the leading "---" line.
func scanBody(r io.Reader, fn func(line string) error) error {
	sc := newScanner(r)

	if !sc.Scan() {
		if err := scanErr(sc); err != nil {
			return err
		}

		return malformed("empty file")
	}

	if sc.Text() != "---" {
		return malformed("missing --- separator")
	}

	for sc.Scan() {
		if err := fn(sc.Text()); err != nil {
			return err
		}
	}

	return scanErr(sc)
}

func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxLineBytes)

	return sc
}

func scanErr(sc *bufio.Scanner) error {
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read compact index: %w", err)
	}

	return nil
}

// ValidName reports whether s can be a gem name in a request path or index
// line: RubyGems accepts letters, digits, dot, underscore and hyphen (rubygems.org
// even lists names such as "-" and ".cat"); "." and ".." are never a name.
func ValidName(s string) bool {
	if s == "" || len(s) > maxFieldLen || s == "." || s == ".." {
		return false
	}

	for i := range len(s) {
		if !nameByte(s[i]) {
			return false
		}
	}

	return true
}

func nameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}

// ValidVersionField reports whether s is "<version>" and "<version>-<platform>": a digit first
// (a Gem::Version), then name characters.
func ValidVersionField(s string) bool {
	if s == "" || len(s) > maxFieldLen || s[0] < '0' || s[0] > '9' {
		return false
	}

	for i := range len(s) {
		if !nameByte(s[i]) {
			return false
		}
	}

	return true
}

func validHex(s string, n int) bool {
	if len(s) != n {
		return false
	}

	for i := range len(s) {
		if c := s[i]; c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}

	return true
}

// ValidSHA256 reports whether s is a lowercase hex SHA-256.
func ValidSHA256(s string) bool { return validHex(s, 64) }

// validMD5 accepts a lowercase hex MD5, or nothing: rubygems.org lists a few
// platform releases without a checksum (an empty last field), and bundler
// reads such lines as they are.
func validMD5(s string) bool { return s == "" || validHex(s, 32) }

func clip(s string) string {
	const limit = 64
	if len(s) > limit {
		return s[:limit] + "..."
	}

	return s
}

// DigestValue is the Digest and Repr-Digest header value for content whose
// SHA-256 is sum, spelled the way rubygems.org sends it (a quoted base64
// string); bundler checks the file it assembled from ranges against it.
func DigestValue(sum []byte) string {
	return `sha-256="` + base64.StdEncoding.EncodeToString(sum) + `"`
}
