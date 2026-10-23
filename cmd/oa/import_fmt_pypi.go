// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const (
	// pypiMaxHeaderBytes bounds what is read of a METADATA / PKG-INFO file: the
	// RFC 822 style header block at its top is all that is used.
	pypiMaxHeaderBytes = 1 << 20
	// pypiMaxScan bounds how much of a compressed sdist is decompressed while
	// looking for its PKG-INFO.
	pypiMaxScan = 1 << 30
	// pypiMaxLicenseLen: a longer License field is a whole license text, not an
	// identifier; the server derives the licenses it enforces from the file.
	pypiMaxLicenseLen = 256
)

var (
	errPyPIMetadata = errors.New("cannot read the package metadata")
	errPyPIIdentity = errors.New("cannot determine the project name and version")
	errPyPIReader   = errors.New("this archive type has no reader")
	errPyPINoInfo   = errors.New("the archive has no METADATA or PKG-INFO")

	pypiSeparators = regexp.MustCompile(`[-_.]+`)
	pypiEggPython  = regexp.MustCompile(`-(py\d+\.\d+)(?:-[^/]*)?\.egg$`)
	pypiEggName    = regexp.MustCompile(`^(.+)-([^-]+)-py\d+\.\d+(?:-.+)?$`)

	// pypiExtensions are the distribution file types that are imported.
	pypiExtensions = []string{".whl", ".tar.gz", ".zip", ".egg", ".tar.bz2", ".tar.xz", ".tar"} //nolint:gochecknoglobals // immutable lookup table
	// pypiServerExtensions are the endings the server strips from a file name
	// before splitting it into project and version.
	pypiServerExtensions = []string{".tar.gz", ".tar.bz2", ".tar.xz", ".tar.Z", ".zip", ".egg", ".tar", ".exe", ".msi", ".rpm", ".dmg"} //nolint:gochecknoglobals // immutable lookup table
)

func init() { //nolint:gochecknoinits // every format registers itself with the importer
	registerFormat(formatDef{
		format: formatPyPI, nexus: []string{"pypi"}, artifactory: []string{"pypi"},
		importable: importablePyPI, match: matchSHA, components: true,
		upload: pypiUpload, remove: pypiRemove,
	})
}

// importablePyPI selects distribution files; simple-index pages, signatures,
// checksums and PEP 658 metadata sidecars are regenerated or not kept.
func importablePyPI(p string) bool {
	base := strings.ToLower(path.Base(p))

	for _, ext := range pypiExtensions {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}

	return false
}

// pypiNormalizeName is the PEP 503 normalization the server applies to project
// names.
func pypiNormalizeName(name string) string {
	return strings.ToLower(pypiSeparators.ReplaceAllString(name, "-"))
}

// pypiSplitFilename returns the project part of a file name that carries the
// given version, with the same rules as the server's upload check: wheels are
// {dist}-{version}-{tags}.whl with "-" in the version written "_", everything
// else {dist}-{version}{ext}.
func pypiSplitFilename(filename, version string) (string, bool) {
	if stem, isWheel := strings.CutSuffix(filename, ".whl"); isWheel {
		parts := strings.Split(stem, "-")
		if len(parts) < 5 {
			return "", false
		}

		return parts[0], parts[1] == strings.ReplaceAll(version, "-", "_")
	}

	dist, found := strings.CutSuffix(pypiStem(filename), "-"+version)

	return dist, found && dist != ""
}

// pypiStem is a file name without its extension (the server's list of known
// ones first, any other last dot-suffix otherwise).
func pypiStem(filename string) string {
	for _, ext := range pypiServerExtensions {
		if cut, found := strings.CutSuffix(filename, ext); found {
			return cut
		}
	}

	if i := strings.LastIndex(filename, "."); i > 0 {
		return filename[:i]
	}

	return filename
}

// pypiBelongs reports whether the server accepts a file under the project name
// and version: the file name must be built from them.
func pypiBelongs(filename, name, version string) bool {
	if strings.Trim(name, "-_.") == "" || version == "" {
		return false
	}

	dist, ok := pypiSplitFilename(filename, version)

	return ok && pypiNormalizeName(dist) == pypiNormalizeName(name)
}

// pypiGuess reads a project name and version out of a file name alone, taking
// the last dash of an sdist as the separator; it is a last resort, since names
// and versions may both contain dashes.
func pypiGuess(filename string) (string, string) {
	if stem, isWheel := strings.CutSuffix(filename, ".whl"); isWheel {
		if parts := strings.Split(stem, "-"); len(parts) >= 5 {
			return parts[0], parts[1]
		}

		return "", ""
	}

	stem := pypiStem(filename)

	// An egg is {name}-{version}-py{X.Y}[-{platform}].egg.
	if m := pypiEggName.FindStringSubmatch(stem); m != nil && strings.HasSuffix(filename, ".egg") {
		return m[1], m[2]
	}

	i := strings.LastIndex(stem, "-")
	if i <= 0 {
		return "", ""
	}

	return stem[:i], stem[i+1:]
}

// pypiDistType gives the twine "filetype" and "pyversion" of a distribution
// file: wheels carry their python tag, eggs the interpreter in their name,
// source distributions "source".
func pypiDistType(filename string) (string, string) {
	switch lower := strings.ToLower(filename); {
	case strings.HasSuffix(lower, ".whl"):
		if parts := strings.Split(pypiStem(filename), "-"); len(parts) >= 5 {
			return "bdist_wheel", parts[len(parts)-3]
		}

		return "bdist_wheel", ""
	case strings.HasSuffix(lower, ".egg"):
		if m := pypiEggPython.FindStringSubmatch(filename); m != nil {
			return "bdist_egg", m[1]
		}

		return "bdist_egg", ""
	}

	return "sdist", "source"
}

// ─── Metadata ─────────────────────────────────────────────────────────────────

// pypiMetadata is what an upload form carries of a distribution's core
// metadata (METADATA of a wheel, PKG-INFO of an sdist or egg).
type pypiMetadata struct {
	Name           string
	Version        string
	RequiresPython string
	Summary        string
	Author         string
	AuthorEmail    string
	License        string
	Keywords       string
	Classifiers    []string
}

// pypiHeaders are the header fields of a core metadata file, keyed by lower
// case name; continuation lines are joined with a newline.
type pypiHeaders map[string][]string

func (h pypiHeaders) first(key string) string {
	if vs := h[key]; len(vs) > 0 {
		return strings.TrimSpace(vs[0])
	}

	return ""
}

// pypiParseHeaders reads the header block at the top of a core metadata file.
func pypiParseHeaders(r io.Reader) pypiHeaders {
	br := bufio.NewReader(io.LimitReader(r, pypiMaxHeaderBytes))
	headers := pypiHeaders{}
	last := ""

	for {
		line, err := br.ReadString('\n')

		// A blank line ends the header block; what follows is the long description.
		if line = strings.TrimRight(line, "\r\n"); line == "" {
			return headers
		}

		if (line[0] == ' ' || line[0] == '\t') && last != "" {
			vs := headers[last]
			vs[len(vs)-1] += "\n" + strings.TrimSpace(line)
		} else if key, value, ok := strings.Cut(line, ":"); ok {
			last = strings.ToLower(strings.TrimSpace(key))
			headers[last] = append(headers[last], strings.TrimSpace(value))
		}

		if err != nil {
			return headers
		}
	}
}

func pypiMetadataFrom(h pypiHeaders) pypiMetadata {
	md := pypiMetadata{
		Name:           h.first("name"),
		Version:        h.first("version"),
		RequiresPython: h.first("requires-python"),
		Summary:        h.first("summary"),
		Author:         h.first("author"),
		AuthorEmail:    h.first("author-email"),
		License:        h.first("license-expression"),
		Keywords:       h.first("keywords"),
	}

	if md.License == "" {
		if lic := h.first("license"); len(lic) <= pypiMaxLicenseLen && !strings.ContainsAny(lic, "\r\n") {
			md.License = lic
		}
	}

	for _, c := range h["classifier"] {
		if c = strings.TrimSpace(c); c != "" {
			md.Classifiers = append(md.Classifiers, c)
		}
	}

	return md
}

// pypiMetadataRank tells how well an archive member is the distribution's
// metadata file: 1 for a wheel's *.dist-info/METADATA, an egg's
// EGG-INFO/PKG-INFO or an sdist's top-level PKG-INFO, 2 for the PKG-INFO of
// an egg-info directory inside an sdist, 0 for anything else.
func pypiMetadataRank(name string) int {
	parts := strings.Split(strings.TrimLeft(path.Clean(name), "/"), "/")

	switch {
	case len(parts) == 2 && parts[1] == "PKG-INFO", len(parts) == 2 && parts[1] == "METADATA" && strings.HasSuffix(parts[0], ".dist-info"):
		return 1
	case len(parts) == 3 && parts[2] == "PKG-INFO" && strings.HasSuffix(parts[1], ".egg-info"):
		return 2
	}

	return 0
}

// pypiReadMetadata reads the core metadata out of a spooled distribution.
func pypiReadMetadata(sp *spooled, filename string) (pypiMetadata, error) {
	var (
		md  pypiMetadata
		err error
	)

	switch lower := strings.ToLower(filename); {
	case strings.HasSuffix(lower, ".whl"), strings.HasSuffix(lower, ".egg"), strings.HasSuffix(lower, ".zip"):
		md, err = pypiZipMetadata(sp)
	case strings.HasSuffix(lower, ".tar.gz"):
		var zr *gzip.Reader

		if zr, err = gzip.NewReader(sp.Reader()); err == nil {
			defer func() { _ = zr.Close() }()

			md, err = pypiTarMetadata(zr)
		}
	case strings.HasSuffix(lower, ".tar.bz2"):
		md, err = pypiTarMetadata(bzip2.NewReader(sp.Reader()))
	case strings.HasSuffix(lower, ".tar"):
		md, err = pypiTarMetadata(sp.Reader())
	default:
		err = errPyPIReader
	}

	if err != nil {
		return pypiMetadata{}, fmt.Errorf("%w: %w", errPyPIMetadata, err)
	}

	return md, nil
}

func pypiZipMetadata(sp *spooled) (pypiMetadata, error) {
	zr, err := zip.NewReader(sp.Reader(), sp.Size)
	if err != nil {
		return pypiMetadata{}, err
	}

	var best *zip.File

	for _, f := range zr.File {
		if rank := pypiMetadataRank(f.Name); rank != 0 && (best == nil || rank < pypiMetadataRank(best.Name)) {
			best = f
		}
	}

	if best == nil {
		return pypiMetadata{}, errPyPINoInfo
	}

	rc, err := best.Open()
	if err != nil {
		return pypiMetadata{}, err
	}

	defer func() { _ = rc.Close() }()

	return pypiMetadataFrom(pypiParseHeaders(rc)), nil
}

// pypiTarMetadata scans a tar stream for the sdist's PKG-INFO.
func pypiTarMetadata(r io.Reader) (pypiMetadata, error) {
	tr := tar.NewReader(io.LimitReader(r, pypiMaxScan))

	var (
		fallback pypiMetadata
		found    bool
	)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return pypiMetadata{}, err
		}

		rank := pypiMetadataRank(hdr.Name)
		if hdr.Typeflag != tar.TypeReg || rank == 0 {
			continue
		}

		md := pypiMetadataFrom(pypiParseHeaders(tr))
		if rank == 1 {
			return md, nil
		}

		if !found {
			fallback, found = md, true
		}
	}

	if !found {
		return pypiMetadata{}, errPyPINoInfo
	}

	return fallback, nil
}

// ─── Upload ───────────────────────────────────────────────────────────────────

// pypiIdentity picks the project name and version to upload a file under: the
// first source whose pair the server accepts for this file name. The source
// system's own record of the package comes first (Nexus component, Artifactory
// pypi.name / pypi.version), then the archive's metadata, then the file name.
func pypiIdentity(a sourceAsset, filename string, md pypiMetadata) (string, string, error) {
	guessName, guessVersion := pypiGuess(filename)

	for _, c := range [][2]string{
		{a.Component.Name, a.Component.Version},
		{firstValue(a.Props["pypi.name"]), firstValue(a.Props["pypi.version"])},
		{md.Name, md.Version},
		{guessName, guessVersion},
	} {
		if pypiBelongs(filename, c[0], c[1]) {
			return c[0], c[1], nil
		}
	}

	return "", "", fmt.Errorf("%w of %s: none of the source's record, the archive metadata or the file name gives a pair the server accepts for this file name (it must read <project>-<version>.<ext>)", errPyPIIdentity, filename)
}

func pypiFields(name, version, filename, sha string, md pypiMetadata) [][2]string {
	filetype, pyversion := pypiDistType(filename)

	return [][2]string{
		{":action", "file_upload"},
		{"protocol_version", "1"},
		{"name", name},
		{"version", version},
		{"filetype", filetype},
		{"pyversion", pyversion},
		{"sha256_digest", sha},
		{"requires_python", md.RequiresPython},
		{"summary", md.Summary},
		{"author", md.Author},
		{"author_email", md.AuthorEmail},
		{"license", md.License},
		{"keywords", md.Keywords},
		{"classifiers", strings.Join(md.Classifiers, "\n")},
	}
}

// pypiUpload posts the file the way twine does (POST /legacy/, multipart). The
// file is spooled first: its own metadata supplies what the source does not
// name (project and version of a bare sdist) and the form fields pip relies on
// (requires_python).
func pypiUpload(ctx context.Context, t *importTarget, r uploadRequest) (uploadResult, error) {
	filename := path.Base(r.Asset.Path)
	digest := sha256.New()

	sp, err := spool(io.TeeReader(r.Body, digest))
	if err != nil {
		return uploadResult{}, err
	}

	defer func() { _ = sp.Close() }()

	// Unreadable metadata is only fatal when nothing else names the package.
	md, mdErr := pypiReadMetadata(sp, filename)

	name, version, err := pypiIdentity(r.Asset, filename, md)
	if err != nil {
		if mdErr != nil {
			err = fmt.Errorf("%w (%w)", err, mdErr)
		}

		return uploadResult{}, err
	}

	form, contentType := multipartStreamFields(pypiFields(name, version, filename, hex.EncodeToString(digest.Sum(nil)), md), sp.Reader(), "content", filename)

	defer func() { _ = form.Close() }()

	hdr := r.Meta.header()
	hdr.Set("Content-Type", contentType)

	resp, err := t.send(ctx, http.MethodPost, "/repos/"+url.PathEscape(r.Dst)+"/legacy/", form, -1, hdr)
	if err != nil {
		return uploadResult{}, err
	}

	_ = resp.Body.Close()

	// The upload reply is empty: read the id and the stored digest from the
	// listing, by file name.
	return t.storedResult(ctx, r.Dst, formatPyPI, pypiNormalizeName(name), func(p listedPackage) bool {
		return p.Filename == filename
	})
}

// pypiRemove deletes a file that failed verification (DELETE /files/<file>).
func pypiRemove(ctx context.Context, t *importTarget, dst string, a sourceAsset, _ uploadResult) error {
	return t.deletePath(ctx, "/repos/"+url.PathEscape(dst)+"/files/"+url.PathEscape(path.Base(a.Path)))
}
