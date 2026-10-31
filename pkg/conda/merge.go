// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
)

// MemberOptions tunes Builder.Add for one member's document.
type MemberOptions struct {
	// Hidden reports whether entries of the named package are dropped. It is
	// asked about the name in the file name and about the entry's own "name"
	// field, so a document cannot smuggle a package past it by lying in one.
	Hidden func(name string) bool
	// Drop, when set, is asked about every entry that passed the checks above,
	// with its parsed file name (its raw JSON is only valid during the call),
	// and drops the entry when it returns true.
	Drop func(file File, entry json.RawMessage) bool
	// MaxBytes bounds the decoded size of the member's document.
	MaxBytes int64
}

// MemberStats counts what Builder.Add did with one member's entries.
type MemberStats struct {
	// Added entries went into the merged document.
	Added int
	// Duplicates were dropped: an earlier member lists the same file.
	Duplicates int
	// Hidden entries were dropped by MemberOptions.Hidden.
	Hidden int
	// Dropped entries were dropped by MemberOptions.Drop.
	Dropped int
	// Invalid entries were dropped: a key that is no package file name, or one
	// listed in the wrong section.
	Invalid int
}

// Builder merges the repodata.json documents of several members into one for
// a subdirectory, in a single streaming pass: entries go to temporary files,
// so memory is one entry plus the set of file names seen. Earlier members win
// a file name. Close it to remove the temporary files.
type Builder struct {
	subdir string
	tmpDir string
	secs   [2]*section
	seen   map[string]struct{}
}

// section spools the entries of one package map.
type section struct {
	f *os.File
	w *bufio.Writer
	n int
}

// NewBuilder starts a merged document for subdir. Temporary files go to
// tmpDir ("" for the system default).
func NewBuilder(subdir, tmpDir string) (*Builder, error) {
	b := &Builder{subdir: subdir, tmpDir: tmpDir, seen: map[string]struct{}{}}

	for i := range b.secs {
		f, err := os.CreateTemp(tmpDir, "openartifact-conda-*")
		if err != nil {
			b.Close()
			return nil, fmt.Errorf("create spool: %w", err)
		}

		b.secs[i] = &section{f: f, w: bufio.NewWriterSize(f, 256<<10)}
	}

	return b, nil
}

// Close removes the temporary files. A Document returned by Finish owns them
// afterwards and Close leaves them alone.
func (b *Builder) Close() {
	for i, s := range b.secs {
		if s != nil {
			removeSpool(s.f)
			b.secs[i] = nil
		}
	}

	b.seen = nil
}

func removeSpool(f *os.File) {
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
}

// Add merges the repodata document in r (plain JSON or a zstd or bzip2
// stream) into the result. On error the Builder is unusable: Close it.
func (b *Builder) Add(r io.Reader, o MemberOptions) (MemberStats, error) {
	var st MemberStats

	rc, _, err := Open(r, o.MaxBytes)
	if err != nil {
		return st, err
	}
	defer func() { _ = rc.Close() }()

	err = Scan(rc, o.MaxBytes, Visitor{Entry: func(s Section, filename string, raw json.RawMessage) error {
		return b.addEntry(s, filename, raw, o, &st)
	}})

	return st, err
}

func (b *Builder) addEntry(s Section, filename string, raw json.RawMessage, o MemberOptions, st *MemberStats) error {
	f, ok := ParseFilename(filename)
	if !ok || f.Ext != s.Ext() {
		st.Invalid++
		return nil
	}

	if _, dup := b.seen[filename]; dup {
		st.Duplicates++
		return nil
	}

	if o.Hidden != nil && (o.Hidden(f.Name) || o.Hidden(entryName(raw))) {
		st.Hidden++
		return nil
	}

	if o.Drop != nil && o.Drop(f, raw) {
		st.Dropped++
		return nil
	}

	b.seen[filename] = struct{}{}
	st.Added++

	sec := b.secs[s]
	if sec.n > 0 {
		_ = sec.w.WriteByte(',')
	}

	sec.n++

	key, _ := json.Marshal(filename) //nolint:errchkjson // a string always encodes
	_, _ = sec.w.Write(key)
	_ = sec.w.WriteByte(':')
	_, err := sec.w.Write(raw)

	return err //nolint:wrapcheck // surfaced as a spool write error by the caller
}

// entryName is the "name" field of an entry, "" when it has none.
func entryName(raw json.RawMessage) string {
	var doc struct {
		Name string `json:"name"`
	}

	_ = json.Unmarshal(raw, &doc)

	return doc.Name
}

// Document is a merged repodata.json held in temporary files.
type Document struct {
	parts []part
	size  int64
	files []*os.File
}

type part struct {
	lit []byte
	src *io.SectionReader
}

// Finish completes the document. The Builder must not be used afterwards
// except to Close it, which keeps the Document's files.
func (b *Builder) Finish() (*Document, error) {
	info, _ := json.Marshal(map[string]string{"subdir": b.subdir}) //nolint:errchkjson // strings always encode
	d := &Document{}

	lit := func(parts ...string) {
		var buf bytes.Buffer
		for _, p := range parts {
			buf.WriteString(p)
		}

		d.parts = append(d.parts, part{lit: buf.Bytes()})
		d.size += int64(buf.Len())
	}

	for i, s := range b.secs {
		if err := s.w.Flush(); err != nil {
			return nil, fmt.Errorf("flush spool: %w", err)
		}

		st, err := s.f.Stat()
		if err != nil {
			return nil, fmt.Errorf("stat spool: %w", err)
		}

		if i == 0 {
			lit(`{"info":`, string(info), `,"`, SectionTarBz2.Key(), `":{`)
		} else {
			lit(`},"`, SectionConda.Key(), `":{`)
		}

		d.parts = append(d.parts, part{src: io.NewSectionReader(s.f, 0, st.Size())})
		d.size += st.Size()
	}

	lit(`},"repodata_version":1}`)

	d.files = []*os.File{b.secs[0].f, b.secs[1].f}
	b.secs = [2]*section{}
	b.seen = nil

	return d, nil
}

// Size is the length of the encoded document in bytes.
func (d *Document) Size() int64 { return d.size }

// WriteTo writes the document to w.
func (d *Document) WriteTo(w io.Writer) (int64, error) {
	var total int64

	for _, p := range d.parts {
		var (
			n   int64
			err error
		)

		if p.src != nil {
			n, err = io.Copy(w, io.NewSectionReader(p.src, 0, p.src.Size()))
		} else {
			var m int
			m, err = w.Write(p.lit)
			n = int64(m)
		}

		total += n

		if err != nil {
			return total, err //nolint:wrapcheck // the caller's writer error
		}
	}

	return total, nil
}

// Close removes the temporary files.
func (d *Document) Close() {
	for _, f := range slices.Clone(d.files) {
		removeSpool(f)
	}

	d.files = nil
}
