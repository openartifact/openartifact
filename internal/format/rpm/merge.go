// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"go.yaml.in/yaml/v3"
)

// maxUpstreamDocument caps the decompressed size of one upstream repodata
// document read for a merge; a larger one (a decompression bomb) ends as a
// truncated, hence malformed, document.
const maxUpstreamDocument = 16 << 30

// PackageInfo identifies a <package> element of an upstream primary, filelists
// or other document. Filelists and other documents carry no checksum element
// but the pkgid attribute; primary carries the location.
type PackageInfo struct {
	Name  string
	Arch  string
	Epoch string
	Ver   string
	Rel   string
	// PkgID is the package checksum (primary: the checksum element, filelists
	// and other: the pkgid attribute).
	PkgID string
	// Href is the package location of a primary element.
	Href string
}

// NEVRA is the identity two members' packages collide on.
func (i PackageInfo) NEVRA() string {
	epoch := i.Epoch
	if epoch == "" {
		epoch = "0"
	}

	return i.Name + "-" + epoch + ":" + i.Ver + "-" + i.Rel + "." + i.Arch
}

// errTruncatedDocument reports an upstream XML document that ends inside an element.
var errTruncatedDocument = errors.New("rpm: parsing upstream metadata: document ends inside an element")

// ScanPackages reads the <package> elements of a primary, filelists or other
// document in order. When withElement is set fn also receives the element
// serialized (without the xml:base attribute of a location, which would send a
// client to another host); the slice is only valid during the call.
func ScanPackages(r io.Reader, withElement bool, fn func(info PackageInfo, element []byte) error) error {
	d := xml.NewDecoder(r)
	s := packageScanner{withElement: withElement}

	for {
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			if s.depth != 0 {
				return errTruncatedDocument
			}

			return nil
		}

		if err != nil {
			return fmt.Errorf("rpm: parsing upstream metadata: %w", err)
		}

		done, err := s.token(tok)
		if err != nil {
			return err
		}

		if done {
			s.info.Name, s.info.Arch = strings.TrimSpace(s.info.Name), strings.TrimSpace(s.info.Arch)
			s.info.PkgID = strings.TrimSpace(s.info.PkgID)

			if err := fn(s.info, s.out.Bytes()); err != nil {
				return err
			}

			s.reset()
		}
	}
}

// packageScanner walks the RawToken stream; depth 1 is the document root, so a
// <package> starts at depth 2.
type packageScanner struct {
	withElement bool

	depth   int
	inPkg   bool
	pkgBase int // depth at which the current package started
	info    PackageInfo
	out     bytes.Buffer
	capture *string // text sink of a leaf child being read
}

func (s *packageScanner) reset() {
	s.inPkg, s.capture = false, nil
	s.info = PackageInfo{}

	s.out.Reset()
}

func (s *packageScanner) token(tok xml.Token) (bool, error) {
	switch t := tok.(type) {
	case xml.StartElement:
		s.depth++

		if !s.inPkg {
			if s.depth == 2 && t.Name.Space == "" && t.Name.Local == "package" {
				s.inPkg, s.pkgBase = true, s.depth
				s.startPackage(t)
				s.writeStart(t)
			}

			return false, nil
		}

		if s.depth == s.pkgBase+1 && t.Name.Space == "" {
			s.child(t)
		}

		s.writeStart(t)
	case xml.EndElement:
		defer func() { s.depth-- }()

		if !s.inPkg {
			return false, nil
		}

		s.capture = nil

		s.writeEnd(t)

		return s.depth == s.pkgBase, nil
	case xml.CharData:
		if !s.inPkg {
			return false, nil
		}

		if s.capture != nil {
			*s.capture += string(t)
		}

		if s.withElement {
			_ = xml.EscapeText(&s.out, t)
		}
	}

	return false, nil
}

func (s *packageScanner) startPackage(t xml.StartElement) {
	for _, a := range t.Attr {
		if a.Name.Space != "" {
			continue
		}

		switch a.Name.Local {
		case "pkgid":
			s.info.PkgID = a.Value
		case "name":
			s.info.Name = a.Value
		case "arch":
			s.info.Arch = a.Value
		}
	}
}

// child records the identifying direct children of a <package>.
func (s *packageScanner) child(t xml.StartElement) {
	switch strings.ToLower(t.Name.Local) {
	case "name":
		s.capture = &s.info.Name
	case "arch":
		s.capture = &s.info.Arch
	case "checksum":
		s.info.PkgID = ""
		s.capture = &s.info.PkgID
	case "version":
		for _, a := range t.Attr {
			switch a.Name.Local {
			case "epoch":
				s.info.Epoch = a.Value
			case "ver":
				s.info.Ver = a.Value
			case "rel":
				s.info.Rel = a.Value
			}
		}
	case "location":
		for _, a := range t.Attr {
			if a.Name.Space == "" && a.Name.Local == "href" {
				s.info.Href = a.Value
			}
		}
	}
}

func (s *packageScanner) writeStart(t xml.StartElement) {
	if !s.withElement {
		return
	}

	s.out.WriteByte('<')
	s.out.WriteString(qname(t.Name))

	for _, a := range t.Attr {
		if a.Name.Space == "xml" && a.Name.Local == "base" {
			continue
		}

		s.out.WriteByte(' ')
		s.out.WriteString(qname(a.Name))
		s.out.WriteString(`="`)
		_ = xml.EscapeText(&s.out, []byte(a.Value))
		s.out.WriteByte('"')
	}

	s.out.WriteByte('>')
}

func (s *packageScanner) writeEnd(t xml.EndElement) {
	if !s.withElement {
		return
	}

	s.out.WriteString("</")
	s.out.WriteString(qname(t.Name))
	s.out.WriteByte('>')
}

func qname(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}

	return n.Space + ":" + n.Local
}

// SafeLocation cleans a package href and decodes its percent-escapes into the
// repository-relative path it is served from. ok is false for a location that
// is absolute or leaves the repository.
func SafeLocation(href string) (string, bool) {
	p, err := url.PathUnescape(href)
	if err != nil || p == "" || strings.ContainsAny(p, "\\\x00") {
		return "", false
	}

	p = path.Clean(p)
	if p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
		return "", false
	}

	return p, true
}

// ParseRepomd returns the location of each data type (the first of each) of an
// upstream repomd.xml.
func ParseRepomd(r io.Reader) (map[string]string, error) {
	var doc struct {
		Data []struct {
			Type     string `xml:"type,attr"`
			Location struct {
				Href string `xml:"href,attr"`
			} `xml:"location"`
		} `xml:"data"`
	}

	if err := xml.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("rpm: parsing upstream repomd.xml: %w", err)
	}

	out := make(map[string]string, len(doc.Data))

	for _, d := range doc.Data {
		if _, dup := out[d.Type]; !dup && d.Location.Href != "" {
			out[d.Type] = d.Location.Href
		}
	}

	return out, nil
}

// OpenMetadata wraps r according to the extension of the file name it was
// fetched as (.gz, .xz, .zst, .bz2 or none). Closing the result closes r.
func OpenMetadata(r io.ReadCloser, name string) (io.ReadCloser, error) {
	var (
		dec  io.Reader
		stop func()
		err  error
	)

	switch {
	case strings.HasSuffix(name, ".gz"):
		var gr *gzip.Reader

		if gr, err = gzip.NewReader(r); err == nil {
			dec, stop = gr, func() { _ = gr.Close() }
		}
	case strings.HasSuffix(name, ".xz"):
		dec, err = xz.NewReader(r)
	case strings.HasSuffix(name, ".zst"):
		var zr *zstd.Decoder

		if zr, err = zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1<<30)); err == nil {
			dec, stop = zr, zr.Close
		}
	case strings.HasSuffix(name, ".bz2"):
		dec = bzip2.NewReader(r)
	default:
		return r, nil
	}

	if err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("rpm: opening %s: %w", name, err)
	}

	return &decompressed{Reader: io.LimitReader(dec, maxUpstreamDocument), src: r, stop: stop}, nil
}

type decompressed struct {
	io.Reader

	src  io.Closer
	stop func()
}

func (d *decompressed) Close() error {
	if d.stop != nil {
		d.stop()
	}

	return d.src.Close()
}

// MergeModules concatenates the modulemd documents of several modules.yaml
// files into one, in order. A document an earlier file already provided (the
// same module stream version and context, the same defaults or obsoletes
// record) is dropped, so the first member wins like it does for packages. It
// returns the number of documents written; a file whose documents cannot be
// parsed contributes none of them.
func MergeModules(w io.Writer, sources []io.Reader) (int, error) {
	seen := make(map[string]struct{})
	n := 0

	for _, src := range sources {
		data, err := io.ReadAll(src)
		if err != nil {
			return n, fmt.Errorf("rpm: reading modules.yaml: %w", err)
		}

		for _, doc := range splitYAMLDocuments(string(data)) {
			key, ok := moduleKey(doc)
			if !ok {
				continue
			}

			if _, dup := seen[key]; dup {
				continue
			}

			seen[key] = struct{}{}

			if _, err := io.WriteString(w, "---\n"+doc+"\n"); err != nil {
				return n, fmt.Errorf("rpm: writing modules.yaml: %w", err)
			}

			n++
		}
	}

	return n, nil
}

// splitYAMLDocuments splits a multi-document stream at its "---" lines; the
// "..." terminators modulemd uses stay with their document.
func splitYAMLDocuments(text string) []string {
	var (
		docs []string
		cur  []string
	)

	flush := func() {
		if d := strings.TrimSpace(strings.Join(cur, "\n")); d != "" && d != "..." {
			docs = append(docs, d)
		}

		cur = cur[:0]
	}

	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimRight(line, " \t\r") == "---" {
			flush()
			continue
		}

		cur = append(cur, strings.TrimRight(line, "\r"))
	}

	flush()

	return docs
}

// moduleKey is the identity of a modulemd, modulemd-defaults or
// modulemd-obsoletes document.
func moduleKey(doc string) (string, bool) {
	var parsed struct {
		Document string         `yaml:"document"`
		Data     map[string]any `yaml:"data"`
	}

	if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil || parsed.Document == "" {
		return "", false
	}

	var b strings.Builder

	b.WriteString(parsed.Document)

	switch parsed.Document {
	case "modulemd", "modulemd-packager":
		for _, f := range []string{"name", "stream", "version", "context", "arch"} {
			b.WriteString("|" + yamlScalar(parsed.Data[f]))
		}
	case "modulemd-defaults":
		b.WriteString("|" + yamlScalar(parsed.Data["module"]))
	default:
		for _, f := range []string{"module", "stream", "context", "modified", "message"} {
			b.WriteString("|" + yamlScalar(parsed.Data[f]))
		}
	}

	return b.String(), true
}

func yamlScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case uint64:
		return strconv.FormatUint(t, 10)
	}

	return fmt.Sprint(v)
}
