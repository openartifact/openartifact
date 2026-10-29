// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"archive/tar"
	"bufio"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/limitio"
)

// Limits on what reading one repository database may cost. A database is
// fetched from an upstream mirror and is untrusted input.
const (
	// MaxDBBytes bounds the decompressed tar stream of a database.
	MaxDBBytes int64 = 2 << 30
	// MaxDBEntries bounds the entries of a database.
	MaxDBEntries = 500_000
	// MaxDescBytes bounds the "desc" (or "depends") member of one package.
	MaxDescBytes = 4 << 20
	// MaxDBFileListBytes bounds the file lists kept from one files database.
	MaxDBFileListBytes = 256 << 20
)

// Errors reading a database.
var (
	ErrNotDatabase  = errors.New("pacman: not a pacman database")
	ErrDBTooLarge   = errors.New("pacman: database exceeds the limit")
	errSkippedEntry = errors.New("pacman: invalid database entry")
)

// ReadDB reads a repository database (the "db" or "files" tarball, gzip, xz or
// zstd compressed) into its entries: the fields of each package's "desc" (and
// legacy "depends") member and, when the archive has them, its "files" member.
//
// The database is untrusted. An entry whose identity is unusable (no valid
// name, version or package file name, or a malformed number or list) is left
// out; a database that is not a compressed tar, or exceeds a limit, is an error.
// Fields the database does not carry stay empty.
func ReadDB(r io.Reader) ([]DBEntry, error) {
	byDir := make(map[string]*DBEntry)
	order := make([]string, 0, 1024)

	files := make(map[string][]string)

	err := scanDB(r, func(dir, member string, body []byte) error {
		switch member {
		case "desc", "depends":
			e, err := parseDesc(body)
			if err != nil {
				// Skip this package: its files member, if any, is dropped below.
				return nil //nolint:nilerr // an unusable entry is skipped, not fatal
			}

			if cur, ok := byDir[dir]; ok {
				mergeDesc(cur, e)
				return nil
			}

			byDir[dir] = e

			order = append(order, dir)
		case "files":
			files[dir] = parseFileList(body)
		}

		return nil
	}, true, nil)
	if err != nil {
		return nil, err
	}

	if len(order) > MaxDBEntries {
		return nil, fmt.Errorf("%w: too many entries", ErrDBTooLarge)
	}

	out := make([]DBEntry, 0, len(order))

	for _, dir := range order {
		e := byDir[dir]
		if !e.usable() {
			continue
		}

		e.Files = files[dir]
		out = append(out, *e)
	}

	return out, nil
}

// ReadFileLists reads the "files" members of a files database: the file list
// of every package whose database directory (name-version, see DBEntry.DirName)
// want accepts, keyed by that directory. Other packages' lists are never
// retained, so reading the files database of a large repository costs memory
// in proportion to what is wanted.
func ReadFileLists(r io.Reader, want func(dir string) bool) (map[string][]string, error) {
	out := make(map[string][]string)

	err := scanDB(r, func(dir, member string, body []byte) error {
		if member == "files" {
			out[dir] = parseFileList(body)
		}

		return nil
	}, false, want)
	if err != nil {
		return nil, err
	}

	return out, nil
}

// scanDB walks the members of a database tarball and hands the body of each
// "desc", "depends" and "files" member to fn, with the package directory it
// lives in. withDesc says whether desc members are read at all; wanted, when
// given, filters the directories whose members are read.
func scanDB(r io.Reader, fn func(dir, member string, body []byte) error, withDesc bool, wanted func(dir string) bool) error {
	dec, _, closeDec, err := openCompressed(r)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotDatabase, err)
	}

	defer closeDec()

	tr := tar.NewReader(bufio.NewReader(limitio.Capped(dec, MaxDBBytes, ErrDBTooLarge)))

	var (
		entries   int
		listBytes int
		sawHeader bool
	)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return tarError(err, sawHeader)
		}

		sawHeader = true

		entries++
		if entries > MaxDBEntries*3 {
			return fmt.Errorf("%w: too many entries", ErrDBTooLarge)
		}

		dir, member, ok := splitMember(hdr)
		if !ok || (member != "files" && !withDesc) || (member == "files" && listBytes >= MaxDBFileListBytes) {
			continue
		}

		if wanted != nil && !wanted(dir) {
			continue
		}

		limit := int64(MaxDescBytes)
		if member == "files" {
			limit = int64(MaxDBFileListBytes - listBytes)
		}

		body, err := readMember(tr, member, limit)
		if err != nil {
			return err
		}

		if body == nil {
			continue
		}

		if member == "files" {
			listBytes += len(body)
		}

		if err := fn(dir, member, body); err != nil {
			return err
		}
	}
}

// tarError classifies a failure of the archive reader: a stream that is not a
// tarball at all is no database.
func tarError(err error, sawHeader bool) error {
	if errors.Is(err, ErrDBTooLarge) {
		return err
	}

	if !sawHeader {
		return fmt.Errorf("%w: %w", ErrNotDatabase, err)
	}

	return fmt.Errorf("pacman: reading database archive: %w", err)
}

// readMember reads a member of at most limit bytes. An oversized desc or depends
// is skipped (nil body); oversized file lists fail the whole read.
func readMember(r io.Reader, member string, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("pacman: reading database member: %w", err)
	}

	if int64(len(body)) > limit {
		if member == "files" {
			return nil, fmt.Errorf("%w: file lists too large", ErrDBTooLarge)
		}

		return nil, nil
	}

	if body == nil {
		body = []byte{}
	}

	return body, nil
}

// splitMember returns the package directory and member name ("desc",
// "depends", "files") of a regular tar member "dir/member".
func splitMember(hdr *tar.Header) (string, string, bool) {
	if hdr.Typeflag != tar.TypeReg {
		return "", "", false
	}

	dir, member := path.Split(path.Clean(hdr.Name))

	dir = strings.TrimSuffix(dir, "/")
	if dir == "" || strings.Contains(dir, "/") {
		return "", "", false
	}

	switch member {
	case "desc", "depends", "files":
		return dir, member, true
	}

	return "", "", false
}

// parseFileList splits the body of a "files" member: the lines after "%FILES%"
// (and before another header, such as "%BACKUP%", which a files database also
// carries).
func parseFileList(body []byte) []string {
	var files []string

	in := false

	for line := range strings.SplitSeq(string(body), "\n") {
		switch {
		case line == "%FILES%":
			in = true
		case strings.HasPrefix(line, "%") && strings.HasSuffix(line, "%"):
			in = false
		case in && line != "":
			files = append(files, line)
		}
	}

	return files
}

// parseDesc parses the "%KEY%" blocks of a desc member.
func parseDesc(body []byte) (*DBEntry, error) {
	e := &DBEntry{}

	var (
		key    string
		values []string
	)

	flush := func() error {
		if key == "" {
			return nil
		}

		err := e.setField(key, values)
		key, values = "", nil

		return err
	}

	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSuffix(line, "\r")

		switch {
		case len(line) > 2 && line[0] == '%' && line[len(line)-1] == '%':
			if err := flush(); err != nil {
				return nil, err
			}

			key = line[1 : len(line)-1]
		case line == "":
			if err := flush(); err != nil {
				return nil, err
			}
		case key != "":
			values = append(values, line)
		}
	}

	if err := flush(); err != nil {
		return nil, err
	}

	return e, nil
}

// setField stores one block of a desc member. Unknown fields are ignored.
func (e *DBEntry) setField(key string, values []string) error {
	if len(values) > MaxListEntries {
		return errSkippedEntry
	}

	for _, v := range values {
		if len(v) > MaxValueLen {
			return errSkippedEntry
		}
	}

	first := ""
	if len(values) > 0 {
		first = values[0]
	}

	switch key {
	case "FILENAME":
		e.Filename = first
	case "NAME":
		e.Name = first
	case "BASE":
		e.Base = first
	case "VERSION":
		e.Version = first
	case "DESC":
		e.Description = first
	case "MD5SUM":
		e.MD5 = first
	case "SHA256SUM":
		e.SHA256 = first
	case "PGPSIG":
		e.PGPSig = first
	case "URL":
		e.URL = first
	case "ARCH":
		e.Arch = first
	case "PACKAGER":
		e.Packager = first
	case "CSIZE", "ISIZE", "BUILDDATE":
		return e.setNumber(key, first)
	default:
		e.setList(key, values)
	}

	return nil
}

func (e *DBEntry) setNumber(key, value string) error {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return errSkippedEntry
	}

	switch key {
	case "CSIZE":
		e.CSize = n
	case "ISIZE":
		e.ISize = n
	case "BUILDDATE":
		e.BuildDate = n
	}

	return nil
}

func (e *DBEntry) setList(key string, values []string) {
	switch key {
	case "GROUPS":
		e.Groups = values
	case "LICENSE":
		e.Licenses = values
	case "REPLACES":
		e.Replaces = values
	case "CONFLICTS":
		e.Conflicts = values
	case "PROVIDES":
		e.Provides = values
	case "DEPENDS":
		e.Depends = values
	case "OPTDEPENDS":
		e.OptDepends = values
	case "MAKEDEPENDS":
		e.MakeDepends = values
	case "CHECKDEPENDS":
		e.CheckDepends = values
	}
}

// mergeDesc adds the fields of a second member of the same package (the legacy
// "depends" next to "desc") that the first did not set.
func mergeDesc(dst, src *DBEntry) {
	if dst.Depends == nil {
		dst.Depends = src.Depends
	}

	if dst.OptDepends == nil {
		dst.OptDepends = src.OptDepends
	}

	if dst.Conflicts == nil {
		dst.Conflicts = src.Conflicts
	}

	if dst.Provides == nil {
		dst.Provides = src.Provides
	}

	if dst.Replaces == nil {
		dst.Replaces = src.Replaces
	}
}

// usable reports whether the entry has an identity the repository can list:
// valid name, version and architecture and a package file name that is neither
// a path nor a database file.
func (e *DBEntry) usable() bool {
	return ValidName(e.Name) && ValidVersion(e.Version) && ValidArch(e.Arch) && ValidFileName(e.Filename)
}
