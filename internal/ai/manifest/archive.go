// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package manifest

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// slot is one member to pick out of an archive.
type slot struct {
	label string
	match func(name string) bool
	// decode turns the member into text; nil reads it as plain text.
	decode func(io.Reader) (string, error)
}

func (s slot) read(r io.Reader) (string, error) {
	if s.decode != nil {
		return s.decode(r)
	}

	return readText(r)
}

// compression selects the stream decoder in front of a tar.
type compression int

const (
	compNone compression = iota
	compGzip
	compBzip2
	compZstd
)

// decompress wraps r with the decoder for c. The returned closer must be
// called when the stream is done.
func decompress(r io.Reader, c compression) (io.Reader, func(), error) {
	switch c {
	case compGzip:
		gr, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("gzip open: %w", err)
		}

		return gr, func() { _ = gr.Close() }, nil
	case compBzip2:
		return bzip2.NewReader(r), func() {}, nil
	case compZstd:
		zr, err := zstd.NewReader(r, zstd.WithDecoderMaxMemory(256<<20), zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, nil, fmt.Errorf("zstd open: %w", err)
		}

		return zr, zr.Close, nil
	default:
		return r, func() {}, nil
	}
}

// collectTar walks a tar stream until every slot has a member or the stream
// ends. Members beyond the first match of a slot are ignored. A damaged or
// over-limit tail is tolerated when something was found before it.
func collectTar(ctx context.Context, r io.Reader, slots []slot) ([]section, error) {
	tr := tar.NewReader(ctxReader{ctx: ctx, r: &capReader{r: r, n: maxScanBytes}})

	found := make([]*string, len(slots))
	remaining := len(slots)

	for remaining > 0 {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) || anyFound(found) {
				break
			}

			return nil, fmt.Errorf("read tar: %w", err)
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}

		for i, s := range slots {
			if found[i] != nil || !s.match(hdr.Name) {
				continue
			}

			text, err := s.read(tr)
			if err != nil {
				return nil, err
			}

			found[i] = &text
			remaining--

			break
		}
	}

	return sectionsOf(slots, found), nil
}

// collectZip picks members out of a zip with random access.
func collectZip(ra *readerAt, slots []slot) ([]section, error) {
	zr, err := zip.NewReader(ra, ra.size)
	if err != nil {
		return nil, fmt.Errorf("read zip: %w", err)
	}

	found := make([]*string, len(slots))

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}

		for i, s := range slots {
			if found[i] != nil || !s.match(f.Name) {
				continue
			}

			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open zip member: %w", err)
			}

			text, err := s.read(rc)
			_ = rc.Close()

			if err != nil {
				return nil, err
			}

			found[i] = &text

			break
		}

		if allFound(found) {
			break
		}
	}

	return sectionsOf(slots, found), nil
}

func anyFound(found []*string) bool {
	for _, f := range found {
		if f != nil {
			return true
		}
	}

	return false
}

func allFound(found []*string) bool {
	for _, f := range found {
		if f == nil {
			return false
		}
	}

	return true
}

func sectionsOf(slots []slot, found []*string) []section {
	out := make([]section, 0, len(slots))

	for i, f := range found {
		if f != nil {
			out = append(out, section{label: slots[i].label, body: *f})
		}
	}

	return out
}

// tarManifest opens the blob as a (compressed) tar and collects the slots.
func tarManifest(ctx context.Context, src Source, c compression, slots []slot) (string, error) {
	rc, err := src.Open()
	if err != nil {
		return "", fmt.Errorf("open artifact: %w", err)
	}
	defer func() { _ = rc.Close() }()

	stream, closeDec, err := decompress(ctxReader{ctx: ctx, r: rc}, c)
	if err != nil {
		return "", err
	}
	defer closeDec()

	parts, err := collectTar(ctx, stream, slots)
	if err != nil {
		return "", err
	}

	return assemble(parts), nil
}

// zipManifest opens the blob as a zip and collects the slots.
func zipManifest(ctx context.Context, src Source, slots []slot) (string, error) {
	ra, err := openAt(ctx, src.Open)
	if err != nil {
		return "", err
	}
	defer ra.release()

	parts, err := collectZip(ra, slots)
	if err != nil {
		return "", err
	}

	return assemble(parts), nil
}

// assemble renders sections; a lone section keeps its label so the model
// knows which file it is reading.
func assemble(parts []section) string {
	return join(parts...)
}

// hasSuffixFold is a case-insensitive HasSuffix.
func hasSuffixFold(s, suffix string) bool {
	return strings.HasSuffix(strings.ToLower(s), suffix)
}
