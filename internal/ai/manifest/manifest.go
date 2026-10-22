// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package manifest extracts the plain-text package manifest (package.json,
// METADATA, a .pom, a deb control file, ...) from a stored artifact so it can
// be sent to the AI analysis model.
//
// Artifact files are untrusted: every read is bounded (per member, per
// decompressed stream, and per spooled download), so a hostile archive can
// neither exhaust memory nor keep a worker busy indefinitely.
package manifest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	// MaxBytes bounds the manifest text returned by Extract (it matches the
	// 64 KiB the AI client forwards to the model).
	MaxBytes = 64 * 1024

	// maxMemberBytes bounds what is read from one archive member; the result
	// is trimmed to MaxBytes afterwards.
	maxMemberBytes = MaxBytes

	// maxSpoolBytes bounds a blob copied to a temporary file for formats
	// that need random access (zip central directory, ar members).
	maxSpoolBytes int64 = 1 << 30

	// pyBodyBytes keeps the head of a long METADATA/PKG-INFO description.
	pyBodyBytes = 2048
)

// maxScanBytes bounds the decompressed bytes walked while looking for a member
// in a compressed tar stream (zip-bomb guard). A variable so tests can lower it.
var maxScanBytes int64 = 512 << 20 //nolint:gochecknoglobals // tests lower it

var (
	// ErrUnsupported means no manifest can be extracted for this format/file.
	ErrUnsupported = errors.New("manifest extraction is not supported for this artifact")
	// ErrNotFound means the artifact was read but holds no manifest file.
	ErrNotFound = errors.New("no manifest found in artifact")
	// ErrTooLarge means the artifact exceeds the extraction limits.
	ErrTooLarge = errors.New("artifact is too large for manifest extraction")
)

// Source describes the artifact to read.
type Source struct {
	// Format is the repository format (npm, pypi, maven, ...).
	Format string
	// Filename is the artifact's file name or repository path; its extension
	// selects the container type.
	Filename string
	// Open returns a fresh reader over the stored blob.
	Open func() (io.ReadCloser, error)
	// Sibling opens another file of the same logical artifact (conan recipes
	// keep conanfile.py next to the conanmanifest.txt the listing points at).
	// It returns (nil, nil) when no such file exists. May be nil.
	Sibling func(name string) (io.ReadCloser, error)
}

// extractor reads the manifest of one artifact kind.
type extractor func(ctx context.Context, src Source) (string, error)

// Supported reports whether a manifest can be extracted for the file.
func Supported(format, filename string) bool {
	return pick(format, filename) != nil
}

// AutoEligible reports whether the file is the artifact that represents a
// package for unattended analysis. Formats that store several files per
// package (maven/ivy/sbt: pom, jar, sources, ...) are limited to the
// descriptor so one upload yields one analysis.
func AutoEligible(format, filename string) bool {
	switch format {
	case "maven", "ivy", "sbt":
		base := path.Base(filename)

		return strings.HasSuffix(base, ".pom") || base == "ivy.xml"
	}

	return Supported(format, filename)
}

// Extract returns the manifest text of the artifact (at most MaxBytes).
func Extract(ctx context.Context, src Source) (string, error) {
	ex := pick(src.Format, src.Filename)
	if ex == nil {
		return "", ErrUnsupported
	}

	text, err := ex(ctx, src)
	if err != nil {
		return "", err
	}

	text = clean(text)
	if strings.TrimSpace(text) == "" {
		return "", ErrNotFound
	}

	return text, nil
}

// clean makes the text valid UTF-8 without NULs and trims it to MaxBytes.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	s = strings.ReplaceAll(s, "\x00", "")

	if len(s) > MaxBytes {
		s = s[:MaxBytes]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}

	return s
}

// section is one named piece of the assembled manifest text.
type section struct {
	label string
	body  string
}

// join renders sections; a single unlabelled section is returned bare.
func join(parts ...section) string {
	var b strings.Builder

	for _, p := range parts {
		if strings.TrimSpace(p.body) == "" {
			continue
		}

		if p.label != "" {
			fmt.Fprintf(&b, "### %s\n", p.label)
		}

		b.WriteString(p.body)

		if !strings.HasSuffix(p.body, "\n") {
			b.WriteByte('\n')
		}

		b.WriteByte('\n')
	}

	return b.String()
}

// ctxReader aborts a read once ctx is done.
type ctxReader struct {
	ctx context.Context //nolint:containedctx // wraps one bounded read
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, fmt.Errorf("manifest extraction aborted: %w", err)
	}

	return c.r.Read(p)
}

// capReader fails with ErrTooLarge after n bytes instead of silently
// truncating, so a decompression bomb is reported rather than half-read.
type capReader struct {
	r io.Reader
	n int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n <= 0 {
		return 0, ErrTooLarge
	}

	if int64(len(p)) > c.n {
		p = p[:c.n]
	}

	n, err := c.r.Read(p)
	c.n -= int64(n)

	return n, err
}

// readText reads up to MaxBytes of r (extra input is ignored).
func readText(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxMemberBytes))
	if err != nil {
		return "", fmt.Errorf("read manifest: %w", err)
	}

	return string(data), nil
}

// readerAt gives random access to an opened blob: the blob itself when the
// store hands out a file, otherwise a temporary copy removed by release.
type readerAt struct {
	io.ReaderAt

	size    int64
	release func()
}

// openAt opens the blob for random access.
func openAt(ctx context.Context, open func() (io.ReadCloser, error)) (*readerAt, error) {
	rc, err := open()
	if err != nil {
		return nil, fmt.Errorf("open artifact: %w", err)
	}

	if ra, ok := rc.(io.ReaderAt); ok {
		if sk, ok := rc.(io.Seeker); ok {
			size, err := sk.Seek(0, io.SeekEnd)
			if err == nil {
				return &readerAt{ReaderAt: ra, size: size, release: func() { _ = rc.Close() }}, nil
			}
		}
	}

	defer func() { _ = rc.Close() }()

	f, err := os.CreateTemp("", "oa-manifest-*")
	if err != nil {
		return nil, fmt.Errorf("spool artifact: %w", err)
	}

	release := func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}

	n, err := io.Copy(f, &capReader{r: ctxReader{ctx: ctx, r: rc}, n: maxSpoolBytes + 1})
	if err != nil && !errors.Is(err, ErrTooLarge) {
		release()
		return nil, fmt.Errorf("spool artifact: %w", err)
	}

	if n > maxSpoolBytes || errors.Is(err, ErrTooLarge) {
		release()
		return nil, ErrTooLarge
	}

	return &readerAt{ReaderAt: f, size: n, release: release}, nil
}

// readBlobText reads the artifact blob as text (at most MaxBytes).
func readBlobText(ctx context.Context, src Source) (string, error) {
	rc, err := src.Open()
	if err != nil {
		return "", fmt.Errorf("open artifact: %w", err)
	}
	defer func() { _ = rc.Close() }()

	return readText(ctxReader{ctx: ctx, r: rc})
}

// trimPyBody keeps the RFC 822 header block of a METADATA/PKG-INFO file and
// only the head of its (potentially huge) long description.
func trimPyBody(s string) string {
	head, body, found := strings.Cut(s, "\n\n")
	if !found {
		return s
	}

	if len(body) > pyBodyBytes {
		body = body[:pyBodyBytes]
	}

	return head + "\n\n" + body
}

// formatRule says which files of a format a manifest can be read from. An
// empty suffix list accepts every file name.
type formatRule struct {
	suffixes []string
	extract  extractor
}

func ruleFor(format string) (formatRule, bool) {
	switch format {
	case "npm":
		return formatRule{[]string{".tgz", ".tar.gz"}, extractNPM}, true
	case "pypi":
		return formatRule{[]string{".whl", ".zip", ".egg", ".tar.gz", ".tgz", ".tar.bz2"}, extractPyPI}, true
	case "maven":
		return formatRule{[]string{".pom", ".jar"}, extractJVM}, true
	case "ivy", "sbt":
		return formatRule{[]string{".pom", ".xml", ".jar"}, extractJVM}, true
	case "nuget":
		return formatRule{[]string{".nupkg"}, extractNuGet}, true
	case "cargo":
		return formatRule{[]string{".crate"}, extractCargo}, true
	case "helm":
		return formatRule{[]string{".tgz", ".tar.gz"}, extractHelm}, true
	case "deb":
		return formatRule{nil, extractDeb}, true
	case "rpm":
		return formatRule{nil, extractRPM}, true
	case "go":
		return formatRule{[]string{".mod", ".zip"}, extractGoMod}, true
	case "rubygems":
		return formatRule{[]string{".gem"}, extractGem}, true
	case "conda":
		return formatRule{[]string{".tar.bz2", ".conda"}, extractConda}, true
	case "conan":
		return formatRule{nil, extractConan}, true
	}

	return formatRule{}, false
}

// pick selects the extractor for a format and file name.
func pick(format, filename string) extractor {
	rule, ok := ruleFor(format)
	if !ok {
		return nil
	}

	name := strings.ToLower(path.Base(strings.ReplaceAll(filename, "\\", "/")))
	if len(rule.suffixes) > 0 && !hasAnySuffix(name, rule.suffixes...) {
		return nil
	}

	return rule.extract
}

func hasAnySuffix(s string, suffixes ...string) bool {
	for _, x := range suffixes {
		if strings.HasSuffix(s, x) {
			return true
		}
	}

	return false
}

// topLevel normalises an archive member name; ok is false for absolute or
// parent-escaping names.
func topLevel(name string) (string, bool) {
	name = strings.TrimPrefix(path.Clean(strings.ReplaceAll(name, "\\", "/")), "./")
	if name == "." || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
		return "", false
	}

	return name, true
}

// depthOne reports whether name is <dir>/<file> and returns <file>.
func depthOne(name string) (string, bool) {
	clean, ok := topLevel(name)
	if !ok {
		return "", false
	}

	dir, file, found := strings.Cut(clean, "/")
	if !found || dir == "" || file == "" || strings.Contains(file, "/") {
		return "", false
	}

	return file, true
}

// rootFile reports whether name is a file at the archive root and returns it.
func rootFile(name string) (string, bool) {
	clean, ok := topLevel(name)
	if !ok || strings.Contains(clean, "/") {
		return "", false
	}

	return clean, true
}
