// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

// MaxEntryBytes bounds one repodata entry (and the info object): real entries
// are a few KiB, the largest depend lists tens of KiB. A hostile document must
// not be able to make the reader buffer a gigabyte-long string.
const MaxEntryBytes = 4 << 20

// maxZstdWindow bounds the zstd window a repodata.json.zst may demand.
const maxZstdWindow = 128 << 20

var (
	// ErrTooLarge is a document that decompresses to more than its limit.
	ErrTooLarge = errors.New("conda: repodata exceeds its size limit")
	// ErrMalformed is a document that is not a repodata.json.
	ErrMalformed = errors.New("conda: malformed repodata")
	// ErrBaseURL is a document whose info.base_url points package downloads
	// somewhere other than the channel: served through a proxy it would let
	// clients fetch around it.
	ErrBaseURL = errors.New("conda: repodata sets info.base_url")
)

// Section is one of the two package maps of a repodata.json.
type Section int

const (
	// SectionTarBz2 is "packages": the .tar.bz2 files.
	SectionTarBz2 Section = iota
	// SectionConda is "packages.conda": the .conda files.
	SectionConda
)

// Key is the repodata.json key of the section.
func (s Section) Key() string {
	if s == SectionConda {
		return "packages.conda"
	}

	return "packages"
}

// Ext is the file extension of the packages listed in the section.
func (s Section) Ext() string {
	if s == SectionConda {
		return ExtConda
	}

	return ExtTarBz2
}

// Visitor receives the parts of a repodata.json as Scan streams over it. Any
// field may be nil. The slices passed in are only valid during the call. A
// returned error stops the scan and is returned as is.
type Visitor struct {
	// Info receives the "info" object.
	Info func(info json.RawMessage) error
	// Entry receives one package entry of a section.
	Entry func(section Section, filename string, entry json.RawMessage) error
}

// Scan streams the repodata.json in r, calling v for the info object and every
// package entry, and holding one entry in memory at a time. r must be plain
// JSON (see Open). It returns ErrMalformed for anything that is not a JSON
// object whose "packages" and "packages.conda" members, when present, are
// objects of objects, and ErrTooLarge when r yields more than limit bytes.
func Scan(r io.Reader, limit int64, v Visitor) error {
	br := &boundReader{r: &capReader{r: r, left: limit}, max: MaxEntryBytes}
	dec := json.NewDecoder(br)
	br.dec = dec

	err := scanTop(dec, v)
	if err != nil {
		return classify(err)
	}

	// Nothing but whitespace may follow the document.
	if _, terr := dec.Token(); !errors.Is(terr, io.EOF) {
		if terr == nil {
			return fmt.Errorf("%w: data after the document", ErrMalformed)
		}

		return classify(terr)
	}

	return nil
}

// classify turns decoder errors into this package's sentinels, leaving the
// caller's own (visitor) errors and the size errors alone.
func classify(err error) error {
	var (
		syn *json.SyntaxError
		typ *json.UnmarshalTypeError
	)

	switch {
	case errors.Is(err, ErrTooLarge), errors.Is(err, ErrMalformed):
		return err
	case errors.As(err, &syn), errors.As(err, &typ), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return fmt.Errorf("%w: %w", ErrMalformed, err)
	}

	return err
}

func scanTop(dec *json.Decoder, v Visitor) error {
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}

	var raw json.RawMessage

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err //nolint:wrapcheck // classified by Scan
		}

		key, _ := keyTok.(string)

		switch key {
		case "info":
			if err := dec.Decode(&raw); err != nil {
				return err //nolint:wrapcheck // classified by Scan
			}

			if v.Info != nil {
				if err := v.Info(raw); err != nil {
					return err
				}
			}
		case SectionTarBz2.Key():
			err = scanSection(dec, SectionTarBz2, v, &raw)
		case SectionConda.Key():
			err = scanSection(dec, SectionConda, v, &raw)
		default:
			// removed, repodata_version, signatures, ...: skipped value by value.
			err = dec.Decode(&raw)
		}

		if err != nil {
			return err
		}
	}

	return expectDelim(dec, '}')
}

// scanSection streams the entries of one package map. A null map is empty.
func scanSection(dec *json.Decoder, s Section, v Visitor, raw *json.RawMessage) error {
	tok, err := dec.Token()
	if err != nil {
		return err //nolint:wrapcheck // classified by Scan
	}

	if tok == nil {
		return nil
	}

	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("%w: %q is not an object", ErrMalformed, s.Key())
	}

	for dec.More() {
		keyTok, kerr := dec.Token()
		if kerr != nil {
			return kerr //nolint:wrapcheck // classified by Scan
		}

		filename, _ := keyTok.(string)

		if derr := dec.Decode(raw); derr != nil {
			return derr //nolint:wrapcheck // classified by Scan
		}

		if len(*raw) == 0 || (*raw)[0] != '{' {
			return fmt.Errorf("%w: entry %q is not an object", ErrMalformed, filename)
		}

		if len(*raw) > MaxEntryBytes {
			return fmt.Errorf("%w: entry %q exceeds %d bytes", ErrMalformed, filename, MaxEntryBytes)
		}

		if v.Entry != nil {
			if verr := v.Entry(s, filename, *raw); verr != nil {
				return verr
			}
		}
	}

	return expectDelim(dec, '}')
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err //nolint:wrapcheck // classified by Scan
	}

	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("%w: expected %q", ErrMalformed, string(want))
	}

	return nil
}

// capReader fails with ErrTooLarge once more than left bytes were read.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, ErrTooLarge
	}

	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}

	n, err := c.r.Read(p)

	c.left -= int64(n)
	if c.left < 0 {
		return n - 1, ErrTooLarge
	}

	return n, err //nolint:wrapcheck // plain passthrough
}

// boundReader fails with ErrMalformed when the decoder has buffered more than
// max bytes it has not yet consumed: a single value (or token) that long.
type boundReader struct {
	r   io.Reader
	dec *json.Decoder
	n   int64
	max int64
}

func (b *boundReader) Read(p []byte) (int, error) {
	if b.n-b.dec.InputOffset() > b.max {
		return 0, fmt.Errorf("%w: a value exceeds %d bytes", ErrMalformed, b.max)
	}

	n, err := b.r.Read(p)
	b.n += int64(n)

	return n, err //nolint:wrapcheck // plain passthrough
}

// Open returns the decoded bytes of a repodata document in r: plain JSON, or
// a zstd or bzip2 stream, told apart by their magic numbers. The decoded
// output is limited to limit bytes (ErrTooLarge). Closing the result releases
// the decoder; it does not close r.
func Open(r io.Reader, limit int64) (io.ReadCloser, Compression, error) {
	br := bufio.NewReaderSize(r, 64<<10)

	head, err := br.Peek(4)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, CompNone, err //nolint:wrapcheck // plain read error
	}

	switch {
	case bytes.HasPrefix(head, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		dec, derr := zstd.NewReader(br, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(maxZstdWindow))
		if derr != nil {
			return nil, CompZstd, fmt.Errorf("%w: %w", ErrMalformed, derr)
		}

		return capped(dec.IOReadCloser(), limit), CompZstd, nil
	case bytes.HasPrefix(head, []byte("BZh")):
		return capped(io.NopCloser(bzip2.NewReader(br)), limit), CompBzip2, nil
	}

	return capped(io.NopCloser(br), limit), CompNone, nil
}

type cappedReadCloser struct {
	*capReader
	io.Closer
}

func capped(rc io.ReadCloser, limit int64) io.ReadCloser {
	return cappedReadCloser{capReader: &capReader{r: rc, left: limit}, Closer: rc}
}

// ValidateIndex checks that r, the body of a repodata file of the given
// encoding, is a usable one: plain and zstd documents are scanned in full
// (structure, and no info.base_url); a bzip2 stream, which no modern client
// asks for and which decodes slowly, is only checked for its magic number.
// limit bounds the decoded size.
func ValidateIndex(r io.Reader, comp Compression, limit int64) error {
	rc, got, err := Open(r, limit)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()

	if got != comp {
		return fmt.Errorf("%w: not encoded as its name says", ErrMalformed)
	}

	if comp == CompBzip2 {
		return nil
	}

	known := false

	err = Scan(rc, limit, Visitor{
		Info: func(info json.RawMessage) error {
			known = true
			return checkInfo(info)
		},
		Entry: func(Section, string, json.RawMessage) error {
			known = true
			return nil
		},
	})
	if err != nil {
		return err
	}

	if !known {
		return fmt.Errorf("%w: no info or packages", ErrMalformed)
	}

	return nil
}

// checkInfo refuses an info object that redirects package downloads.
func checkInfo(info json.RawMessage) error {
	var fields struct {
		BaseURL json.RawMessage `json:"base_url"`
	}

	if err := json.Unmarshal(info, &fields); err != nil {
		return fmt.Errorf("%w: info: %w", ErrMalformed, err)
	}

	if s := bytes.TrimSpace(fields.BaseURL); len(s) > 0 && !bytes.Equal(s, []byte("null")) && !bytes.Equal(s, []byte(`""`)) {
		return ErrBaseURL
	}

	return nil
}

// ValidateJSONObject checks that r holds exactly one JSON object, without
// keeping any of it in memory; limit bounds the bytes read.
func ValidateJSONObject(r io.Reader, limit int64) error {
	dec := json.NewDecoder(&capReader{r: r, left: limit})

	if err := expectDelim(dec, '{'); err != nil {
		return classify(err)
	}

	for depth := 1; depth > 0; {
		tok, err := dec.Token()
		if err != nil {
			return classify(err)
		}

		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: data after the document", ErrMalformed)
		}

		return classify(err)
	}

	return nil
}
