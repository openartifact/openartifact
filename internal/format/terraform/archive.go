// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package terraform

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
)

const (
	// maxModuleBytes and maxProviderBytes cap an upload body.
	maxModuleBytes   int64 = 256 << 20
	maxProviderBytes int64 = 1 << 30

	// maxEntries caps the number of entries in an archive.
	maxEntries = 20000
	// maxEntryBytes caps one entry's expanded size.
	maxEntryBytes int64 = 2 << 30
	// maxModuleExpanded and maxProviderExpanded cap an archive's expanded size.
	maxModuleExpanded   int64 = 1 << 30
	maxProviderExpanded int64 = 2 << 30

	// bombMinBytes and bombRatio flag an archive that expands to more than
	// bombMinBytes and to more than bombRatio times its own size.
	bombMinBytes int64 = 100 << 20
	bombRatio    int64 = 1000

	// maxManifestBytes caps terraform-registry-manifest.json.
	maxManifestBytes = 64 << 10

	manifestName = "terraform-registry-manifest.json"

	extZip   = "zip"
	extTarGz = "tar.gz"
)

// archiveError is a rejected archive. semantic distinguishes a well-formed
// archive that is not a valid module or provider (422) from a malformed or
// unsafe one (400).
type archiveError struct {
	msg      string
	semantic bool
}

func (e *archiveError) Error() string { return e.msg }

func malformed(format string, args ...any) error {
	return &archiveError{msg: fmt.Sprintf(format, args...)}
}

func semantic(format string, args ...any) error {
	return &archiveError{msg: fmt.Sprintf(format, args...), semantic: true}
}

// archiveInfo is what validation learned about an accepted archive.
type archiveInfo struct {
	ext       string
	protocols []string // provider only: from the registry manifest, nil when absent
}

// archiveLimits are the bounds one archive kind is validated against.
type archiveLimits struct {
	expanded int64
}

// detectArchive tells a zip from a gzip stream by its magic bytes; the
// extension of the upload URL is never trusted. It returns "" for anything else.
func detectArchive(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return extZip
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return extTarGz
	}

	return ""
}

// cleanEntryName validates one archive entry name and returns it normalised
// (no leading "./", no trailing slash, "" for the archive root itself). It
// refuses everything that could escape the directory a client extracts into.
func cleanEntryName(name string) (string, error) {
	switch {
	case name == "":
		return "", malformed("entry with an empty name")
	case strings.ContainsRune(name, 0):
		return "", malformed("entry name %q contains a NUL byte", name)
	case strings.ContainsRune(name, '\\'):
		return "", malformed("entry name %q contains a backslash", name)
	case strings.HasPrefix(name, "/"):
		return "", malformed("entry name %q is absolute", name)
	case hasDriveLetter(name):
		return "", malformed("entry name %q has a drive letter", name)
	}

	for seg := range strings.SplitSeq(strings.TrimSuffix(name, "/"), "/") {
		if seg == ".." {
			return "", malformed("entry name %q escapes the archive root", name)
		}
	}

	clean := path.Clean(name)
	if clean == "." {
		return "", nil
	}

	// "./C:x" only becomes a drive-relative name once the "./" is cleaned away.
	if hasDriveLetter(clean) {
		return "", malformed("entry name %q has a drive letter", name)
	}

	return clean, nil
}

// hasDriveLetter reports whether name starts with a Windows drive designator
// ("C:"), which extracts outside the target directory on that platform.
func hasDriveLetter(name string) bool {
	return len(name) >= 2 && name[1] == ':' && isAlnum(name[0])
}

// entrySet collects the entries of an archive and enforces the limits that
// do not depend on its format.
type entrySet struct {
	limits     archiveLimits
	compressed int64
	total      int64
	names      map[string]struct{}
	files      []string
	manifest   []byte
}

func newEntrySet(limits archiveLimits, compressed int64) *entrySet {
	return &entrySet{limits: limits, compressed: compressed, names: make(map[string]struct{})}
}

// add records one entry. size is its declared expanded size.
func (s *entrySet) add(name string, isDir bool, size int64) (string, error) {
	clean, err := cleanEntryName(name)
	if err != nil {
		return "", err
	}

	if clean == "" {
		return "", nil
	}

	if len(s.names) >= maxEntries {
		return "", malformed("more than %d entries", maxEntries)
	}

	if _, dup := s.names[clean]; dup {
		return "", malformed("duplicate entry %q", clean)
	}

	s.names[clean] = struct{}{}

	if isDir {
		return clean, nil
	}

	if size > maxEntryBytes {
		return "", malformed("entry %q is larger than %d bytes", clean, maxEntryBytes)
	}

	s.total += size
	if s.total > s.limits.expanded {
		return "", malformed("archive expands to more than %d bytes", s.limits.expanded)
	}

	if s.total > bombMinBytes && s.total/max(s.compressed, 1) > bombRatio {
		return "", malformed("archive expands more than %d times its size", bombRatio)
	}

	s.files = append(s.files, clean)

	return clean, nil
}

// validateModuleArchive checks an uploaded module archive (zip or tar.gz) of
// size bytes and returns its kind. Nothing is extracted: the archive is read
// once, completely, so a corrupt or lying archive is refused here rather than
// at the first client that installs it.
func validateModuleArchive(ra io.ReaderAt, size int64) (archiveInfo, error) {
	set := newEntrySet(archiveLimits{expanded: maxModuleExpanded}, size)

	var info archiveInfo

	head := make([]byte, 4)
	n, _ := ra.ReadAt(head, 0)

	switch info.ext = detectArchive(head[:n]); info.ext {
	case extZip:
		if err := readZip(ra, size, set); err != nil {
			return archiveInfo{}, err
		}
	case extTarGz:
		if err := readTarGz(io.NewSectionReader(ra, 0, size), set); err != nil {
			return archiveInfo{}, err
		}
	default:
		return archiveInfo{}, malformed("the body is neither a zip nor a gzip-compressed tar archive")
	}

	if len(set.files) == 0 {
		return archiveInfo{}, malformed("the archive has no files")
	}

	if !hasTerraformFile(set.files, set.names) {
		return archiveInfo{}, semantic("the archive has no .tf or .tf.json file at its root or in its single top-level directory")
	}

	return info, nil
}

// validateProviderArchive checks an uploaded provider platform zip: it must
// be a safe zip with the provider binary, named the way Terraform looks for
// it, at its root.
func validateProviderArchive(ra io.ReaderAt, size int64, typ, version string) (archiveInfo, error) {
	head := make([]byte, 4)
	n, _ := ra.ReadAt(head, 0)

	if detectArchive(head[:n]) != extZip {
		return archiveInfo{}, malformed("a provider archive must be a zip")
	}

	set := newEntrySet(archiveLimits{expanded: maxProviderExpanded}, size)
	if err := readZip(ra, size, set); err != nil {
		return archiveInfo{}, err
	}

	if len(set.files) == 0 {
		return archiveInfo{}, malformed("the archive has no files")
	}

	if !slices.ContainsFunc(set.files, func(f string) bool { return isProviderBinary(f, typ, version) }) {
		return archiveInfo{}, semantic("the zip has no terraform-provider-%s_v%s binary at its root", typ, version)
	}

	info := archiveInfo{ext: extZip}

	if set.manifest != nil {
		protocols, err := parseManifestProtocols(set.manifest)
		if err != nil {
			return archiveInfo{}, err
		}

		info.protocols = protocols
	}

	return info, nil
}

// isProviderBinary reports whether the root-level file name is the provider
// plugin: terraform-provider-TYPE_vVERSION, optionally with a _xN protocol
// suffix and a .exe extension.
func isProviderBinary(file, typ, version string) bool {
	rest, ok := strings.CutPrefix(file, "terraform-provider-"+typ+"_v"+version)
	if !ok {
		return false
	}

	rest = strings.TrimSuffix(rest, ".exe")

	if suffix, ok := strings.CutPrefix(rest, "_x"); ok {
		return suffix != "" && strings.Trim(suffix, "0123456789") == ""
	}

	return rest == ""
}

// hasTerraformFile reports whether the module has a Terraform configuration
// file at its root, or, when everything lives under one top-level directory
// (the layout of a source tarball), directly inside that directory.
func hasTerraformFile(files []string, names map[string]struct{}) bool {
	isTF := func(f string) bool { return strings.HasSuffix(f, ".tf") || strings.HasSuffix(f, ".tf.json") }

	if slices.ContainsFunc(files, func(f string) bool { return !strings.Contains(f, "/") && isTF(f) }) {
		return true
	}

	// A file outside any directory (README next to the directory) means the
	// archive root is not the module.
	if slices.ContainsFunc(files, func(f string) bool { return !strings.Contains(f, "/") }) {
		return false
	}

	top := ""

	for name := range names {
		first, _, _ := strings.Cut(name, "/")
		if top != "" && top != first {
			return false
		}

		top = first
	}

	return slices.ContainsFunc(files, func(f string) bool {
		dir, file, _ := strings.Cut(f, "/")

		return dir == top && !strings.Contains(file, "/") && isTF(file)
	})
}

// readZip enumerates a zip, checking every entry and reading all of them to
// the end so declared sizes and checksums are real.
func readZip(ra io.ReaderAt, size int64, set *entrySet) error {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return malformed("not a readable zip archive: %v", err)
	}

	if len(zr.File) > maxEntries {
		return malformed("more than %d entries", maxEntries)
	}

	// Pass one: names, types and declared sizes, so a bomb is refused before
	// anything is decompressed.
	var regular []*zip.File

	for _, f := range zr.File {
		mode := f.Mode()
		if mode&fs.ModeSymlink != 0 || mode&(fs.ModeDevice|fs.ModeNamedPipe|fs.ModeSocket|fs.ModeCharDevice) != 0 {
			return malformed("entry %q is not a regular file or directory", f.Name)
		}

		isDir := f.FileInfo().IsDir()

		clean, addErr := set.add(f.Name, isDir, int64(min(f.UncompressedSize64, uint64(maxEntryBytes)+1)))
		if addErr != nil {
			return addErr
		}

		if !isDir && clean != "" {
			regular = append(regular, f)
		}
	}

	// Pass two: read every file completely.
	for _, f := range regular {
		if err := drainZipEntry(f, set); err != nil {
			return err
		}
	}

	return nil
}

func drainZipEntry(f *zip.File, set *entrySet) error {
	rc, err := f.Open()
	if err != nil {
		return malformed("entry %q cannot be read: %v", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	want := int64(f.UncompressedSize64) //nolint:gosec // bounded by maxEntryBytes in readZip

	if clean, _ := cleanEntryName(f.Name); clean == manifestName {
		data, err := io.ReadAll(io.LimitReader(rc, maxManifestBytes+1))
		if err != nil {
			return malformed("entry %q cannot be read: %v", f.Name, err)
		}

		if len(data) > maxManifestBytes {
			return semantic("%s is larger than %d bytes", manifestName, maxManifestBytes)
		}

		set.manifest = data

		if int64(len(data)) != want {
			return malformed("entry %q is shorter than its declared size", f.Name)
		}

		return nil
	}

	n, err := io.Copy(io.Discard, io.LimitReader(rc, want+1))
	if err != nil {
		return malformed("entry %q is corrupt: %v", f.Name, err)
	}

	if n != want {
		return malformed("entry %q does not match its declared size", f.Name)
	}

	return nil
}

// readTarGz enumerates a gzip-compressed tar stream.
func readTarGz(src io.Reader, set *entrySet) error {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return malformed("not a readable gzip stream: %v", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return malformed("not a readable tar archive: %v", err)
		}

		if err := addTarEntry(set, hdr); err != nil {
			return err
		}
	}

	// Read the gzip trailer, bounded by what the limits could still allow, so a
	// truncated or padded stream is refused.
	room := set.limits.expanded - set.total + 1<<20
	if _, err := io.Copy(io.Discard, io.LimitReader(gz, room)); err != nil {
		return malformed("the gzip stream is corrupt: %v", err)
	}

	return nil
}

func addTarEntry(set *entrySet, hdr *tar.Header) error {
	switch hdr.Typeflag {
	case tar.TypeXGlobalHeader:
		return nil
	case tar.TypeDir:
		_, err := set.add(hdr.Name, true, 0)

		return err
	case tar.TypeReg:
		if hdr.Size < 0 {
			return malformed("entry %q has a negative size", hdr.Name)
		}

		_, err := set.add(hdr.Name, false, hdr.Size)

		return err
	default:
		return malformed("entry %q is a link or special file", hdr.Name)
	}
}

// parseManifestProtocols reads metadata.protocol_versions from a
// terraform-registry-manifest.json. A manifest without the list yields nil.
func parseManifestProtocols(data []byte) ([]string, error) {
	var manifest struct {
		Version  int `json:"version"`
		Metadata struct {
			ProtocolVersions []string `json:"protocol_versions"`
		} `json:"metadata"`
	}

	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, semantic("%s is not valid JSON: %v", manifestName, err)
	}

	if len(manifest.Metadata.ProtocolVersions) == 0 {
		return nil, nil
	}

	protocols, err := normalizeProtocols(manifest.Metadata.ProtocolVersions)
	if err != nil {
		return nil, semantic("%s: %v", manifestName, err)
	}

	return protocols, nil
}

// maxProtocols caps how many plugin protocol versions a provider may declare.
const maxProtocols = 8

var (
	errTooManyProtocols = errors.New("too many protocol versions")
	errBadProtocol      = errors.New("invalid protocol version")
)

// normalizeProtocols validates plugin protocol versions ("5.0", "6.0"),
// removes duplicates and sorts them.
func normalizeProtocols(in []string) ([]string, error) {
	if len(in) > maxProtocols {
		return nil, fmt.Errorf("%w: at most %d", errTooManyProtocols, maxProtocols)
	}

	out := make([]string, 0, len(in))

	for _, p := range in {
		if !validProtocol(p) {
			return nil, fmt.Errorf("%w %q", errBadProtocol, p)
		}

		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}

	slices.Sort(out)

	return out, nil
}

// validProtocol reports whether p is a plugin protocol version: two decimal
// numbers of up to three digits separated by a dot.
func validProtocol(p string) bool {
	major, minor, ok := strings.Cut(p, ".")
	if !ok {
		return false
	}

	digits := func(s string) bool {
		return s != "" && len(s) <= 3 && strings.Trim(s, "0123456789") == ""
	}

	return digits(major) && digits(minor)
}
