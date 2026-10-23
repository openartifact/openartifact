// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"unicode/utf8"

	rpm "github.com/openartifact/openartifact/pkg/rpm"
)

// The primary/filelists/other writers emit exactly what
// xml.MarshalIndent(..., "", "  ") produced for the former struct models
// (empty elements as <a></a>, two-space indent, no trailing newline), but
// append straight into a reusable buffer that is flushed to the destination
// every flushThreshold bytes, so memory stays flat regardless of repo size.

const (
	xmlHeader      = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
	flushThreshold = 64 << 10

	nsRPM       = "http://linux.duke.edu/metadata/rpm"
	nsCommon    = "http://linux.duke.edu/metadata/common"
	nsFilelists = "http://linux.duke.edu/metadata/filelists"
	nsOther     = "http://linux.duke.edu/metadata/other"
)

// xmlOut accumulates XML text and writes it out in large chunks. The first
// write error sticks; later appends are cheap no-ops for the caller to ignore
// until finish reports it.
type xmlOut struct {
	w   io.Writer
	buf []byte
	err error
}

func newXMLOut(w io.Writer) *xmlOut {
	return &xmlOut{w: w, buf: make([]byte, 0, flushThreshold+flushThreshold/4)}
}

func (o *xmlOut) raw(s string) { o.buf = append(o.buf, s...) }

func (o *xmlOut) i64(v int64) { o.buf = strconv.AppendInt(o.buf, v, 10) }

func (o *xmlOut) u32(v uint32) { o.buf = strconv.AppendUint(o.buf, uint64(v), 10) }

// text appends s escaped like encoding/xml.EscapeText (used for both
// character data and attribute values).
func (o *xmlOut) text(s string) {
	o.buf = appendEscaped(o.buf, s)
}

// attr appends ` name="value"` with the value escaped.
func (o *xmlOut) attr(name, value string) {
	o.buf = append(o.buf, ' ')
	o.buf = append(o.buf, name...)
	o.buf = append(o.buf, '=', '"')
	o.buf = appendEscaped(o.buf, value)
	o.buf = append(o.buf, '"')
}

// optAttr is attr that skips empty values (the omitempty behaviour).
func (o *xmlOut) optAttr(name, value string) {
	if value != "" {
		o.attr(name, value)
	}
}

// elem appends `\n<indent><tag>text</tag>`.
func (o *xmlOut) elem(indent, tag, value string) {
	o.open(indent, tag)
	o.buf = append(o.buf, '>')
	o.text(value)
	o.closeTag(tag)
}

// optElem is elem that skips empty values.
func (o *xmlOut) optElem(indent, tag, value string) {
	if value != "" {
		o.elem(indent, tag, value)
	}
}

// open starts a tag (`\n<indent><tag`) leaving it open for attributes.
func (o *xmlOut) open(indent, tag string) {
	o.buf = append(o.buf, '\n')
	o.buf = append(o.buf, indent...)
	o.buf = append(o.buf, '<')
	o.buf = append(o.buf, tag...)
}

func (o *xmlOut) closeTag(tag string) {
	o.buf = append(o.buf, '<', '/')
	o.buf = append(o.buf, tag...)
	o.buf = append(o.buf, '>')
}

// emptyClose ends an attribute-only element: `></tag>`.
func (o *xmlOut) emptyClose(tag string) {
	o.buf = append(o.buf, '>')
	o.closeTag(tag)
}

// version writes `<version [epoch=".."] ver=".." rel=".."></version>`.
func (o *xmlOut) version(indent string, p *rpm.Package) {
	o.open(indent, "version")

	if p.Epoch > 0 {
		o.buf = append(o.buf, ` epoch="`...)
		o.u32(p.Epoch)
		o.buf = append(o.buf, '"')
	}

	o.attr("ver", p.Version)
	o.attr("rel", p.Release)
	o.emptyClose("version")
}

func (o *xmlOut) flush() {
	if o.err == nil && len(o.buf) > 0 {
		_, o.err = o.w.Write(o.buf)
	}

	o.buf = o.buf[:0]
}

func (o *xmlOut) flushIfFull() {
	if len(o.buf) >= flushThreshold {
		o.flush()
	}
}

func (o *xmlOut) finish() error {
	o.flush()

	return o.err
}

const (
	escQuot = "&#34;"
	escApos = "&#39;"
	escAmp  = "&amp;"
	escLT   = "&lt;"
	escGT   = "&gt;"
	escTab  = "&#x9;"
	escNL   = "&#xA;"
	escCR   = "&#xD;"
	escFFFD = "\uFFFD"
)

// appendEscaped appends s to dst escaped exactly like encoding/xml's
// EscapeText (the escaping used for chardata and attribute values).
func appendEscaped(dst []byte, s string) []byte {
	last := 0

	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c < utf8.RuneSelf && c != '"' && c != '\'' && c != '&' && c != '<' && c != '>' {
			i++
			continue
		}

		r, width := utf8.DecodeRuneInString(s[i:])

		esc, ok := xmlEscape(r, width)
		if !ok {
			i += width
			continue
		}

		dst = append(dst, s[last:i]...)
		dst = append(dst, esc...)
		i += width
		last = i
	}

	return append(dst, s[last:]...)
}

// xmlEscape returns the replacement for r (decoded with the given width), or
// false when r is emitted unchanged.
func xmlEscape(r rune, width int) (string, bool) {
	switch r {
	case '"':
		return escQuot, true
	case '\'':
		return escApos, true
	case '&':
		return escAmp, true
	case '<':
		return escLT, true
	case '>':
		return escGT, true
	case '\t':
		return escTab, true
	case '\n':
		return escNL, true
	case '\r':
		return escCR, true
	}

	if !isXMLChar(r) || (r == utf8.RuneError && width == 1) {
		return escFFFD, true
	}

	return "", false
}

// isXMLChar reports whether r is in the XML 1.0 Char production.
func isXMLChar(r rune) bool {
	return r == 0x09 || r == 0x0A || r == 0x0D ||
		r >= 0x20 && r <= 0xD7FF ||
		r >= 0xE000 && r <= 0xFFFD ||
		r >= 0x10000 && r <= 0x10FFFF
}

// DocKind selects which repodata document a DocWriter renders.
type DocKind int

const (
	DocPrimary DocKind = iota
	DocFilelists
	DocOther
)

// DocWriter streams one repodata document (primary, filelists or other)
// package by package, for a caller that merges packages from several sources:
// rendered ones from the database (Package) and verbatim elements read from an
// upstream document (Raw). count is the number of packages the caller will
// write; it is the document's packages attribute.
type DocWriter struct {
	kind  DocKind
	o     *xmlOut
	rf    *requiresFilter
	wrote bool
}

// NewDocWriter writes the document header of kind to w.
func NewDocWriter(w io.Writer, kind DocKind, count int) *DocWriter {
	d := &DocWriter{kind: kind, o: newXMLOut(w)}

	d.o.raw(xmlHeader)

	switch kind {
	case DocPrimary:
		d.rf = newRequiresFilter()
		d.o.raw(`<metadata xmlns="` + nsCommon + `" xmlns:rpm="` + nsRPM + `" packages="`)
	case DocFilelists:
		d.o.raw(`<filelists xmlns="` + nsFilelists + `" packages="`)
	case DocOther:
		d.o.raw(`<otherdata xmlns="` + nsOther + `" packages="`)
	}

	d.o.i64(int64(count))
	d.o.raw(`">`)

	return d
}

// Package renders p into the document.
func (d *DocWriter) Package(p *rpm.Package) {
	d.wrote = true

	switch d.kind {
	case DocPrimary:
		writePrimaryPackage(d.o, d.rf, p)
	case DocFilelists:
		writeFilelistsPackage(d.o, p)
	case DocOther:
		writeOtherPackage(d.o, p)
	}

	d.o.flushIfFull()
}

// Raw appends an already serialized <package> element.
func (d *DocWriter) Raw(element []byte) {
	d.wrote = true

	d.o.raw("\n  ")
	d.o.buf = append(d.o.buf, element...)
	d.o.flushIfFull()
}

// Close writes the closing tag and reports the first write error.
func (d *DocWriter) Close() error {
	if d.wrote {
		d.o.raw("\n")
	}

	switch d.kind {
	case DocPrimary:
		d.o.raw("</metadata>")
	case DocFilelists:
		d.o.raw("</filelists>")
	case DocOther:
		d.o.raw("</otherdata>")
	}

	return d.o.finish()
}

// WritePrimaryXML streams primary.xml for packages to w.
func WritePrimaryXML(w io.Writer, packages []rpm.Package) error {
	return writeDocument(w, DocPrimary, packages)
}

func writeDocument(w io.Writer, kind DocKind, packages []rpm.Package) error {
	d := NewDocWriter(w, kind, len(packages))

	for i := range packages {
		d.Package(&packages[i])
	}

	return d.Close()
}

// PackageLocation is the href a hosted package is listed under in primary.xml
// (and served from): Packages/<name>-<version>-<release>.<arch>.rpm.
func PackageLocation(p *rpm.Package) string {
	return "Packages/" + url.PathEscape(p.Name+"-"+p.Version+"-"+p.Release+"."+p.Arch+".rpm")
}

func writePrimaryPackage(o *xmlOut, rf *requiresFilter, p *rpm.Package) {
	o.raw("\n  <package type=\"rpm\">")
	o.elem("    ", "name", p.Name)
	o.elem("    ", "arch", p.Arch)
	o.version("    ", p)

	o.open("    ", "checksum")
	o.raw(` type="sha256" pkgid="YES">`)
	o.text(p.SHA256)
	o.closeTag("checksum")

	o.elem("    ", "summary", p.Summary)
	o.elem("    ", "description", p.Description)
	o.optElem("    ", "packager", p.Packager)
	o.optElem("    ", "url", p.URL)

	o.open("    ", "time")
	o.raw(` file="`)

	if p.FileTime > 0 {
		o.i64(p.FileTime)
	} else {
		o.i64(p.BuildTime)
	}

	o.raw(`" build="`)
	o.i64(p.BuildTime)
	o.raw(`"`)
	o.emptyClose("time")

	o.open("    ", "size")
	o.raw(` package="`)
	o.i64(p.PackageSize)
	o.raw(`" installed="`)
	o.i64(p.InstalledSize)
	o.raw(`" archive="`)
	o.i64(p.ArchiveSize)
	o.raw(`"`)
	o.emptyClose("size")

	// The element is lower-case: libdnf and createrepo_c only recognise
	// <location>, and cannot download a package listed under any other name.
	o.open("    ", "location")
	o.attr("href", PackageLocation(p))
	o.emptyClose("location")

	writePrimaryFormat(o, rf, p)

	o.raw("\n  </package>")
}

func writePrimaryFormat(o *xmlOut, rf *requiresFilter, p *rpm.Package) {
	o.raw("\n    <format xmlns:rpm=\"" + nsRPM + "\">")
	o.optElem("      ", "rpm:license", p.License)
	o.optElem("      ", "rpm:vendor", p.Vendor)
	o.optElem("      ", "rpm:group", p.Group)
	o.optElem("      ", "rpm:buildhost", p.BuildHost)
	o.optElem("      ", "rpm:sourcerpm", p.SourceRPM)

	o.open("      ", "rpm:header-range")
	o.raw(` start="`)
	o.i64(p.HeaderStart)
	o.raw(`" end="`)
	o.i64(p.HeaderEnd)
	o.raw(`"`)
	o.emptyClose("rpm:header-range")

	writeDepList(o, "rpm:provides", p.Provides, false)
	writeDepList(o, "rpm:requires", rf.filter(p), true)
	writeDepList(o, "rpm:conflicts", p.Conflicts, false)
	writeDepList(o, "rpm:obsoletes", p.Obsoletes, false)
	writeDepList(o, "rpm:suggests", p.Suggests, false)
	writeDepList(o, "rpm:enhances", p.Enhances, false)
	writeDepList(o, "rpm:recommends", p.Recommends, false)
	writeDepList(o, "rpm:supplements", p.Supplements, false)

	o.raw("\n    </format>")
}

// writeDepList renders a dependency list the way createrepo_c does. When
// isRequires is set, rpmlib(...) features are skipped and script-time
// requirements carry pre="1". The wrapper element is always emitted, empty
// when no entry survives.
func writeDepList(o *xmlOut, tag string, deps []rpm.Dependency, isRequires bool) {
	o.open("      ", tag)
	o.raw(">")

	wrote := false

	for i := range deps {
		d := &deps[i]
		if isRequires && d.IsRPMLib() {
			continue
		}

		wrote = true

		o.open("        ", "rpm:entry")
		o.attr("name", d.Name)

		flags := rpm.SenseFlagsString(d.Flags)
		if flags != "" {
			ver, rel := rpm.SplitVersionRelease(d.Version)

			o.attr("flags", flags)
			o.raw(` epoch="`)
			o.u32(d.Epoch)
			o.raw(`"`)
			o.optAttr("ver", ver)
			o.optAttr("rel", rel)
		}

		if isRequires && d.IsPre() {
			o.raw(` pre="1"`)
		}

		o.emptyClose("rpm:entry")
	}

	if wrote {
		o.raw("\n      ")
	}

	o.closeTag(tag)
}

// WriteFilelistsXML streams filelists.xml for packages to w.
func WriteFilelistsXML(w io.Writer, packages []rpm.Package) error {
	return writeDocument(w, DocFilelists, packages)
}

func writeFilelistsPackage(o *xmlOut, p *rpm.Package) {
	writePackageRef(o, p)

	for j := range p.Files {
		o.elem("    ", "file", p.Files[j].Path)
	}

	o.raw("\n  </package>")
}

// WriteOtherXML streams other.xml for packages to w.
func WriteOtherXML(w io.Writer, packages []rpm.Package) error {
	return writeDocument(w, DocOther, packages)
}

func writeOtherPackage(o *xmlOut, p *rpm.Package) {
	writePackageRef(o, p)

	for j := range p.Changelogs {
		cl := &p.Changelogs[j]

		o.open("    ", "changelog")
		o.attr("author", cl.Author)
		o.raw(` date="`)
		o.i64(cl.Time)
		o.raw(`">`)
		o.text(cl.Description)
		o.closeTag("changelog")
	}

	o.raw("\n  </package>")
}

// writePackageRef opens a filelists/other <package> element and its <version>.
func writePackageRef(o *xmlOut, p *rpm.Package) {
	o.raw("\n  <package")
	o.attr("pkgid", p.SHA256)
	o.attr("name", p.Name)
	o.attr("arch", p.Arch)
	o.raw(">")
	o.version("    ", p)
}

func generateXML(kind string, write func(io.Writer) error) ([]byte, error) {
	var buf bytes.Buffer

	if err := write(&buf); err != nil {
		return nil, fmt.Errorf("rpm: marshaling %s.xml: %w", kind, err)
	}

	return buf.Bytes(), nil
}

// GeneratePrimaryXML renders primary.xml in memory; see WritePrimaryXML to
// stream it instead. baseURL is unused and kept for API compatibility.
func GeneratePrimaryXML(packages []rpm.Package, _ string) ([]byte, error) {
	return generateXML("primary", func(w io.Writer) error { return WritePrimaryXML(w, packages) })
}

// GenerateFilelistsXML renders filelists.xml in memory.
func GenerateFilelistsXML(packages []rpm.Package) ([]byte, error) {
	return generateXML("filelists", func(w io.Writer) error { return WriteFilelistsXML(w, packages) })
}

// GenerateOtherXML renders other.xml in memory.
func GenerateOtherXML(packages []rpm.Package) ([]byte, error) {
	return generateXML("other", func(w io.Writer) error { return WriteOtherXML(w, packages) })
}
