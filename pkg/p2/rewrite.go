// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package p2

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

// Limits of the documents the proxy rewrites. The documents come from an
// upstream the operator trusts to publish software, not to be benign: every
// bound below holds for hostile input.
const (
	// MaxXMLBytes bounds the XML of one document after decompression. A real
	// artifacts.xml of a large update site is some tens of MiB.
	MaxXMLBytes int64 = 512 << 20

	// maxTokenBytes bounds one XML token (a tag with its attributes, a run of
	// text, a comment); the rewriter holds a token in memory and nothing else.
	maxTokenBytes = 8 << 20

	// maxDepth bounds the nesting of elements. p2 documents are five deep.
	maxDepth = 64

	// maxPropsBytes bounds the repository-level <properties> element, which is
	// held in memory to count what is left of it.
	maxPropsBytes = 1 << 20
)

var (
	// ErrNotRepository is returned for a document that is not a p2 repository
	// (a captive portal page that parses as XML, a second root element).
	ErrNotRepository = errors.New("p2: not a repository document")
	// ErrDirective is returned for a DTD or any other <!...> directive. p2
	// documents have none, and refusing them keeps entity tricks out.
	ErrDirective = errors.New("p2: directive in a repository document")
	// ErrTooDeep is returned for elements nested deeper than p2 documents are.
	ErrTooDeep = errors.New("p2: document nests too deeply")
	// ErrTokenTooLarge is returned for a single XML token over its bound.
	ErrTokenTooLarge = errors.New("p2: XML token too large")
	// ErrTruncated is returned for a document that ends inside an element.
	ErrTruncated = errors.New("p2: document ends inside an element")
	// ErrTooLarge is returned for a document whose XML exceeds MaxXMLBytes.
	ErrTooLarge = errors.New("p2: document too large")
)

// strippedProperties are the repository properties that send a client around the
// proxy: the mirror list (a download redirector that answers with other hosts)
// and the statistics endpoint every download is reported to.
var strippedProperties = map[string]bool{ //nolint:gochecknoglobals // constant table
	"p2.mirrorsURL": true,
	"p2.statsURI":   true,
}

// Options tune RewriteXML.
type Options struct {
	// Relocate maps the location of a composite repository's child (see
	// Relocator.Relocate); nil leaves the children alone.
	Relocate func(location string) (string, bool)
}

// RewriteXML copies the p2 repository document in src to dst with the
// repository properties p2.mirrorsURL and p2.statsURI removed (the size of the
// properties element follows) and the child locations of a composite repository
// relocated through opts. Everything else is copied byte for byte: the rewriter
// tracks the offsets of the tokens it reads and passes the bytes between them
// through, so attribute quoting, whitespace and comments survive.
//
// It reads src once, holding one token at a time, and refuses what a repository
// document never contains: a root other than <repository>, a directive (DTD),
// nesting beyond maxDepth, a token over maxTokenBytes, a document that ends
// inside an element. The decoder does not expand entities, so a document cannot
// grow in the rewriter; the caller bounds the length of src.
func RewriteXML(dst io.Writer, src io.Reader, opts Options) error {
	in := &capture{r: bufio.NewReaderSize(src, 64<<10)}
	dec := xml.NewDecoder(in)
	out := bufio.NewWriterSize(dst, 64<<10)
	rw := &rewriter{out: out, opts: opts}

	var done int64

	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return fmt.Errorf("p2: read document: %w", err)
		}

		end := dec.InputOffset()

		raw, ok := in.take(end - done)
		if !ok {
			return fmt.Errorf("%w: lost track of the token offsets", ErrNotRepository)
		}

		done = end

		if err := rw.token(tok, raw); err != nil {
			return err
		}
	}

	if len(rw.stack) != 0 {
		return ErrTruncated
	}

	if !rw.rooted {
		return ErrNotRepository
	}

	return out.Flush() //nolint:wrapcheck // the destination's own error
}

// capture is the byte source of the XML decoder. Implementing io.ByteReader
// stops the decoder from reading ahead, so the bytes it has taken are exactly
// the tokens it returned (plus at most the one byte it pushed back); capture
// keeps them until take hands them out.
type capture struct {
	r    *bufio.Reader
	pend []byte
	// drop is how many bytes at the front of pend were handed out by take; they
	// are removed at the next read or take so the slice take returned stays valid.
	drop int
}

// compact removes the bytes take handed out.
func (c *capture) compact() {
	if c.drop > 0 {
		n := copy(c.pend, c.pend[c.drop:])
		c.pend, c.drop = c.pend[:n], 0
	}
}

func (c *capture) ReadByte() (byte, error) {
	c.compact()

	b, err := c.r.ReadByte()
	if err != nil {
		return 0, err //nolint:wrapcheck // io.EOF must reach the decoder unchanged
	}

	if len(c.pend) >= maxTokenBytes {
		return 0, ErrTokenTooLarge
	}

	c.pend = append(c.pend, b)

	return b, nil
}

// Read satisfies io.Reader; the decoder reads through ReadByte.
func (c *capture) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	b, err := c.ReadByte()
	if err != nil {
		return 0, err
	}

	p[0] = b

	return 1, nil
}

// take returns the next n bytes, valid until the next read or take.
func (c *capture) take(n int64) ([]byte, bool) {
	c.compact()

	if n < 0 || n > int64(len(c.pend)) {
		return nil, false
	}

	c.drop = int(n)

	return c.pend[:n], true
}

// rewriter is the state of one RewriteXML run.
type rewriter struct {
	out  *bufio.Writer
	opts Options

	// stack holds the local names of the open elements.
	stack  []string
	rooted bool
	// skip is the depth of the element being dropped, 0 when none is.
	skip int
	// props collects the repository-level properties element while it is open.
	props *properties
}

// properties is the repository-level <properties> element, held back until its
// end so that its size attribute can be corrected.
type properties struct {
	start   []byte
	body    bytes.Buffer
	kept    int
	dropped int
}

func (r *rewriter) token(tok xml.Token, raw []byte) error {
	switch t := tok.(type) {
	case xml.Directive:
		return ErrDirective
	case xml.StartElement:
		return r.start(t, raw)
	case xml.EndElement:
		return r.end(raw)
	case xml.CharData:
		if len(r.stack) == 0 && len(bytes.TrimSpace(t)) != 0 {
			return ErrNotRepository
		}
	}

	if r.skip > 0 {
		return nil
	}

	return r.emit(raw)
}

func (r *rewriter) start(t xml.StartElement, raw []byte) error {
	switch {
	case len(r.stack) >= maxDepth:
		return ErrTooDeep
	case len(r.stack) == 0 && (r.rooted || t.Name.Local != "repository"):
		return ErrNotRepository
	}

	r.rooted = true
	r.stack = append(r.stack, t.Name.Local)

	if r.skip > 0 {
		return nil
	}

	depth := len(r.stack)

	switch {
	case depth == 2 && t.Name.Local == "properties":
		r.props = &properties{start: bytes.Clone(raw)}

		return nil
	case depth == 3 && r.stack[1] == "properties" && t.Name.Local == "property" && r.props != nil:
		if strippedProperties[attr(t, "name")] {
			r.skip = depth
			r.props.dropped++

			return nil
		}

		r.props.kept++
	case depth == 3 && r.stack[1] == "children" && t.Name.Local == "child" && r.opts.Relocate != nil:
		return r.emit(r.relocated(t, raw))
	}

	return r.emit(raw)
}

func (r *rewriter) end(raw []byte) error {
	closing := len(r.stack)
	if closing == 0 {
		return ErrNotRepository
	}

	r.stack = r.stack[:closing-1]

	switch {
	case r.skip > 0:
		if closing == r.skip {
			r.skip = 0
		}

		return nil
	case closing == 2 && r.props != nil:
		return r.flushProps(raw)
	}

	return r.emit(raw)
}

// emit writes raw to the output, or to the held-back properties element.
func (r *rewriter) emit(raw []byte) error {
	if r.props != nil {
		if r.props.body.Len()+len(raw) > maxPropsBytes {
			return ErrTokenTooLarge
		}

		r.props.body.Write(raw)

		return nil
	}

	_, err := r.out.Write(raw)

	return err //nolint:wrapcheck // the destination's own error
}

var sizeAttr = regexp.MustCompile(`(\ssize\s*=\s*['"])\d+(['"])`)

// flushProps writes the held-back properties element, its size attribute
// matching what is left of it.
func (r *rewriter) flushProps(end []byte) error {
	p := r.props
	r.props = nil

	start := p.start
	if p.dropped > 0 {
		start = sizeAttr.ReplaceAll(start, []byte("${1}"+strconv.Itoa(p.kept)+"${2}"))
	}

	for _, b := range [][]byte{start, p.body.Bytes(), end} {
		if _, err := r.out.Write(b); err != nil {
			return err //nolint:wrapcheck // the destination's own error
		}
	}

	return nil
}

// relocated is the start tag of a composite child with its location mapped, or
// raw when the location stays as it is.
func (r *rewriter) relocated(t xml.StartElement, raw []byte) []byte {
	var tag bytes.Buffer

	changed := false

	tag.WriteByte('<')
	tag.WriteString(qname(t.Name))

	for _, a := range t.Attr {
		v := a.Value

		if a.Name.Space == "" && a.Name.Local == "location" {
			if loc, ok := r.opts.Relocate(v); ok {
				v, changed = loc, true
			}
		}

		tag.WriteByte(' ')
		tag.WriteString(qname(a.Name))
		tag.WriteString("='")
		_ = xml.EscapeText(&tag, []byte(v)) // a bytes.Buffer does not fail

		tag.WriteByte('\'')
	}

	if !changed {
		return raw
	}

	if bytes.HasSuffix(raw, []byte("/>")) {
		tag.WriteString("/>")
	} else {
		tag.WriteByte('>')
	}

	return tag.Bytes()
}

// qname spells a name as the document wrote it: RawToken leaves the prefix in
// Space.
func qname(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}

	return n.Space + ":" + n.Local
}

// attr is the value of the unprefixed attribute name of t.
func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value
		}
	}

	return ""
}
