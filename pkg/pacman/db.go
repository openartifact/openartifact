// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package pacman

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"
)

// DBEntry is one package of a repository database: what repo-add writes into
// the "desc" (and, for the files database, "files") entry of a package.
type DBEntry struct {
	Filename    string
	Name        string
	Base        string
	Version     string
	Description string
	Groups      []string
	CSize       int64
	ISize       int64
	MD5         string
	SHA256      string
	// PGPSig is the base64 of the binary detached signature of the package file,
	// empty for an unsigned package.
	PGPSig       string
	URL          string
	Licenses     []string
	Arch         string
	BuildDate    int64
	Packager     string
	Replaces     []string
	Conflicts    []string
	Provides     []string
	Depends      []string
	OptDepends   []string
	MakeDepends  []string
	CheckDepends []string
	// Files is the file list written to the "files" entry of the files database.
	Files []string
}

// Desc renders the "desc" entry exactly as repo-add writes it: one
// "%FIELD%" block per non-empty field, a blank line after each.
func (e *DBEntry) Desc() []byte {
	var b bytes.Buffer

	one := func(key, value string) {
		if value != "" {
			b.WriteString("%" + key + "%\n" + value + "\n\n")
		}
	}

	many := func(key string, values []string) {
		if len(values) == 0 {
			return
		}

		b.WriteString("%" + key + "%\n")

		for _, v := range values {
			b.WriteString(v + "\n")
		}

		b.WriteString("\n")
	}

	num := func(key string, n int64) {
		if n > 0 {
			one(key, strconv.FormatInt(n, 10))
		}
	}

	one("FILENAME", e.Filename)
	one("NAME", e.Name)
	one("BASE", e.Base)
	one("VERSION", e.Version)
	one("DESC", e.Description)
	many("GROUPS", e.Groups)
	num("CSIZE", e.CSize)
	num("ISIZE", e.ISize)
	one("MD5SUM", e.MD5)
	one("SHA256SUM", e.SHA256)
	one("PGPSIG", e.PGPSig)
	one("URL", e.URL)
	many("LICENSE", e.Licenses)
	one("ARCH", e.Arch)
	num("BUILDDATE", e.BuildDate)
	one("PACKAGER", e.Packager)
	many("REPLACES", e.Replaces)
	many("CONFLICTS", e.Conflicts)
	many("PROVIDES", e.Provides)
	many("DEPENDS", e.Depends)
	many("OPTDEPENDS", e.OptDepends)
	many("MAKEDEPENDS", e.MakeDepends)
	many("CHECKDEPENDS", e.CheckDepends)

	return b.Bytes()
}

// FilesEntry renders the "files" entry of the files database.
func (e *DBEntry) FilesEntry() []byte {
	var b bytes.Buffer

	b.WriteString("%FILES%\n")

	for _, f := range e.Files {
		b.WriteString(f + "\n")
	}

	return b.Bytes()
}

// DirName is the directory the package has in the database tarball.
func (e *DBEntry) DirName() string { return e.Name + "-" + e.Version }

// dbModTime is the fixed modification time of every database member, so equal
// input produces byte-identical (and so equally checksummed) databases.
var dbModTime = time.Unix(0, 0).UTC() //nolint:gochecknoglobals // constant

// WriteDB writes a repository database as a gzip-compressed tar: one directory
// per package holding its "desc" and, when withFiles is set, its "files" entry.
// Entries are written in the order of name and version so the output does not
// depend on the input order.
func WriteDB(w io.Writer, entries []DBEntry, withFiles bool) error {
	sorted := make([]*DBEntry, len(entries))
	for i := range entries {
		sorted[i] = &entries[i]
	}

	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}

		return sorted[i].Version < sorted[j].Version
	})

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	for _, e := range sorted {
		dir := e.DirName()

		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeDir, Name: dir + "/", Mode: 0o755, ModTime: dbModTime,
		}); err != nil {
			return fmt.Errorf("pacman: writing database: %w", err)
		}

		if err := writeMember(tw, dir+"/desc", e.Desc()); err != nil {
			return err
		}

		if withFiles {
			if err := writeMember(tw, dir+"/files", e.FilesEntry()); err != nil {
				return err
			}
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("pacman: writing database: %w", err)
	}

	if err := gz.Close(); err != nil {
		return fmt.Errorf("pacman: writing database: %w", err)
	}

	return nil
}

func writeMember(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: dbModTime,
	}); err != nil {
		return fmt.Errorf("pacman: writing database: %w", err)
	}

	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("pacman: writing database: %w", err)
	}

	return nil
}
