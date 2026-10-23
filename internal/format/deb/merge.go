// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package deb

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ulikunitz/xz"
)

// Stanza is one package entry of a Packages file read from an upstream index:
// the identifying fields a merge keys on plus the verbatim text.
type Stanza struct {
	Package      string
	Version      string
	Architecture string
	// Filename is the pool-relative path the entry downloads from.
	Filename string
	SHA256   string
	// Text is the stanza as read, normalised to end with a blank line.
	Text string
}

// ScanPackages calls fn for every stanza of a Packages file in order. Field
// names are matched case-insensitively (apt does). A stanza without a Package
// field is skipped; fn returning an error stops the scan with it.
func ScanPackages(r io.Reader, fn func(Stanza) error) error {
	br := bufio.NewReaderSize(r, 64<<10)

	var (
		cur  Stanza
		buf  strings.Builder
		seen bool
	)

	flush := func() error {
		defer func() {
			cur, seen = Stanza{}, false

			buf.Reset()
		}()

		if !seen || cur.Package == "" {
			return nil
		}

		cur.Text = buf.String() + "\n"

		return fn(cur)
	}

	for {
		line, err := br.ReadString('\n')
		if line != "" {
			trimmed := strings.TrimRight(line, "\r\n")

			if strings.TrimSpace(trimmed) == "" {
				if ferr := flush(); ferr != nil {
					return ferr
				}
			} else {
				seen = true

				buf.WriteString(trimmed)
				buf.WriteByte('\n')

				cur.setField(trimmed)
			}
		}

		if errors.Is(err, io.EOF) {
			return flush()
		}

		if err != nil {
			return fmt.Errorf("deb: reading Packages: %w", err)
		}
	}
}

// setField records line when it is one of the fields a stanza is keyed on.
func (s *Stanza) setField(line string) {
	if line[0] == ' ' || line[0] == '\t' {
		return
	}

	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}

	value = strings.TrimSpace(value)

	switch {
	case strings.EqualFold(name, "Package"):
		s.Package = value
	case strings.EqualFold(name, "Version"):
		s.Version = value
	case strings.EqualFold(name, "Architecture"):
		s.Architecture = value
	case strings.EqualFold(name, "Filename"):
		s.Filename = value
	case strings.EqualFold(name, "SHA256"):
		s.SHA256 = value
	}
}

// UpstreamRelease is what a merge needs from an upstream Release file.
type UpstreamRelease struct {
	Components    []string
	Architectures []string
	// Files holds the index paths the Release lists (relative to dists/<dist>/),
	// i.e. which Packages variants exist upstream.
	Files map[string]struct{}
}

// ParseRelease reads an upstream Release or InRelease document. A cleartext
// signature is stripped, not verified: the group re-signs what it publishes.
func ParseRelease(data []byte) UpstreamRelease {
	rel := UpstreamRelease{Files: make(map[string]struct{})}
	section := ""

	for line := range strings.SplitSeq(string(stripClearsign(data)), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}

		if line[0] == ' ' || line[0] == '\t' {
			if fields := strings.Fields(line); strings.HasSuffix(section, "sum") || strings.HasPrefix(section, "sha") {
				if len(fields) == 3 {
					rel.Files[fields[2]] = struct{}{}
				}
			}

			continue
		}

		name, value, _ := strings.Cut(line, ":")
		section = strings.ToLower(name)

		switch section {
		case "components":
			rel.Components = strings.Fields(value)
		case "architectures":
			rel.Architectures = strings.Fields(value)
		}
	}

	return rel
}

// stripClearsign returns the signed text of a cleartext-signed document, or
// data unchanged when it is not one.
func stripClearsign(data []byte) []byte {
	const (
		begin = "-----BEGIN PGP SIGNED MESSAGE-----"
		sig   = "-----BEGIN PGP SIGNATURE-----"
	)

	if !bytes.HasPrefix(bytes.TrimLeft(data, "\r\n "), []byte(begin)) {
		return data
	}

	var out []string

	inBody, headers := false, true

	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimRight(line, "\r")

		switch {
		case strings.HasPrefix(line, sig):
			return []byte(strings.Join(out, "\n"))
		case !inBody && strings.HasPrefix(line, begin):
			inBody = true
		case inBody && headers:
			// Armor headers (Hash: ...) end at the first empty line.
			if line == "" {
				headers = false
			}
		case inBody:
			out = append(out, strings.TrimPrefix(line, "- "))
		}
	}

	return []byte(strings.Join(out, "\n"))
}

// PackagesVariants lists the paths a Packages index of binary-<arch> of
// component comp may be fetched from upstream, best compression first,
// restricted to those the Release lists (all three when it lists none of them,
// e.g. a Release that only carries by-hash entries).
func (r UpstreamRelease) PackagesVariants(comp, arch string) []string {
	base := comp + "/binary-" + arch + "/Packages"
	all := []string{base + ".xz", base + ".gz", base}

	var listed []string

	for _, p := range all {
		if _, ok := r.Files[p]; ok {
			listed = append(listed, p)
		}
	}

	if len(listed) == 0 {
		return all
	}

	return listed
}

// HasIndex reports whether the Release declares binary-<arch> of comp.
func (r UpstreamRelease) HasIndex(comp, arch string) bool {
	return slices.Contains(r.Components, comp) && slices.Contains(r.Architectures, arch)
}

// DecompressIndex wraps r according to the extension of the Packages path it
// was fetched from (.xz, .gz or none). Closing the result closes r.
func DecompressIndex(r io.ReadCloser, path string) (io.ReadCloser, error) {
	switch {
	case strings.HasSuffix(path, ".xz"):
		xr, err := xz.NewReader(r)
		if err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("deb: xz index: %w", err)
		}

		return readCloser{Reader: xr, closer: r}, nil
	case strings.HasSuffix(path, ".gz"):
		gr, err := gzip.NewReader(r)
		if err != nil {
			_ = r.Close()
			return nil, fmt.Errorf("deb: gzip index: %w", err)
		}

		return readCloser{Reader: gr, closer: multiCloser{gr, r}}, nil
	}

	return r, nil
}

type readCloser struct {
	io.Reader

	closer io.Closer
}

func (c readCloser) Close() error { return c.closer.Close() }

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var first error

	for _, c := range m {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}

	return first
}
