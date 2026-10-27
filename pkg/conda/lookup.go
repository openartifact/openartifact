// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// ErrNotListed is a package file the repodata does not list.
var ErrNotListed = errors.New("conda: file is not listed in the repodata")

// Entry is what a repodata entry says about its package file: the digests a
// download must match.
type Entry struct {
	// SHA256 and MD5 are lowercase hex, "" when the entry has none (or a
	// malformed one).
	SHA256, MD5 string
	// Size is the file size in bytes, -1 when the entry has none.
	Size int64
}

var (
	sha256HexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	md5HexRE    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// plainNameRE is a file name that JSON encoders write the same way in
	// every dialect, so the raw bytes can be searched for.
	plainNameRE = regexp.MustCompile(`^[A-Za-z0-9._+=@-]+$`)
)

// lookupChunk is the read size of the raw search.
const lookupChunk = 1 << 20

// Lookup finds filename in the plain repodata.json in r and returns its
// digests, or ErrNotListed. It reads r at most once and keeps about a
// megabyte: instead of parsing the whole document it searches for the
// entry's key and decodes only the value after it. r must hold a document
// that was validated (ValidateIndex); limit bounds the bytes read.
func Lookup(r io.Reader, filename string, limit int64) (Entry, error) {
	if _, ok := ParseFilename(filename); !ok {
		return Entry{}, ErrNotListed
	}

	r = &capReader{r: r, left: limit}

	if !plainNameRE.MatchString(filename) {
		return lookupScan(r, filename, limit)
	}

	needle := []byte(`"` + filename + `":`)
	window := make([]byte, 0, lookupChunk+len(needle))
	tmp := make([]byte, lookupChunk)

	for {
		n, err := r.Read(tmp)
		window = append(window, tmp[:n]...)

		if i := bytes.Index(window, needle); i >= 0 {
			return decodeEntry(io.MultiReader(bytes.NewReader(window[i+len(needle):]), r))
		}

		if keep := len(needle) - 1; len(window) > keep {
			window = append(window[:0], window[len(window)-keep:]...)
		}

		if errors.Is(err, io.EOF) {
			return Entry{}, ErrNotListed
		}

		if err != nil {
			return Entry{}, classify(err)
		}
	}
}

// errFound stops lookupScan.
var errFound = errors.New("found")

// lookupScan is Lookup for names the raw search cannot be sure to match.
func lookupScan(r io.Reader, filename string, limit int64) (Entry, error) {
	var (
		found Entry
		hit   bool
	)

	err := Scan(r, limit, Visitor{Entry: func(_ Section, name string, raw json.RawMessage) error {
		if name != filename {
			return nil
		}

		e, derr := parseEntry(raw)
		found, hit = e, derr == nil

		return errFound
	}})
	if errors.Is(err, errFound) {
		if !hit {
			return Entry{}, fmt.Errorf("%w: entry %q", ErrMalformed, filename)
		}

		return found, nil
	}

	if err != nil {
		return Entry{}, err
	}

	return Entry{}, ErrNotListed
}

func decodeEntry(r io.Reader) (Entry, error) {
	var raw json.RawMessage

	if err := json.NewDecoder(io.LimitReader(r, MaxEntryBytes)).Decode(&raw); err != nil {
		return Entry{}, classify(err)
	}

	return parseEntry(raw)
}

func parseEntry(raw json.RawMessage) (Entry, error) {
	var doc struct {
		SHA256 string      `json:"sha256"`
		MD5    string      `json:"md5"`
		Size   json.Number `json:"size"`
	}

	if err := json.Unmarshal(raw, &doc); err != nil {
		return Entry{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}

	e := Entry{Size: -1}

	if s := strings.ToLower(doc.SHA256); sha256HexRE.MatchString(s) {
		e.SHA256 = s
	}

	if s := strings.ToLower(doc.MD5); md5HexRE.MatchString(s) {
		e.MD5 = s
	}

	if size, err := doc.Size.Int64(); err == nil && size >= 0 {
		e.Size = size
	}

	return e, nil
}
