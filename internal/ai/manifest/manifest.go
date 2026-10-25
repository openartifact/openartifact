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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/pathseg"
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
	// ErrAmbiguous means the artifact holds several candidates for a manifest
	// that must be unique (Strict sources only).
	ErrAmbiguous = errors.New("artifact holds more than one manifest")
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
	// MemberLimit bounds what is read from one archive member; zero means
	// MaxBytes.
	MemberLimit int64
	// Strict makes a member (or, for METADATA / PKG-INFO, a header block)
	// larger than the limit fail with ErrTooLarge instead of being cut short,
	// and an archive with two candidates for a unique manifest fail with
	// ErrAmbiguous. Callers that act on the manifest's content (license
	// enforcement) need this: a silently truncated or arbitrarily picked
	// manifest could hide what the package really declares.
	Strict bool
	// Deps also reads the lockfiles an artifact ships next to its manifest
	// (npm-shrinkwrap.json, package-lock.json, Cargo.lock, Chart.lock),
	// listed after the manifest parts. SBOM generation sets it; the AI
	// analysis and license readers leave it off so their input is unchanged.
	Deps bool
}

// limits are the per-read bounds derived from a Source.
type limits struct {
	n      int64
	strict bool
}

func (s Source) limits() limits {
	n := s.MemberLimit
	if n <= 0 {
		n = maxMemberBytes
	}

	return limits{n: n, strict: s.Strict}
}

// extractor reads the manifest files of one artifact kind.
type extractor func(ctx context.Context, src Source) ([]section, error)

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

// Part is one manifest file read from an artifact.
type Part struct {
	// Label names the file (package.json, METADATA, pom.xml, ...); empty for
	// artifacts that are the manifest themselves (a .pom).
	Label string
	// Body is the file's text, valid UTF-8 without NULs.
	Body string
}

// ExtractParts returns the manifest files of the artifact separately, each
// read up to src.MemberLimit (MaxBytes when zero). Unlike Extract it does not
// trim the result to MaxBytes. It returns ErrNotFound when the artifact holds
// no manifest file.
func ExtractParts(ctx context.Context, src Source) ([]Part, error) {
	ex := pick(src.Format, src.Filename)
	if ex == nil {
		return nil, ErrUnsupported
	}

	sections, err := ex(ctx, src)
	if err != nil {
		return nil, err
	}

	parts := make([]Part, 0, len(sections))

	for _, sec := range sections {
		body := sanitize(sec.body)
		if strings.TrimSpace(body) != "" {
			parts = append(parts, Part{Label: sec.label, Body: body})
		}
	}

	if len(parts) == 0 {
		return nil, ErrNotFound
	}

	return parts, nil
}

// Extract returns the manifest text of the artifact (at most MaxBytes).
func Extract(ctx context.Context, src Source) (string, error) {
	ex := pick(src.Format, src.Filename)
	if ex == nil {
		return "", ErrUnsupported
	}

	sections, err := ex(ctx, src)
	if err != nil {
		return "", err
	}

	text := clean(render(sections))
	if strings.TrimSpace(text) == "" {
		return "", ErrNotFound
	}

	return text, nil
}

// sanitize makes the text valid UTF-8 without NULs.
func sanitize(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "\uFFFD"), "\x00", "")
}

// clean sanitizes the text and trims it to MaxBytes.
func clean(s string) string {
	s = sanitize(s)

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

// render renders the sections of an extraction. A lone unlabelled section (an
// artifact that is the manifest itself) is returned as it was read.
func render(sections []section) string {
	if len(sections) == 1 && sections[0].label == "" {
		return sections[0].body
	}

	return join(sections...)
}

// join renders sections; a single unlabelled section is returned bare. A
// section that is blank once sanitized (a body of NULs) is left out, as
// ExtractParts leaves it out, not rendered as a bare heading.
func join(parts ...section) string {
	var b strings.Builder

	for _, p := range parts {
		body := sanitize(p.body)
		if strings.TrimSpace(body) == "" {
			continue
		}

		if p.label != "" {
			fmt.Fprintf(&b, "### %s\n", p.label)
		}

		b.WriteString(body)

		if !strings.HasSuffix(body, "\n") {
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

// readText reads up to l.n bytes of r. Extra input is ignored, or an
// ErrTooLarge in strict mode.
func readText(r io.Reader, l limits) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, l.n+1))
	if err != nil {
		return "", fmt.Errorf("read manifest: %w", err)
	}

	if int64(len(data)) > l.n {
		if l.strict {
			return "", ErrTooLarge
		}

		data = data[:l.n]
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

// readBlobText reads the artifact blob as text (at most the member limit).
func readBlobText(ctx context.Context, src Source) ([]section, error) {
	rc, err := src.Open()
	if err != nil {
		return nil, fmt.Errorf("open artifact: %w", err)
	}
	defer func() { _ = rc.Close() }()

	text, err := readText(ctxReader{ctx: ctx, r: rc}, src.limits())
	if err != nil {
		return nil, err
	}

	return []section{{body: text}}, nil
}

// readPyText reads the RFC 822 header block of a METADATA/PKG-INFO file and,
// unless strict, only the head of its (potentially huge) long description. A
// strict read stops at the end of the headers (the body is never read) and
// fails with ErrTooLarge when the headers alone exceed the limit, so padding
// cannot push a License field out of view.
func readPyText(r io.Reader, l limits) (string, error) {
	if !l.strict {
		text, err := readText(r, l)
		if err != nil {
			return "", err
		}

		return trimPyBody(text), nil
	}

	br := bufio.NewReader(io.LimitReader(r, l.n+1))

	var head strings.Builder

	for {
		line, err := br.ReadString('\n')
		head.WriteString(line)

		if int64(head.Len()) > l.n {
			return "", ErrTooLarge
		}

		if strings.TrimRight(line, "\r\n") == "" && line != "" {
			break
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return "", fmt.Errorf("read manifest: %w", err)
		}
	}

	return head.String(), nil
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
	if name == "." || strings.HasPrefix(name, "/") || pathseg.Escapes(name) {
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
