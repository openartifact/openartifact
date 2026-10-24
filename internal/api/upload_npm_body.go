// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/iocopy"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

const npmBodyReadBufferLen = 64 << 10

var (
	errNPMBodySyntax    = errors.New("invalid npm publish body")
	errNPMBadAttachment = errors.New("invalid npm attachment")
)

// npmAttachment is one decoded _attachments entry. The tarball is spooled to
// a temp file as it is decoded, so a publish never holds the base64 text or
// the decoded bytes in memory.
type npmAttachment struct {
	path string
	size int64

	contentType    string
	hasContentType bool
	length         int64
	hasLength      bool
	hasData        bool
	dataErr        error
}

// parseNPMPublishBody reads an npm publish packument from r in one pass. The
// small fields (name, versions, dist-tags) are decoded normally; each
// attachment's base64 "data" string is decoded straight into a temp file. The
// returned cleanup removes every spooled file and must always be called.
func parseNPMPublishBody(r io.Reader) (*npmPublishBody, func(), error) {
	pub := &npmPublishBody{Attachments: map[string]*npmAttachment{}}

	cleanup := func() {
		for _, a := range pub.Attachments {
			if a.path != "" {
				_ = os.Remove(a.path)
			}
		}
	}

	if err := readNPMPublishBody(bufio.NewReaderSize(r, npmBodyReadBufferLen), pub); err != nil {
		cleanup()

		return nil, func() {}, err
	}

	return pub, cleanup, nil
}

func readNPMPublishBody(br *bufio.Reader, pub *npmPublishBody) error {
	if err := expectByte(br, '{'); err != nil {
		return err
	}

	return forEachMember(br, func(key string) error {
		if key == "_attachments" {
			return readNPMAttachments(br, pub)
		}

		raw, err := readRawJSONValue(br)
		if err != nil {
			return err
		}

		switch key {
		case "name":
			return unmarshalNPMField(raw, &pub.Name)
		case "versions":
			return unmarshalNPMField(raw, &pub.Versions)
		case "dist-tags":
			return unmarshalNPMField(raw, &pub.DistTags)
		}

		return nil
	})
}

func unmarshalNPMField(raw []byte, dst any) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("%w: %w", errNPMBodySyntax, err)
	}

	return nil
}

func readNPMAttachments(br *bufio.Reader, pub *npmPublishBody) error {
	b, err := peekNonSpace(br)
	if err != nil {
		return err
	}

	if b == 'n' { // null
		_, err := readRawJSONValue(br)

		return err
	}

	if err := expectByte(br, '{'); err != nil {
		return err
	}

	return forEachMember(br, func(name string) error {
		att, err := readNPMAttachment(br)
		if att != nil {
			pub.Attachments[name] = att
		}

		return err
	})
}

func readNPMAttachment(br *bufio.Reader) (*npmAttachment, error) {
	att := &npmAttachment{}

	if err := expectByte(br, '{'); err != nil {
		return nil, err
	}

	err := forEachMember(br, func(key string) error {
		if key == "data" {
			return att.readData(br)
		}

		raw, err := readRawJSONValue(br)
		if err != nil {
			return err
		}

		switch key {
		case "content_type":
			att.hasContentType = json.Unmarshal(raw, &att.contentType) == nil
		case "length":
			var n json.Number

			if json.Unmarshal(raw, &n) == nil {
				if v, err := n.Int64(); err == nil {
					att.length, att.hasLength = v, true
				}
			}
		}

		return nil
	})

	return att, err
}

// readData decodes the base64 string value at the reader into a temp file.
func (a *npmAttachment) readData(br *bufio.Reader) error {
	b, err := peekNonSpace(br)
	if err != nil {
		return err
	}

	if b != '"' { // not a string: record the failure, skip the value
		a.dataErr = fmt.Errorf("%w: data missing or not a string", pkgnpm.ErrInvalidAttachment)
		_, err := readRawJSONValue(br)

		return err
	}

	if _, err := br.ReadByte(); err != nil {
		return err
	}

	f, err := os.CreateTemp("", "npm-publish-*.tgz")
	if err != nil {
		return fmt.Errorf("spool npm attachment: %w", err)
	}

	a.path = f.Name()
	a.hasData = true

	src := &jsonStringReader{br: br}

	n, copyErr := iocopy.Copy(f, base64.NewDecoder(base64.StdEncoding, src))
	if closeErr := f.Close(); copyErr == nil {
		copyErr = closeErr
	}

	a.size = n

	switch {
	case src.err != nil: // body read or JSON string error: the whole body is unusable
		return src.err
	case copyErr != nil:
		a.dataErr = fmt.Errorf("%w: base64 decode: %w", pkgnpm.ErrInvalidAttachment, copyErr)
		// Drain the rest of the string so parsing can continue.
		_, _ = iocopy.Copy(io.Discard, src)

		return src.err
	}

	return nil
}

// validate reports the first problem with the attachment.
func (a *npmAttachment) validate() error {
	if err := pkgnpm.CheckAttachmentContentType(a.contentType, a.hasContentType); err != nil {
		return err
	}

	if !a.hasData {
		return fmt.Errorf("%w: data missing or not a string", pkgnpm.ErrInvalidAttachment)
	}

	if a.dataErr != nil {
		return a.dataErr
	}

	return pkgnpm.CheckAttachmentLength(a.length, a.hasLength, a.size)
}

// jsonStringReader yields the decoded characters of a JSON string whose
// opening quote was already consumed, ending at the closing quote.
type jsonStringReader struct {
	br   *bufio.Reader
	done bool
	err  error // set only for failures other than reaching the closing quote
}

func (s *jsonStringReader) Read(p []byte) (int, error) {
	if s.done {
		return 0, io.EOF
	}

	n := 0

	for n < len(p) {
		if s.br.Buffered() == 0 {
			if _, err := s.br.Peek(1); err != nil {
				s.err, s.done = unexpectedEOF(err), true

				return n, s.err
			}
		}

		// Copy the plain run up to the next quote or backslash in one go.
		chunk, _ := s.br.Peek(min(s.br.Buffered(), len(p)-n))

		run := len(chunk)
		if i := bytes.IndexByte(chunk, '"'); i >= 0 {
			run = i
		}

		if i := bytes.IndexByte(chunk[:run], '\\'); i >= 0 {
			run = i
		}

		n += copy(p[n:], chunk[:run])
		_, _ = s.br.Discard(run)

		if run == len(chunk) {
			continue
		}

		c, _ := s.br.ReadByte()
		if c == '"' {
			s.done = true

			return n, io.EOF
		}

		r, err := s.readEscape()
		if err != nil {
			s.err, s.done = err, true

			return n, err
		}

		if r >= utf8.RuneSelf {
			s.err, s.done = fmt.Errorf("%w: non-ASCII escape in base64 data", errNPMBadAttachment), true

			return n, s.err
		}

		p[n] = byte(r & 0x7f) // r < RuneSelf checked above
		n++
	}

	return n, nil
}

func (s *jsonStringReader) readEscape() (rune, error) {
	c, err := s.br.ReadByte()
	if err != nil {
		return 0, unexpectedEOF(err)
	}

	switch c {
	case '/', '\\', '"':
		return rune(c), nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		var hex [4]byte
		if _, err := io.ReadFull(s.br, hex[:]); err != nil {
			return 0, unexpectedEOF(err)
		}

		v, err := strconv.ParseUint(string(hex[:]), 16, 16)
		if err != nil {
			return 0, fmt.Errorf("%w: bad unicode escape", errNPMBodySyntax)
		}

		return rune(v), nil
	}

	return 0, fmt.Errorf("%w: bad escape", errNPMBodySyntax)
}

func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}

	return err
}

// forEachMember iterates the members of an object whose '{' was consumed,
// calling fn with each key positioned at its value, and consumes the closing '}'.
func forEachMember(br *bufio.Reader, fn func(key string) error) error {
	b, err := peekNonSpace(br)
	if err != nil {
		return err
	}

	if b == '}' {
		_, err := br.ReadByte()

		return err
	}

	for {
		raw, err := readRawJSONValue(br)
		if err != nil {
			return err
		}

		var key string
		if err := json.Unmarshal(raw, &key); err != nil {
			return fmt.Errorf("%w: object key: %w", errNPMBodySyntax, err)
		}

		if err := expectByte(br, ':'); err != nil {
			return err
		}

		if err := fn(key); err != nil {
			return err
		}

		b, err := peekNonSpace(br)
		if err != nil {
			return err
		}

		_, _ = br.ReadByte()

		switch b {
		case ',':
		case '}':
			return nil
		default:
			return fmt.Errorf("%w: unexpected %q", errNPMBodySyntax, b)
		}
	}
}

func peekNonSpace(br *bufio.Reader) (byte, error) {
	for {
		c, err := br.ReadByte()
		if err != nil {
			return 0, unexpectedEOF(err)
		}

		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		}

		if err := br.UnreadByte(); err != nil {
			return 0, err
		}

		return c, nil
	}
}

func expectByte(br *bufio.Reader, want byte) error {
	got, err := peekNonSpace(br)
	if err != nil {
		return err
	}

	if got != want {
		return fmt.Errorf("%w: expected %q, got %q", errNPMBodySyntax, want, got)
	}

	_, err = br.ReadByte()

	return err
}

// readRawJSONValue copies one complete JSON value (leading whitespace skipped)
// out of br without interpreting it. Structural validity is left to
// json.Unmarshal of the result.
func readRawJSONValue(br *bufio.Reader) ([]byte, error) {
	first, err := peekNonSpace(br)
	if err != nil {
		return nil, err
	}

	var (
		out      bytes.Buffer
		depth    int
		inString bool
		escaped  bool
	)

	scalar := first != '{' && first != '[' && first != '"'

	for {
		c, err := br.ReadByte()
		if err != nil {
			if scalar && errors.Is(err, io.EOF) && out.Len() > 0 {
				return out.Bytes(), nil
			}

			return nil, unexpectedEOF(err)
		}

		if scalar {
			switch c {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				_ = br.UnreadByte()

				return out.Bytes(), nil
			}

			out.WriteByte(c)

			continue
		}

		out.WriteByte(c)

		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false

				if depth == 0 {
					return out.Bytes(), nil
				}
			}

			continue
		}

		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--

			if depth == 0 {
				return out.Bytes(), nil
			}
		}
	}
}

// integrity hashes the spooled tarball: npm integrity string, sha1 and sha256.
func (a *npmAttachment) integrity() (string, string, string, error) {
	f, err := os.Open(a.path)
	if err != nil {
		return "", "", "", fmt.Errorf("open spooled tarball: %w", err)
	}

	defer func() { _ = f.Close() }()

	integrity, sha1Hex, sha256Hex, _, err := pkgnpm.ComputeIntegrity(f)

	return integrity, sha1Hex, sha256Hex, err
}
