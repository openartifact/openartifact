// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package helm

import (
	"bytes"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// This file writes the chart entries of index.yaml without the yaml library,
// for the common case in which every string of the entries is one that
// go.yaml.in/yaml/v3 writes as a plain, single-line scalar. The layout is the
// one yaml.Marshal gives HelmChartEntry (4-space mapping indent, sequences
// under a key indented by 2 more, see TestRenderHelmIndexMatchesWholeDocument).
// Anything else (a string that needs quoting, folding or escaping, a long key)
// is left to the library: the writers return false and the caller marshals the
// chart name with yaml.

const (
	// yamlBestWidth is the column past which the emitter folds a plain scalar
	// at a space.
	yamlBestWidth = 80

	// yamlMaxSimpleKey is the length of the longest mapping key the emitter
	// writes inline; a longer one becomes a complex `? key` entry.
	yamlMaxSimpleKey = 128

	// entryFieldColumn is the column of the fields of an entry.
	entryFieldColumn = 10

	entryFirstPrefix  = "        - "
	entryFieldPrefix  = "          "
	entryItemPrefix   = "            - "
	entryItemMapField = "              "
)

// writeGroupDirect writes the entries of one chart name, key line included. On
// false the buffer is as it was.
func writeGroupDirect(buf *bytes.Buffer, name string, charts []*models.HelmChart) bool {
	mark := buf.Len()

	if !plainKey(name) {
		return false
	}

	buf.WriteString("    ")
	buf.WriteString(name)
	buf.WriteString(":\n")

	for _, chart := range charts {
		if !writeEntryDirect(buf, chart) {
			buf.Truncate(mark)

			return false
		}
	}

	return true
}

// writeEntryDirect writes the entry of chart (see newHelmChartEntry for the
// fields) and reports whether every value was one the direct writer supports.
// It may leave a partial entry behind on false.
func writeEntryDirect(buf *bytes.Buffer, chart *models.HelmChart) bool {
	chartURL := "charts/" + url.PathEscape(chart.Name+"-"+chart.Version) + ".tgz"

	var createdBuf [len(time.RFC3339)]byte

	created, ok := timestampValue(createdBuf[:0], chart.CreatedAt)
	if !ok {
		return false
	}

	if !writeField(buf, entryFirstPrefix, "name", chart.Name, true) ||
		!writeField(buf, entryFieldPrefix, "version", chart.Version, true) ||
		!writeField(buf, entryFieldPrefix, "appVersion", chart.AppVersion, false) ||
		!writeField(buf, entryFieldPrefix, "description", chart.Description, false) ||
		!writeField(buf, entryFieldPrefix, "digest", chart.Digest, true) ||
		!writeItems(buf, "urls", []string{chartURL}) {
		return false
	}

	buf.WriteString(entryFieldPrefix + "created: \"")
	buf.Write(created)
	buf.WriteString("\"\n")

	return writeField(buf, entryFieldPrefix, "home", chart.Home, false) &&
		writeItems(buf, "sources", chart.Sources) &&
		writeMaintainers(buf, chart.Maintainers) &&
		writeField(buf, entryFieldPrefix, "icon", chart.Icon, false) &&
		writeField(buf, entryFieldPrefix, "apiVersion", chart.APIVersion, false) &&
		writeField(buf, entryFieldPrefix, "type", chart.Type, false) &&
		writeItems(buf, "keywords", chart.Keywords)
}

// writeField writes `key: value`. A required field writes an empty value as
// "" and an optional one (omitempty) skips it.
func writeField(buf *bytes.Buffer, prefix, key, value string, required bool) bool {
	if value == "" {
		if !required {
			return true
		}

		value = `""`
	} else if !plainScalar(value, entryFieldColumn+len(key)+2) {
		return false
	}

	buf.WriteString(prefix)
	buf.WriteString(key)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteByte('\n')

	return true
}

// writeItems writes a sequence field of plain scalars; an empty one is omitted.
func writeItems(buf *bytes.Buffer, key string, items []string) bool {
	if len(items) == 0 {
		return true
	}

	for _, item := range items {
		if !plainScalar(item, len(entryItemPrefix)) {
			return false
		}
	}

	buf.WriteString(entryFieldPrefix + key + ":\n")

	for _, item := range items {
		buf.WriteString(entryItemPrefix)
		buf.WriteString(item)
		buf.WriteByte('\n')
	}

	return true
}

// writeMaintainers writes the maintainers field. Each maintainer is a mapping
// the library writes with its keys sorted; only keys made of letters, which sort
// as plain strings, and non-empty plain values are supported.
func writeMaintainers(buf *bytes.Buffer, maintainers []map[string]string) bool {
	if len(maintainers) == 0 {
		return true
	}

	const maxKey = 16

	var keyBuf [8]string

	for _, m := range maintainers {
		if len(m) == 0 {
			return false
		}

		for key, value := range m {
			if len(key) > maxKey || !letters(key) || keyword(key) ||
				!plainScalar(value, len(entryItemMapField)+len(key)+2) {
				return false
			}
		}
	}

	buf.WriteString(entryFieldPrefix + "maintainers:\n")

	for _, m := range maintainers {
		keys := keyBuf[:0]
		for key := range m {
			keys = append(keys, key)
		}

		slices.Sort(keys)

		for i, key := range keys {
			if i == 0 {
				buf.WriteString(entryItemPrefix)
			} else {
				buf.WriteString(entryItemMapField)
			}

			buf.WriteString(key)
			buf.WriteString(": ")
			buf.WriteString(m[key])
			buf.WriteByte('\n')
		}
	}

	return true
}

// timestampValue appends t as RFC 3339 in UTC. The emitter always quotes such
// a string (it would read back as a timestamp); that holds for the years
// time.Time formats with four digits.
func timestampValue(dst []byte, t time.Time) ([]byte, bool) {
	t = t.UTC()
	if y := t.Year(); y < 1 || y > 9999 {
		return nil, false
	}

	return t.AppendFormat(dst, time.RFC3339), true
}

// plainKey reports whether s is a mapping key the emitter writes inline and
// unquoted: letters, digits, '.', '_' and '-', starting with a letter or digit.
func plainKey(s string) bool {
	if s == "" || len(s) > yamlMaxSimpleKey || !plainStart(s) {
		return false
	}

	for i := range len(s) {
		if c := s[i]; !alnum(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
	}

	return true
}

// plainScalar reports whether the emitter writes s, as a value starting at
// column col, as an unquoted scalar on one line, exactly as it is. It is
// conservative: false only means "leave it to the library".
//
// The accepted strings are non-empty, start with a letter or digit and
// consist of letters, digits, '.', '_', '-', '+', '/', '@', '%', '=', single spaces between
// words, and ':' when not followed by a space. They must not read back as
// something else (a bool, null or number: see plainStart) and, when they
// contain a space, must end at or before column yamlBestWidth so that no line
// fold applies.
func plainScalar(s string, col int) bool {
	if !plainStart(s) {
		return false
	}

	for i := range len(s) {
		switch c := s[i]; {
		case alnum(c), c == '.', c == '_', c == '-', c == '+', c == '/', c == '@', c == '%', c == '=':
		case c == ' ':
			if s[i-1] == ' ' || i == len(s)-1 || col+len(s) > yamlBestWidth {
				return false
			}
		case c == ':':
			if i == len(s)-1 || s[i+1] == ' ' {
				return false
			}
		default:
			return false
		}
	}

	return true
}

// plainStart reports whether s starts with a letter or digit and would resolve
// to a string, not to a bool, null, number or timestamp.
func plainStart(s string) bool {
	switch {
	case s == "":
		return false
	case isLetter(s[0]):
		return !keyword(s)
	case isDigit(s[0]):
		return digitStringIsText(s)
	default:
		return false
	}
}

// keyword reports whether s is, in any case, a word the yaml 1.1 resolver reads
// as a bool or null.
func keyword(s string) bool {
	if len(s) > len("false") {
		return false
	}

	for _, k := range [...]string{"y", "n", "yes", "no", "on", "off", "true", "false", "null", "nan", "inf"} {
		if strings.EqualFold(s, k) {
			return true
		}
	}

	return false
}

// digitStringIsText reports whether s, which starts with a digit, certainly
// does not resolve to an integer, float, base-60 number or timestamp. Those
// are made of digits, a-f, x, o, t, z, e, '.', '+', '-', '_', ':' and spaces,
// with at most one '.'; an integer in a non-decimal base starts with 0x, 0o or
// 0b followed by digits of that base, and a decimal one or a float has no
// letter other than the exponent's e.
// That leaves versions with two dots ("1.0.0"), strings with any other letter
// and hex digests that contain a letter other than e.
func digitStringIsText(s string) bool {
	dots := 0
	nonExponentLetter := false
	separator := false

	for i := range len(s) {
		switch c := s[i]; {
		case c == '.':
			dots++
		case isLetter(c):
			switch c {
			case 'e', 'E':
			case 'a', 'b', 'c', 'd', 'f', 'A', 'B', 'C', 'D', 'F', 'x', 'X', 'o', 'O', 't', 'T', 'z', 'Z':
				nonExponentLetter = true
			default:
				return true
			}
		case c == '-', c == '+', c == '_', c == ':', c == ' ':
			separator = true
		}
	}

	if dots >= 2 {
		return true
	}

	return nonExponentLetter && !separator && !prefixedInteger(s)
}

// prefixedInteger reports whether s, which starts with a digit, may be a 0x,
// 0o or 0b integer: it has that prefix and only digits of that base after it.
func prefixedInteger(s string) bool {
	if len(s) < 2 || s[0] != '0' {
		return false
	}

	var digits string

	switch s[1] {
	case 'x', 'X':
		digits = "0123456789abcdefABCDEF"
	case 'o', 'O':
		digits = "01234567"
	case 'b', 'B':
		digits = "01"
	default:
		return false
	}

	for i := 2; i < len(s); i++ {
		if !strings.ContainsRune(digits, rune(s[i])) {
			return false
		}
	}

	return true
}

func letters(s string) bool {
	if s == "" {
		return false
	}

	for i := range len(s) {
		if !isLetter(s[i]) {
			return false
		}
	}

	return true
}

func alnum(c byte) bool    { return isLetter(c) || isDigit(c) }
func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
