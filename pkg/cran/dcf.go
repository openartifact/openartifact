// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Limits of the DCF reader.
const (
	// MaxDescriptionBytes bounds a DESCRIPTION file.
	MaxDescriptionBytes = 1 << 20
	// MaxIndexBytes bounds an uncompressed PACKAGES file of an upstream
	// repository (CRAN's is about 30 MiB).
	MaxIndexBytes = 256 << 20

	maxLineBytes   = 1 << 20
	maxFieldsInRec = 256
	maxFieldName   = 128
	maxFieldBytes  = 512 << 10
)

var (
	// ErrSyntax marks a document that is not valid DCF.
	ErrSyntax = errors.New("cran: malformed DCF")
	// ErrTooLarge marks a DCF document or field beyond the limits.
	ErrTooLarge = errors.New("cran: DCF too large")
)

// Field is one "Name: value" pair of a record. Continuation lines of a value
// are joined with "\n", without their leading white space.
type Field struct {
	Name, Value string
}

// Record is one DCF paragraph: its fields in document order. A field name
// occurs at most once.
type Record []Field

// Get returns the value of the field name (matched exactly, as R does), "" when
// absent.
func (r Record) Get(name string) string {
	v, _ := r.Lookup(name)

	return v
}

// Lookup returns the value of the field name and whether the record has it.
func (r Record) Lookup(name string) (string, bool) {
	for _, f := range r {
		if f.Name == name {
			return f.Value, true
		}
	}

	return "", false
}

// ReadRecords parses a DCF document of at most limit bytes (limit <= 0 means
// MaxIndexBytes) and calls fn with every record, in order; fn returning an
// error stops the read with that error. Records are separated by blank lines;
// a line starting with white space continues the previous field. A repeated
// field in one record, a line that is neither a field nor a continuation,
// invalid UTF-8 and NUL bytes are errors for strict readers; this one repairs
// invalid UTF-8 (a latin1 DESCRIPTION is common in old packages) and rejects
// the rest.
//
//nolint:gocyclo,cyclop // one scanner loop: each branch is a line kind of the DCF grammar
func ReadRecords(r io.Reader, limit int64, fn func(Record) error) error {
	if limit <= 0 {
		limit = MaxIndexBytes
	}

	sc := bufio.NewScanner(io.LimitReader(r, limit+1))
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)

	var (
		rec   Record
		total int64
		seen  map[string]struct{}
	)

	flush := func() error {
		if len(rec) == 0 {
			return nil
		}

		out := rec
		rec, seen = nil, nil

		return fn(out)
	}

	for sc.Scan() {
		line := sc.Bytes()

		if total += int64(len(line)) + 1; total > limit {
			return ErrTooLarge
		}

		if bytes.IndexByte(line, 0) >= 0 {
			return fmt.Errorf("%w: NUL byte", ErrSyntax)
		}

		text := strings.TrimRight(toUTF8(line), " \t\r")

		switch {
		case strings.TrimSpace(text) == "":
			if err := flush(); err != nil {
				return err
			}
		case text[0] == ' ' || text[0] == '\t':
			if len(rec) == 0 {
				return fmt.Errorf("%w: continuation line with no field", ErrSyntax)
			}

			last := &rec[len(rec)-1]
			if len(last.Value)+len(text) > maxFieldBytes {
				return ErrTooLarge
			}

			last.Value += "\n" + strings.TrimSpace(text)
		default:
			name, value, ok := strings.Cut(text, ":")
			if !ok || name == "" || len(name) > maxFieldName || strings.ContainsAny(name, " \t") {
				return fmt.Errorf("%w: line is not a field", ErrSyntax)
			}

			if len(rec) >= maxFieldsInRec {
				return ErrTooLarge
			}

			if seen == nil {
				seen = make(map[string]struct{}, 16)
			}

			if _, dup := seen[name]; dup {
				return fmt.Errorf("%w: field %q repeated", ErrSyntax, name)
			}

			seen[name] = struct{}{}
			rec = append(rec, Field{Name: name, Value: strings.TrimSpace(value)})
		}
	}

	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return ErrTooLarge
		}

		return err
	}

	if total > limit {
		return ErrTooLarge
	}

	return flush()
}

// ParseDescription reads the single record of a DESCRIPTION file.
func ParseDescription(r io.Reader) (Record, error) {
	var out []Record

	err := ReadRecords(io.LimitReader(r, MaxDescriptionBytes+1), MaxDescriptionBytes, func(rec Record) error {
		out = append(out, rec)

		return nil
	})
	if err != nil {
		return nil, err
	}

	if len(out) != 1 {
		return nil, fmt.Errorf("%w: DESCRIPTION holds %d records", ErrSyntax, len(out))
	}

	return out[0], nil
}

func toUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}

	return strings.ToValidUTF8(string(b), "\uFFFD")
}

// Fold collapses every run of white space (continuation line breaks included)
// of a field value to one space.
func Fold(v string) string {
	return strings.Join(strings.Fields(v), " ")
}
