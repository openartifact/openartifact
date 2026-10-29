// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package npm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/database/models"
)

// Packuments are assembled by appending to one byte slice. Stored manifests
// are valid JSON objects (validated on publish), so they are passed through as
// raw bytes with whitespace stripped instead of being decoded into
// map[string]any and re-encoded: only the top-level members of each manifest
// (and of its dist object) are located, and dist is rewritten in place.

var errMalformedJSON = errors.New("malformed JSON")

const hexDigits = "0123456789abcdef"

// appendJSONString appends s as a JSON string literal escaped the way
// encoding/json does (HTML-safe, invalid UTF-8 replaced).
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	dst = appendJSONEscaped(dst, s)

	return append(dst, '"')
}

// appendJSONEscaped appends s escaped for use inside a JSON string literal.
func appendJSONEscaped(dst []byte, s string) []byte {
	start := 0

	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if b >= 0x20 && b != '"' && b != '\\' && b != '<' && b != '>' && b != '&' {
				i++
				continue
			}

			dst = append(dst, s[start:i]...)

			switch b {
			case '"', '\\':
				dst = append(dst, '\\', b)
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xF])
			}

			i++
			start = i

			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, `\ufffd`...)
			i += size
			start = i

			continue
		}

		if r == '\u2028' || r == '\u2029' {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xF])
			i += size
			start = i

			continue
		}

		i += size
	}

	return append(dst, s[start:]...)
}

func isJSONSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func skipSpace(b []byte, i int) int {
	for i < len(b) && isJSONSpace(b[i]) {
		i++
	}

	return i
}

// skipString returns the index just past the string literal opening at b[i].
func skipString(b []byte, i int) (int, error) {
	for i++; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '"':
			return i + 1, nil
		}
	}

	return 0, errMalformedJSON
}

// skipValue returns the index just past the JSON value starting at b[i].
func skipValue(b []byte, i int) (int, error) {
	if i >= len(b) {
		return 0, errMalformedJSON
	}

	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		depth := 0

		for ; i < len(b); i++ {
			switch b[i] {
			case '"':
				end, err := skipString(b, i)
				if err != nil {
					return 0, err
				}

				i = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, nil
				}
			}
		}

		return 0, errMalformedJSON
	default:
		start := i
		for i < len(b) && !isJSONSpace(b[i]) && b[i] != ',' && b[i] != '}' && b[i] != ']' {
			i++
		}

		if i == start {
			return 0, errMalformedJSON
		}

		return i, nil
	}
}

// appendCompact appends the JSON value raw with insignificant whitespace removed.
func appendCompact(dst, raw []byte) []byte {
	inString := false
	start := 0

	for i := 0; i < len(raw); i++ {
		c := raw[i]

		if inString {
			switch c {
			case '\\':
				i++
			case '"':
				inString = false
			}

			continue
		}

		switch {
		case c == '"':
			inString = true
		case isJSONSpace(c):
			dst = append(dst, raw[start:i]...)
			start = i + 1
		}
	}

	return append(dst, raw[start:]...)
}

// objectIter walks the top-level members of a JSON object without decoding.
type objectIter struct {
	b   []byte
	pos int
	n   int // members returned so far
	err error
}

func newObjectIter(b []byte) *objectIter {
	it := &objectIter{b: b, pos: skipSpace(b, 0)}
	if it.pos >= len(b) || b[it.pos] != '{' {
		it.err = errMalformedJSON
	}

	it.pos++

	return it
}

// member is one raw `"key": value` pair of a JSON object.
type member struct{ key, val []byte }

// next returns the next member (raw key still escaped and unquoted), or false at the end of the object or on malformed input (err).
func (it *objectIter) next() (member, bool) {
	if it.err != nil {
		return member{}, false
	}

	b := it.b
	it.pos = skipSpace(b, it.pos)

	if it.pos >= len(b) {
		it.err = errMalformedJSON
		return member{}, false
	}

	if b[it.pos] == '}' {
		return member{}, false
	}

	if it.n > 0 {
		if b[it.pos] != ',' {
			it.err = errMalformedJSON
			return member{}, false
		}

		it.pos = skipSpace(b, it.pos+1)
	}

	if it.pos >= len(b) || b[it.pos] != '"' {
		it.err = errMalformedJSON
		return member{}, false
	}

	end, err := skipString(b, it.pos)
	if err != nil {
		it.err = err
		return member{}, false
	}

	key := b[it.pos+1 : end-1]

	it.pos = skipSpace(b, end)
	if it.pos >= len(b) || b[it.pos] != ':' {
		it.err = errMalformedJSON
		return member{}, false
	}

	it.pos = skipSpace(b, it.pos+1)

	end, err = skipValue(b, it.pos)
	if err != nil {
		it.err = err
		return member{}, false
	}

	val := b[it.pos:end]
	it.pos = end
	it.n++

	return member{key, val}, true
}

// decodeKey returns the unescaped form of a raw key containing a backslash.
func decodeKey(raw []byte) string {
	var s string
	if err := json.Unmarshal(append(append([]byte{'"'}, raw...), '"'), &s); err != nil {
		return string(raw)
	}

	return s
}

// hasEscape reports whether a raw key needs decoding. Keys are almost never
// escaped, so the common path compares the raw bytes without allocating.
func hasEscape(raw []byte) bool { return bytes.IndexByte(raw, '\\') >= 0 }

// keyIs reports whether the raw key decodes to name.
func keyIs(raw []byte, name string) bool {
	if hasEscape(raw) {
		return decodeKey(raw) == name
	}

	return string(raw) == name
}

// isServerDistKey reports whether a dist member is replaced by the server.
func isServerDistKey(raw []byte) bool {
	if hasEscape(raw) {
		raw = []byte(decodeKey(raw))
	}

	switch string(raw) {
	case "tarball", "integrity", "shasum":
		return true
	}

	return false
}

// isObject reports whether raw is a JSON object.
func isObject(raw []byte) bool {
	i := skipSpace(raw, 0)

	return i < len(raw) && raw[i] == '{'
}

// packumentWriter assembles packument documents.
type packumentWriter struct {
	buf         []byte
	packageName string
	// tarballPrefix is the escaped "<base>/repos/<repo>/<package>/-/" shared
	// by every version's tarball URL.
	tarballPrefix []byte
}

func newPackumentWriter(repoName, packageName, origin string, capacity int) *packumentWriter {
	prefix := appendJSONEscaped(nil, origin)
	prefix = append(prefix, "/repos/"...)
	prefix = appendJSONEscaped(prefix, repoName)
	prefix = append(prefix, '/')
	prefix = appendJSONEscaped(prefix, packageName)
	prefix = append(prefix, "/-/"...)

	return &packumentWriter{buf: make([]byte, 0, capacity), packageName: packageName, tarballPrefix: prefix}
}

func (w *packumentWriter) member(first *bool, key string) {
	if !*first {
		w.buf = append(w.buf, ',')
	}

	*first = false
	w.buf = appendJSONString(w.buf, key)
	w.buf = append(w.buf, ':')
}

// rawMember appends `"key":value` from an already-escaped raw key and raw value.
func (w *packumentWriter) rawMember(first *bool, rawKey, val []byte) {
	if !*first {
		w.buf = append(w.buf, ',')
	}

	*first = false
	w.buf = append(w.buf, '"')
	w.buf = append(w.buf, rawKey...)
	w.buf = append(w.buf, '"', ':')
	w.buf = appendCompact(w.buf, val)
}

func (w *packumentWriter) distTags(tags map[string]string) {
	if tags == nil {
		w.buf = append(w.buf, "null"...)
		return
	}

	keys := slices.Sorted(maps.Keys(tags))
	w.buf = append(w.buf, '{')

	for i, k := range keys {
		if i > 0 {
			w.buf = append(w.buf, ',')
		}

		w.buf = appendJSONString(w.buf, k)
		w.buf = append(w.buf, ':')
		w.buf = appendJSONString(w.buf, tags[k])
	}

	w.buf = append(w.buf, '}')
}

// dist appends the version's dist object: the stored one (if it is an object)
// minus tarball/integrity/shasum, followed by the server-computed values.
func (w *packumentWriter) dist(stored []byte, v *models.NPMVersion) error {
	w.buf = append(w.buf, '{')
	first := true

	if stored != nil && isObject(stored) {
		it := newObjectIter(stored)

		for {
			m, ok := it.next()
			if !ok {
				break
			}

			if isServerDistKey(m.key) {
				continue
			}

			w.rawMember(&first, m.key, m.val)
		}

		if it.err != nil {
			return it.err
		}
	}

	w.member(&first, "integrity")
	w.buf = appendJSONString(w.buf, v.Integrity)
	w.member(&first, "shasum")
	w.buf = appendJSONString(w.buf, v.Shasum)
	w.member(&first, "tarball")
	w.tarballURL(v.TarballFilename)
	w.buf = append(w.buf, '}')

	return nil
}

// tarballURL appends origin/repos/<repo>/<package>/-/<filename> as a JSON string.
func (w *packumentWriter) tarballURL(filename string) {
	w.buf = append(w.buf, '"')
	w.buf = append(w.buf, w.tarballPrefix...)
	w.buf = appendJSONEscaped(w.buf, filename)
	w.buf = append(w.buf, '"')
}

// version appends one version's manifest with its dist rewritten. When keep is
// non-nil only the members it accepts (plus dist) are emitted.
func (w *packumentWriter) version(v *models.NPMVersion, keep func(key []byte) bool) error {
	w.buf = append(w.buf, '{')
	first := true

	var stored []byte

	it := newObjectIter(v.ManifestJSON)

	for {
		m, ok := it.next()
		if !ok {
			break
		}

		if keyIs(m.key, "dist") {
			stored = m.val
			continue
		}

		if keep != nil && !keep(m.key) {
			continue
		}

		w.rawMember(&first, m.key, m.val)
	}

	if it.err != nil {
		return w.manifestError(v, it.err)
	}

	w.member(&first, "dist")

	if err := w.dist(stored, v); err != nil {
		return w.manifestError(v, err)
	}

	w.buf = append(w.buf, '}')

	return nil
}

// manifestError reports a malformed stored manifest, preferring the precise
// decoder error for the message.
func (w *packumentWriter) manifestError(v *models.NPMVersion, cause error) error {
	var probe map[string]any
	if err := json.Unmarshal(v.ManifestJSON, &probe); err != nil {
		cause = err
	}

	return fmt.Errorf("unmarshal manifest for %s@%s: %w", w.packageName, v.Version, cause)
}

// newestPerVersion returns the versions sorted by version string with
// duplicates collapsed to the last occurrence (map-assignment semantics).
func newestPerVersion(versions []*models.NPMVersion) []*models.NPMVersion {
	sorted := make([]*models.NPMVersion, len(versions))
	copy(sorted, versions)
	slices.SortStableFunc(sorted, func(a, b *models.NPMVersion) int { return strings.Compare(a.Version, b.Version) })

	out := sorted[:0]

	for i, v := range sorted {
		if i+1 < len(sorted) && sorted[i+1].Version == v.Version {
			continue
		}

		out = append(out, v)
	}

	return out
}

func estimatePackumentSize(versions []*models.NPMVersion, perVersion int) int {
	n := 256

	for _, v := range versions {
		n += len(v.ManifestJSON) + perVersion
	}

	return n
}

func appendRFC3339(dst []byte, t time.Time) []byte {
	dst = append(dst, '"')
	dst = t.UTC().AppendFormat(dst, time.RFC3339)

	return append(dst, '"')
}
