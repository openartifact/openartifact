// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

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
	Name          string
	Version       string
	Release       string
	Epoch         uint32
	Arch          string
	Summary       string
	Description   string
	License       string
	URL           string
	Vendor        string
	Group         string
	Packager      string
	SourceRPM     string
	BuildHost     string
	BuildTime     int64
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
	Suggests      []Dependency
	Supplements   []Dependency
	Enhances      []Dependency
	Files         []FileEntry
	Changelogs    []ChangelogEntry
}

func ParseRPM(r io.Reader) (*Package, error) {
	// Lead is 96 bytes
	if _, err := ParseLead(r); err != nil {
		return nil, fmt.Errorf("rpm: parsing lead: %w", err)
	}

	sigHeader, err := ParseHeader(r, 1<<24)
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

	pkgHeader, err := ParseHeader(r, 1<<24)
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
		PackageSize: int64(getUint32(pkgHeader, RPMTAG_SIZE)),
		HeaderStart: headerStart,
		HeaderEnd:   headerEnd,
	}

	if err := validateRequired(pkg); err != nil {
		return nil, err
	}

	pkg.Provides, _ = ParseDependencies(pkgHeader, RPMTAG_PROVIDES, RPMTAG_PROVIDEFLAGS, RPMTAG_PROVIDEVERSION)
	pkg.Requires, _ = ParseDependencies(pkgHeader, RPMTAG_REQUIRENAME, RPMTAG_REQUIREFLAGS, RPMTAG_REQUIREVERSION)
	pkg.Conflicts, _ = ParseDependencies(pkgHeader, RPMTAG_CONFLICTNAME, RPMTAG_CONFLICTFLAGS, RPMTAG_CONFLICTVERSION)
	pkg.Obsoletes, _ = ParseDependencies(pkgHeader, RPMTAG_OBSOLETENAME, RPMTAG_OBSOLETEFLAGS, RPMTAG_OBSOLETEVERSION)
	pkg.Suggests, _ = ParseDependencies(pkgHeader, RPMTAG_SUGGESTSNAME, RPMTAG_SUGGESTSFLAGS, RPMTAG_SUGGESTSVERSION)
	pkg.Supplements, _ = ParseDependencies(pkgHeader, RPMTAG_SUPPLEMENTNAME, RPMTAG_SUPPLEMENTFLAGS, RPMTAG_SUPPLEMENTVERSION)
	pkg.Enhances, _ = ParseDependencies(pkgHeader, RPMTAG_ENHANCENAME, RPMTAG_ENHANCEFLAGS, RPMTAG_ENHANCEVERSION)

	pkg.Files = extractFiles(pkgHeader)
	pkg.Changelogs = extractChangelogs(pkgHeader)

	return pkg, nil
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

		if strings.ContainsAny(v, "/\\\x00 \t\r\n") || strings.Contains(v, "..") {
			return fmt.Errorf("%w: invalid characters in %s tag", ErrInvalidHeader, name)
		}
	}

	return nil
}

func extractFiles(h *Header) []FileEntry {
	bases, _ := h.GetStringArray(RPMTAG_BASENAMES)
	if len(bases) == 0 {
		return nil
	}

	dirNames, _ := h.GetStringArray(RPMTAG_DIRNAMES)
	dirIdx, _ := h.GetInt32s(RPMTAG_DIRINDEXES)
	sizes, _ := h.GetInt32s(RPMTAG_FILESIZES)
	modes, _ := h.GetInt32s(RPMTAG_FILEMODES)
	mtimes, _ := h.GetInt32s(RPMTAG_FILEMTIMES)
	digests, _ := h.GetStringArray(RPMTAG_FILEDIGESTS)
	linktos, _ := h.GetStringArray(RPMTAG_FILELINKTOS)

	files := make([]FileEntry, 0, len(bases))
	for i, base := range bases {
		dir := ""
		if i < len(dirIdx) && dirIdx[i] >= 0 && int(dirIdx[i]) < len(dirNames) {
			dir = dirNames[dirIdx[i]]
		}

		entry := FileEntry{Path: dir + base}

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

		if i < len(digests) {
			entry.Digest = digests[i]
		}

		if i < len(linktos) {
			entry.LinkTo = linktos[i]
		}

		files = append(files, entry)
	}

	return files
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

	if len(p.Changelogs) > 0 {
		metadata["changelogcount"] = len(p.Changelogs)
	}

	return metadata
}

// extraData is the persisted (JSON) subset of a Package not stored in scalar columns.
type extraData struct {
	Provides    []Dependency     `json:"provides,omitempty"`
	Requires    []Dependency     `json:"requires,omitempty"`
	Conflicts   []Dependency     `json:"conflicts,omitempty"`
	Obsoletes   []Dependency     `json:"obsoletes,omitempty"`
	Suggests    []Dependency     `json:"suggests,omitempty"`
	Supplements []Dependency     `json:"supplements,omitempty"`
	Enhances    []Dependency     `json:"enhances,omitempty"`
	Files       []FileEntry      `json:"files,omitempty"`
	Changelogs  []ChangelogEntry `json:"changelogs,omitempty"`
}

// MarshalExtra encodes dependencies, files and changelogs for persistence.
func (p *Package) MarshalExtra() ([]byte, error) {
	return json.Marshal(extraData{
		Provides: p.Provides, Requires: p.Requires, Conflicts: p.Conflicts, Obsoletes: p.Obsoletes,
		Suggests: p.Suggests, Supplements: p.Supplements, Enhances: p.Enhances,
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
	p.Suggests, p.Supplements, p.Enhances = e.Suggests, e.Supplements, e.Enhances
	p.Files, p.Changelogs = e.Files, e.Changelogs

	return nil
}
