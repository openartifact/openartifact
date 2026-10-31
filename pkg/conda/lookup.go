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
	"strconv"
	"strings"
	"time"
)

// ErrNotListed is a package file the repodata does not list.
var ErrNotListed = errors.New("conda: file is not listed in the repodata")

// Entry is what a repodata entry says about its package file: the digests a
// download must match.
type Entry struct {
	// SHA256 and MD5 are lowercase hex, "" when the entry has none (or a
	// malformed one).
	SHA256, MD5 string
	// Name and Version are the package name and version the entry itself
	// states ("" when it has none), which need not agree with its file name.
	Name, Version string
	// Size is the file size in bytes, -1 when the entry has none.
	Size int64
	// Published is the entry's "timestamp" (see EntryTimestamp), the zero time
	// when it has none.
	Published time.Time
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
		SHA256  string      `json:"sha256"`
		MD5     string      `json:"md5"`
		Size    json.Number `json:"size"`
		Name    string      `json:"name"`
		Version string      `json:"version"`
		// The build time as the repodata states it; not every entry has one.
		Timestamp json.RawMessage `json:"timestamp"`
	}

	if err := json.Unmarshal(raw, &doc); err != nil {
		return Entry{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}

	e := Entry{Size: -1, Name: doc.Name, Version: doc.Version, Published: timestampTime(doc.Timestamp)}

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

// secondsLimit tells seconds from milliseconds in a repodata timestamp: a
// value below it (the year 5138 in seconds, 1973 in milliseconds) is seconds.
// conda-build writes milliseconds; older entries have seconds.
const secondsLimit = 1e11

// EntryTimestamp is the "timestamp" of a repodata entry: when the package was
// built, the only date repodata carries. The zero time when the entry has none,
// or one that is not a positive number.
func EntryTimestamp(raw json.RawMessage) time.Time {
	var doc struct {
		Timestamp json.RawMessage `json:"timestamp"`
	}

	if err := json.Unmarshal(raw, &doc); err != nil {
		return time.Time{}
	}

	return timestampTime(doc.Timestamp)
}

// timestampTime reads a JSON number (a string holding one is as good).
func timestampTime(raw json.RawMessage) time.Time {
	v, err := strconv.ParseFloat(strings.Trim(string(raw), `"`), 64)
	if err != nil || v <= 0 || v >= 1e18 {
		return time.Time{}
	}

	if v < secondsLimit {
		return time.Unix(int64(v), 0).UTC()
	}

	return time.UnixMilli(int64(v)).UTC()
}

// EntryIdentity is the "name" and "version" a repodata entry states about its
// package, "" for a field it lacks or that is not a string.
func EntryIdentity(raw json.RawMessage) (string, string) {
	var doc struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", ""
	}

	return doc.Name, doc.Version
}

// EarliestBuild is the earliest "timestamp" among the entries of the plain
// repodata.json in r whose name (compared without regard to case) and version
// are the given ones: when the package version was first built, the nearest
// thing to a publish time a channel states. ok is false when no matching entry
// carries a timestamp. limit bounds the bytes read.
func EarliestBuild(r io.Reader, limit int64, name, version string) (time.Time, bool, error) {
	var (
		earliest time.Time
		ok       bool
	)

	err := Scan(r, limit, Visitor{Entry: func(_ Section, _ string, raw json.RawMessage) error {
		n, v := EntryIdentity(raw)
		if v != version || !strings.EqualFold(n, name) {
			return nil
		}

		if built := EntryTimestamp(raw); !built.IsZero() && (!ok || built.Before(earliest)) {
			earliest, ok = built, true
		}

		return nil
	}})

	return earliest, ok, err
}
