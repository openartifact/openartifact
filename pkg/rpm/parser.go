// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/openartifact/openartifact/internal/pathseg"
)

// MaxFileListBytes bounds the total size of the strings (path, digest, link
// target) kept for a package's file list. Every path is dirname+basename and
// all entries may reference the same multi-megabyte dirname, so the entry
// count alone does not bound the expanded size.
const MaxFileListBytes = 16 << 20

type FileEntry struct {
	Path   string
	Size   int64
	Mode   uint16
	MTime  int64
	Digest string
	LinkTo string
	IsDir  bool
}

type ChangelogEntry struct {
	Time        int64
	Author      string
	Description string
}

type Package struct {
	Name        string
	Version     string
	Release     string
	Epoch       uint32
	Arch        string
	Summary     string
	Description string
	License     string
	URL         string
	Vendor      string
	Group       string
	Packager    string
	SourceRPM   string
	BuildHost   string
	BuildTime   int64
	// FileTime is when the file entered the repository (the <time file=>
	// value of primary.xml); zero falls back to BuildTime.
	FileTime      int64
	PackageSize   int64
	InstalledSize int64
	ArchiveSize   int64
	SHA256        string
	HeaderStart   int64
	HeaderEnd     int64
	Provides      []Dependency
	Requires      []Dependency
	Conflicts     []Dependency
	Obsoletes     []Dependency
	Recommends    []Dependency
	Suggests      []Dependency
	Supplements   []Dependency
	Enhances      []Dependency
	Files         []FileEntry
	Changelogs    []ChangelogEntry

	// FilesTruncated is true when the header declared more files than
	// MaxArrayEntries or their strings exceeded MaxFileListBytes, so Files is
	// only a prefix of the package's file list.
	FilesTruncated bool

	// WeakDepsParsed is true when the persisted extra data already carries
	// weak dependencies (see extraData.WeakDeps).
	WeakDepsParsed bool

	// SizesParsed is true when the persisted extra data already carries the
	// installed and archive sizes (see extraData.SizesParsed).
	SizesParsed bool
}

func ParseRPM(r io.Reader) (*Package, error) {
	// Lead is 96 bytes
	if _, err := ParseLead(r); err != nil {
		return nil, fmt.Errorf("rpm: parsing lead: %w", err)
	}

	sigHeader, err := ParseHeader(r, MaxHeaderDataSize)
	if err != nil {
		return nil, fmt.Errorf("rpm: parsing signature header: %w", err)
	}

	padBytes := PadTo8(sigHeader.TotalSize())
	if padBytes > 0 {
		pad := make([]byte, padBytes)
		if _, err := io.ReadFull(r, pad); err != nil {
			return nil, fmt.Errorf("rpm: signature padding: %w", err)
		}
	}

	// Calculate header start offset: 96 (lead) + sigHeader.TotalSize() + padBytes
	headerStart := int64(96) + sigHeader.TotalSize() + padBytes

	pkgHeader, err := ParseHeader(r, MaxHeaderDataSize)
	if err != nil {
		return nil, fmt.Errorf("rpm: parsing package header: %w", err)
	}

	// Calculate header end offset: headerStart + pkgHeader.TotalSize()
	headerEnd := headerStart + pkgHeader.TotalSize()

	pkg := &Package{
		Name:        getString(pkgHeader, RPMTAG_NAME),
		Version:     getString(pkgHeader, RPMTAG_VERSION),
		Release:     getString(pkgHeader, RPMTAG_RELEASE),
		Epoch:       getUint32(pkgHeader, RPMTAG_EPOCH),
		Arch:        getString(pkgHeader, RPMTAG_ARCH),
		Summary:     getString(pkgHeader, RPMTAG_SUMMARY),
		Description: getString(pkgHeader, RPMTAG_DESCRIPTION),
		License:     getString(pkgHeader, RPMTAG_LICENSE),
		URL:         getString(pkgHeader, RPMTAG_URL),
		Vendor:      getString(pkgHeader, RPMTAG_VENDOR),
		Group:       getString(pkgHeader, RPMTAG_GROUP),
		Packager:    getString(pkgHeader, RPMTAG_PACKAGER),
		SourceRPM:   getString(pkgHeader, RPMTAG_SOURCERPM),
		BuildHost:   getString(pkgHeader, RPMTAG_BUILDHOST),
		BuildTime:   int64(getUint32(pkgHeader, RPMTAG_BUILDTIME)),
		HeaderStart: headerStart,
		HeaderEnd:   headerEnd,
		// PackageSize (the .rpm file size) is not in the header; callers set it.
		InstalledSize: pkgHeader.firstNumber(RPMTAG_LONGSIZE, RPMTAG_SIZE),
		ArchiveSize:   archiveSize(sigHeader, pkgHeader),
	}

	if err := validateRequired(pkg); err != nil {
		return nil, err
	}

	if err := parseAllDependencies(pkg, pkgHeader); err != nil {
		return nil, err
	}

	pkg.Files, pkg.FilesTruncated = extractFiles(pkgHeader)
	pkg.Changelogs = extractChangelogs(pkgHeader)

	return pkg, nil
}

// archiveSize returns the uncompressed payload size: format-4 packages carry it
// in the signature header (LONGARCHIVESIZE, else PAYLOADSIZE); format-6
// packages moved it to the main header as PAYLOADSIZEALT (their PAYLOADSIZE is
// the compressed size).
func archiveSize(sigHeader, pkgHeader *Header) int64 {
	if v := sigHeader.firstNumber(RPMSIGTAG_LONGARCHIVESIZE, RPMSIGTAG_PAYLOADSIZE); v > 0 {
		return v
	}

	return pkgHeader.firstNumber(RPMTAG_PAYLOADSIZEALT)
}

// parseAllDependencies fills every dependency list of pkg from h. Malformed
// lists are skipped, but a list over the entry cap rejects the package: silently
// dropping part of a dependency list would make the package installable
// without its requirements.
func parseAllDependencies(pkg *Package, h *Header) error {
	for _, d := range []struct {
		dst                   *[]Dependency
		name, flags, versions TagType
	}{
		{&pkg.Provides, RPMTAG_PROVIDES, RPMTAG_PROVIDEFLAGS, RPMTAG_PROVIDEVERSION},
		{&pkg.Requires, RPMTAG_REQUIRENAME, RPMTAG_REQUIREFLAGS, RPMTAG_REQUIREVERSION},
		{&pkg.Conflicts, RPMTAG_CONFLICTNAME, RPMTAG_CONFLICTFLAGS, RPMTAG_CONFLICTVERSION},
		{&pkg.Obsoletes, RPMTAG_OBSOLETENAME, RPMTAG_OBSOLETEFLAGS, RPMTAG_OBSOLETEVERSION},
		{&pkg.Recommends, RPMTAG_RECOMMENDNAME, RPMTAG_RECOMMENDFLAGS, RPMTAG_RECOMMENDVERSION},
		{&pkg.Suggests, RPMTAG_SUGGESTSNAME, RPMTAG_SUGGESTSFLAGS, RPMTAG_SUGGESTSVERSION},
		{&pkg.Supplements, RPMTAG_SUPPLEMENTNAME, RPMTAG_SUPPLEMENTFLAGS, RPMTAG_SUPPLEMENTVERSION},
		{&pkg.Enhances, RPMTAG_ENHANCENAME, RPMTAG_ENHANCEFLAGS, RPMTAG_ENHANCEVERSION},
	} {
		if n := h.ArrayLen(d.name); n > MaxArrayEntries {
			return fmt.Errorf("%w: tag %d declares %d entries, max %d", ErrHeaderTooLarge, d.name, n, MaxArrayEntries)
		}

		*d.dst, _ = ParseDependencies(h, d.name, d.flags, d.versions)
	}

	return nil
}

func getString(h *Header, tag TagType) string {
	val, _ := h.GetString(tag)
	return val
}

func getUint32(h *Header, tag TagType) uint32 {
	val, _ := h.GetUint32(tag)
	return val
}

// validateRequired rejects packages missing identity tags or containing
// characters that are unsafe in a filename.
func validateRequired(p *Package) error {
	for name, v := range map[string]string{"NAME": p.Name, "VERSION": p.Version, "RELEASE": p.Release, "ARCH": p.Arch} {
		if v == "" {
			return fmt.Errorf("%w: missing %s tag", ErrInvalidHeader, name)
		}

		if !pathseg.Name(v) || strings.ContainsAny(v, " \t\r\n") {
			return fmt.Errorf("%w: invalid characters in %s tag", ErrInvalidHeader, name)
		}
	}

	return nil
}

// extractFiles decodes the file list. It keeps at most MaxArrayEntries files
// and MaxFileListBytes of strings, and reports whether it stopped early.
func extractFiles(h *Header) ([]FileEntry, bool) {
	bases, _ := h.GetStringArray(RPMTAG_BASENAMES)
	if len(bases) == 0 {
		return nil, false
	}

	truncated := h.ArrayLen(RPMTAG_BASENAMES) > len(bases) && len(bases) >= MaxArrayEntries

	dirNames, _ := h.GetStringArray(RPMTAG_DIRNAMES)
	dirIdx, _ := h.GetInt32s(RPMTAG_DIRINDEXES)
	sizes, _ := h.GetInt32s(RPMTAG_FILESIZES)
	modes, _ := h.GetInt32s(RPMTAG_FILEMODES)
	mtimes, _ := h.GetInt32s(RPMTAG_FILEMTIMES)
	digests, _ := h.GetStringArray(RPMTAG_FILEDIGESTS)
	linktos, _ := h.GetStringArray(RPMTAG_FILELINKTOS)

	budget := MaxFileListBytes
	files := make([]FileEntry, 0, len(bases))

	for i, base := range bases {
		dir := ""
		if i < len(dirIdx) && dirIdx[i] >= 0 && int(dirIdx[i]) < len(dirNames) {
			dir = dirNames[dirIdx[i]]
		}

		entry := FileEntry{}

		if i < len(digests) {
			entry.Digest = digests[i]
		}

		if i < len(linktos) {
			entry.LinkTo = linktos[i]
		}

		// Check before concatenating: dir may be megabytes long.
		cost := len(dir) + len(base) + len(entry.Digest) + len(entry.LinkTo)
		if cost > budget {
			truncated = true

			break
		}

		budget -= cost
		entry.Path = dir + base

		if i < len(sizes) {
			entry.Size = int64(sizes[i])
		}

		if i < len(modes) {
			entry.Mode = uint16(modes[i] & 0xFFFF)
			entry.IsDir = (modes[i] & 0xF000) == 0x4000
		}

		if i < len(mtimes) {
			entry.MTime = int64(mtimes[i])
		}

		files = append(files, entry)
	}

	return files, truncated
}

func extractChangelogs(h *Header) []ChangelogEntry {
	times, _ := h.GetInt32s(RPMTAG_CHANGELOGTIME)
	authors, _ := h.GetStringArray(RPMTAG_CHANGELOGNAME)
	texts, _ := h.GetStringArray(RPMTAG_CHANGELOGTEXT)

	if times == nil {
		return nil
	}

	entries := make([]ChangelogEntry, 0, len(times))
	for i := range times {
		entry := ChangelogEntry{Time: int64(times[i])}
		if i < len(authors) {
			entry.Author = authors[i]
		}

		if i < len(texts) {
			entry.Description = texts[i]
		}

		entries = append(entries, entry)
	}

	return entries
}

func (p *Package) GetMetadata() map[string]any {
	metadata := map[string]any{
		"format":        "rpm",
		"name":          p.Name,
		"version":       p.Version,
		"release":       p.Release,
		"epoch":         p.Epoch,
		"architecture":  p.Arch,
		"summary":       p.Summary,
		"description":   p.Description,
		"license":       p.License,
		"url":           p.URL,
		"vendor":        p.Vendor,
		"group":         p.Group,
		"packager":      p.Packager,
		"sourcerpm":     p.SourceRPM,
		"buildhost":     p.BuildHost,
		"buildtime":     p.BuildTime,
		"packagesize":   p.PackageSize,
		"installedsize": p.InstalledSize,
		"archivesize":   p.ArchiveSize,
	}

	if len(p.Provides) > 0 {
		metadata["provides"] = p.Provides
	}

	if len(p.Requires) > 0 {
		metadata["requires"] = p.Requires
	}

	if len(p.Conflicts) > 0 {
		metadata["conflicts"] = p.Conflicts
	}

	if len(p.Obsoletes) > 0 {
		metadata["obsoletes"] = p.Obsoletes
	}

	if len(p.Recommends) > 0 {
		metadata["recommends"] = p.Recommends
	}

	if len(p.Suggests) > 0 {
		metadata["suggests"] = p.Suggests
	}

	if len(p.Supplements) > 0 {
		metadata["supplements"] = p.Supplements
	}

	if len(p.Enhances) > 0 {
		metadata["enhances"] = p.Enhances
	}

	if len(p.Files) > 0 {
		metadata["filecount"] = len(p.Files)
	}

	if p.FilesTruncated {
		metadata["files_truncated"] = true
	}

	if len(p.Changelogs) > 0 {
		metadata["changelogcount"] = len(p.Changelogs)
	}

	return metadata
}

// extraData is the persisted (JSON) subset of a Package not stored in scalar columns.
type extraData struct {
	Provides    []Dependency `json:"provides,omitempty"`
	Requires    []Dependency `json:"requires,omitempty"`
	Conflicts   []Dependency `json:"conflicts,omitempty"`
	Obsoletes   []Dependency `json:"obsoletes,omitempty"`
	Recommends  []Dependency `json:"recommends,omitempty"`
	Suggests    []Dependency `json:"suggests,omitempty"`
	Supplements []Dependency `json:"supplements,omitempty"`
	Enhances    []Dependency `json:"enhances,omitempty"`
	// WeakDeps marks rows persisted by a version that stored weak dependencies
	// (recommends/suggests/supplements/enhances). Rows without it are re-read
	// from their blob when metadata is regenerated.
	WeakDeps bool `json:"weak_deps,omitempty"`
	// SizesParsed marks rows whose installed/archive sizes were read from the
	// header correctly: older versions stored 0 (installed_size/archive_size
	// columns), so rows without it are re-read from their blob when metadata
	// is regenerated and the values below take precedence over the columns.
	SizesParsed   bool             `json:"sizes_parsed,omitempty"`
	InstalledSize int64            `json:"installed_size,omitempty"`
	ArchiveSize   int64            `json:"archive_size,omitempty"`
	Files         []FileEntry      `json:"files,omitempty"`
	Changelogs    []ChangelogEntry `json:"changelogs,omitempty"`
}

// MarshalExtra encodes dependencies, sizes, files and changelogs for persistence.
func (p *Package) MarshalExtra() ([]byte, error) {
	return json.Marshal(extraData{
		Provides: p.Provides, Requires: p.Requires, Conflicts: p.Conflicts, Obsoletes: p.Obsoletes,
		Recommends: p.Recommends, WeakDeps: true, Suggests: p.Suggests, Supplements: p.Supplements, Enhances: p.Enhances,
		SizesParsed: true, InstalledSize: p.InstalledSize, ArchiveSize: p.ArchiveSize,
		Files: p.Files, Changelogs: p.Changelogs,
	})
}

// UnmarshalExtra restores data written by MarshalExtra.
func (p *Package) UnmarshalExtra(data []byte) error {
	var e extraData
	if len(data) == 0 {
		return nil
	}

	if err := json.Unmarshal(data, &e); err != nil {
		return fmt.Errorf("rpm: decoding extra data: %w", err)
	}

	p.Provides, p.Requires, p.Conflicts, p.Obsoletes = e.Provides, e.Requires, e.Conflicts, e.Obsoletes
	p.Recommends, p.WeakDepsParsed = e.Recommends, e.WeakDeps
	p.Suggests, p.Supplements, p.Enhances = e.Suggests, e.Supplements, e.Enhances
	p.Files, p.Changelogs = e.Files, e.Changelogs

	p.SizesParsed = e.SizesParsed
	if e.SizesParsed {
		p.InstalledSize, p.ArchiveSize = e.InstalledSize, e.ArchiveSize
	}

	return nil
}
