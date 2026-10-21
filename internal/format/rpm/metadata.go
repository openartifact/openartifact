// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	rpm "github.com/openartifact/openartifact/pkg/rpm"
)

type RepomdData struct {
	Type         string
	Location     string
	Checksum     string
	ChecksumType string
	Timestamp    int64
	Size         int64
	OpenSize     int64
}

type repomdXML struct {
	XMLName  xml.Name        `xml:"repomd"`
	Xmlns    string          `xml:"xmlns,attr"`
	XmlnsRpm string          `xml:"xmlns:rpm,attr"`
	Revision string          `xml:"revision"`
	Data     []repomdDataXML `xml:"data"`
}

type repomdDataXML struct {
	XMLName   xml.Name    `xml:"data"`
	Type      string      `xml:"type,attr"`
	Location  locationXML `xml:"location"`
	Checksum  checksumXML `xml:"checksum"`
	Timestamp int64       `xml:"timestamp"`
	Size      int64       `xml:"size"`
	OpenSize  int64       `xml:"open-size,omitempty"`
}

type locationXML struct {
	Href string `xml:"href,attr"`
}

type checksumXML struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type primaryPackageXML struct {
	XMLName     xml.Name `xml:"package"`
	Type        string   `xml:"type,attr"`
	Name        string   `xml:"name"`
	Arch        string   `xml:"arch"`
	Version     primaryVersionXML
	Checksum    primaryChecksumXML
	Summary     string `xml:"summary"`
	Description string `xml:"description"`
	Packager    string `xml:"packager,omitempty"`
	URL         string `xml:"url,omitempty"`
	Time        primaryTimeXML
	Size        primarySizeXML
	Location    locationXML
	Format      primaryFormatXML
}

type primaryVersionXML struct {
	XMLName xml.Name `xml:"version"`
	Epoch   string   `xml:"epoch,attr,omitempty"`
	Ver     string   `xml:"ver,attr"`
	Rel     string   `xml:"rel,attr"`
}

type primaryChecksumXML struct {
	XMLName xml.Name `xml:"checksum"`
	Type    string   `xml:"type,attr"`
	PkgID   string   `xml:"pkgid,attr"`
	Value   string   `xml:",chardata"`
}

type primaryTimeXML struct {
	XMLName xml.Name `xml:"time"`
	File    int64    `xml:"file,attr"`
	Build   int64    `xml:"build,attr"`
}

type primarySizeXML struct {
	XMLName   xml.Name `xml:"size"`
	Package   int64    `xml:"package,attr"`
	Installed int64    `xml:"installed,attr"`
	Archive   int64    `xml:"archive,attr"`
}

type primaryFormatXML struct {
	XMLName     xml.Name `xml:"format"`
	Xmlns       string   `xml:"xmlns:rpm,attr"`
	License     string   `xml:"rpm:license,omitempty"`
	Vendor      string   `xml:"rpm:vendor,omitempty"`
	Group       string   `xml:"rpm:group,omitempty"`
	BuildHost   string   `xml:"rpm:buildhost,omitempty"`
	SourceRPM   string   `xml:"rpm:sourcerpm,omitempty"`
	HeaderRange primaryHeaderRangeXML
	Provides    []primaryEntryXML `xml:"rpm:provides>rpm:entry,omitempty"`
	Requires    []primaryEntryXML `xml:"rpm:requires>rpm:entry,omitempty"`
	Conflicts   []primaryEntryXML `xml:"rpm:conflicts>rpm:entry,omitempty"`
	Obsoletes   []primaryEntryXML `xml:"rpm:obsoletes>rpm:entry,omitempty"`
	Suggests    []primaryEntryXML `xml:"rpm:suggests>rpm:entry,omitempty"`
	Enhances    []primaryEntryXML `xml:"rpm:enhances>rpm:entry,omitempty"`
	Recommends  []primaryEntryXML `xml:"rpm:recommends>rpm:entry,omitempty"`
	Supplements []primaryEntryXML `xml:"rpm:supplements>rpm:entry,omitempty"`
}

type primaryHeaderRangeXML struct {
	XMLName xml.Name `xml:"rpm:header-range"`
	Start   int64    `xml:"start,attr"`
	End     int64    `xml:"end,attr"`
}

type primaryEntryXML struct {
	XMLName xml.Name `xml:"rpm:entry"`
	Name    string   `xml:"name,attr"`
	Flags   string   `xml:"flags,attr,omitempty"`
	Epoch   string   `xml:"epoch,attr,omitempty"`
	Ver     string   `xml:"ver,attr,omitempty"`
	Rel     string   `xml:"rel,attr,omitempty"`
	Pre     string   `xml:"pre,attr,omitempty"`
}

type primaryMetadata struct {
	XMLName  xml.Name            `xml:"metadata"`
	Xmlns    string              `xml:"xmlns,attr"`
	XmlnsRpm string              `xml:"xmlns:rpm,attr"`
	Packages int                 `xml:"packages,attr"`
	PkgList  []primaryPackageXML `xml:"package"`
}

func GenerateRepomdXML(revision string, entries []RepomdData) ([]byte, error) {
	dataEntries := make([]repomdDataXML, len(entries))
	for i, e := range entries {
		dataEntries[i] = repomdDataXML{
			Type:      e.Type,
			Location:  locationXML{Href: e.Location},
			Checksum:  checksumXML{Type: e.ChecksumType, Value: e.Checksum},
			Timestamp: e.Timestamp,
			Size:      e.Size,
			OpenSize:  e.OpenSize,
		}
	}

	if revision == "" {
		revision = strconv.FormatInt(time.Now().Unix(), 10)
	}

	repomd := repomdXML{
		Xmlns:    "http://linux.duke.edu/metadata/repo",
		XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Revision: revision,
		Data:     dataEntries,
	}

	output, err := xml.MarshalIndent(repomd, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rpm: marshaling repomd.xml: %w", err)
	}

	return append([]byte(xml.Header), output...), nil
}

func GeneratePrimaryXML(packages []rpm.Package, baseURL string) ([]byte, error) {
	pkgEntries := make([]primaryPackageXML, len(packages))
	for i, pkg := range packages {
		epochStr := ""
		if pkg.Epoch > 0 {
			epochStr = strconv.FormatUint(uint64(pkg.Epoch), 10)
		}

		pkgEntries[i] = primaryPackageXML{
			Type:        "rpm",
			Name:        pkg.Name,
			Arch:        pkg.Arch,
			Version:     primaryVersionXML{Epoch: epochStr, Ver: pkg.Version, Rel: pkg.Release},
			Checksum:    primaryChecksumXML{Type: "sha256", PkgID: "YES", Value: pkg.SHA256},
			Summary:     pkg.Summary,
			Description: pkg.Description,
			Packager:    pkg.Packager,
			URL:         pkg.URL,
			Time:        primaryTimeXML{File: pkg.BuildTime, Build: pkg.BuildTime},
			Size:        primarySizeXML{Package: pkg.PackageSize, Installed: pkg.InstalledSize, Archive: pkg.ArchiveSize},
			Location:    locationXML{Href: fmt.Sprintf("Packages/%s-%s-%s.%s.rpm", pkg.Name, pkg.Version, pkg.Release, pkg.Arch)},
			Format: primaryFormatXML{
				Xmlns:       "http://linux.duke.edu/metadata/rpm",
				License:     pkg.License,
				Vendor:      pkg.Vendor,
				Group:       pkg.Group,
				BuildHost:   pkg.BuildHost,
				SourceRPM:   pkg.SourceRPM,
				HeaderRange: primaryHeaderRangeXML{Start: pkg.HeaderStart, End: pkg.HeaderEnd},
				Provides:    depsToPrimaryEntries(pkg.Provides, false),
				Requires:    depsToPrimaryEntries(filterRequires(&pkg), true),
				Conflicts:   depsToPrimaryEntries(pkg.Conflicts, false),
				Obsoletes:   depsToPrimaryEntries(pkg.Obsoletes, false),
				Suggests:    depsToPrimaryEntries(pkg.Suggests, false),
				Enhances:    depsToPrimaryEntries(pkg.Enhances, false),
				Recommends:  depsToPrimaryEntries(pkg.Recommends, false),
				Supplements: depsToPrimaryEntries(pkg.Supplements, false),
			},
		}
	}

	metadata := primaryMetadata{
		Xmlns:    "http://linux.duke.edu/metadata/common",
		XmlnsRpm: "http://linux.duke.edu/metadata/rpm",
		Packages: len(packages),
		PkgList:  pkgEntries,
	}

	output, err := xml.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rpm: marshaling primary.xml: %w", err)
	}

	return append([]byte(xml.Header), output...), nil
}

var glibcSymbolRe = regexp.MustCompile(`^(.*)\(GLIBC_(\d+(?:\.\d+)*)\)((?:\(64bit\))?)$`)

// compareDotted compares dotted numeric versions ("2.9" < "2.34").
func compareDotted(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}

		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}

		if x != y {
			return x - y
		}
	}

	return 0
}

type depKey struct {
	name, flags, ver string
	epoch            uint32
	pre              bool
}

func keyOf(d rpm.Dependency) depKey {
	return depKey{d.Name, rpm.SenseFlagsString(d.Flags), d.Version, d.Epoch, d.IsPre()}
}

type glibcBest struct {
	idx int
	ver string
}

// bestGlibcRequires finds, per library+arch, the requirement with the highest GLIBC_x symbol version.
func bestGlibcRequires(reqs []rpm.Dependency) map[string]glibcBest {
	best := map[string]glibcBest{}

	for i, d := range reqs {
		if m := glibcSymbolRe.FindStringSubmatch(d.Name); m != nil && !d.Versioned() {
			key := m[1] + "|" + m[3]
			if b, ok := best[key]; !ok || compareDotted(m[2], b.ver) > 0 {
				best[key] = glibcBest{i, m[2]}
			}
		}
	}

	return best
}

type glibcAction int

const (
	glibcKeep glibcAction = iota
	glibcDrop
	glibcLast
)

// dropGlibc decides how a requirement is treated by the GLIBC collapsing.
func dropGlibc(d rpm.Dependency, idx int, best map[string]glibcBest) glibcAction {
	if d.Versioned() {
		return glibcKeep
	}

	if m := glibcSymbolRe.FindStringSubmatch(d.Name); m != nil {
		if best[m[1]+"|"+m[3]].idx != idx {
			return glibcDrop
		}

		// createrepo_c emits the surviving GLIBC requirement last.
		return glibcLast
	}

	if base, ok := strings.CutSuffix(d.Name, "()(64bit)"); ok {
		if _, has := best[base+"|(64bit)"]; has {
			return glibcDrop
		}
	} else if base, ok := strings.CutSuffix(d.Name, "()"); ok {
		if _, has := best[base+"|"]; has {
			return glibcDrop
		}
	}

	return glibcKeep
}

// filterRequires mirrors createrepo_c: duplicates are dropped, requirements the
// package satisfies itself (own provides or packaged files) are dropped unless
// they are script-time requirements, and versioned GLIBC symbol requirements
// collapse to the highest version per library.
func filterRequires(pkg *rpm.Package) []rpm.Dependency {
	provides := make(map[depKey]bool, len(pkg.Provides))
	provided := make(map[string]bool, len(pkg.Provides)+len(pkg.Files))

	for _, d := range pkg.Provides {
		k := keyOf(d)
		k.pre = false
		provides[k] = true
		provided[d.Name] = true
	}

	for _, f := range pkg.Files {
		provided[f.Path] = true
	}

	best := bestGlibcRequires(pkg.Requires)

	var glibc []rpm.Dependency

	seen := make(map[depKey]bool, len(pkg.Requires))
	out := make([]rpm.Dependency, 0, len(pkg.Requires))

	for i, d := range pkg.Requires {
		k := keyOf(d)
		if seen[k] {
			continue
		}

		seen[k] = true

		if !d.IsPre() && !d.IsRPMLib() {
			versioned := k.flags != ""
			if (versioned && provides[k]) || (!versioned && provided[d.Name]) {
				continue
			}
		}

		switch dropGlibc(d, i, best) {
		case glibcDrop:
			continue
		case glibcLast:
			glibc = append(glibc, d)
			continue
		case glibcKeep:
		}

		out = append(out, d)
	}

	return append(out, glibc...)
}

// depsToPrimaryEntries renders dependencies the way createrepo_c does. When
// isRequires is set, rpmlib(...) features are skipped and script-time
// requirements carry pre="1".
func depsToPrimaryEntries(deps []rpm.Dependency, isRequires bool) []primaryEntryXML {
	if len(deps) == 0 {
		return nil
	}

	out := make([]primaryEntryXML, 0, len(deps))

	for _, dep := range deps {
		if isRequires && dep.IsRPMLib() {
			continue
		}

		e := depToPrimaryEntry(dep)
		if isRequires && dep.IsPre() {
			e.Pre = "1"
		}

		out = append(out, e)
	}

	return out
}

func depToPrimaryEntry(dep rpm.Dependency) primaryEntryXML {
	e := primaryEntryXML{Name: dep.Name, Flags: rpm.SenseFlagsString(dep.Flags)}
	if e.Flags == "" {
		return e
	}

	e.Epoch = strconv.FormatUint(uint64(dep.Epoch), 10)
	e.Ver, e.Rel = rpm.SplitVersionRelease(dep.Version)

	return e
}

type filelistsPackageXML struct {
	XMLName xml.Name `xml:"package"`
	PkgID   string   `xml:"pkgid,attr"`
	Name    string   `xml:"name,attr"`
	Arch    string   `xml:"arch,attr"`
	Version versionRefXML
	Files   []filelistsFileXML
}

type versionRefXML struct {
	XMLName xml.Name `xml:"version"`
	Epoch   string   `xml:"epoch,attr,omitempty"`
	Ver     string   `xml:"ver,attr"`
	Rel     string   `xml:"rel,attr"`
}

type filelistsFileXML struct {
	XMLName xml.Name `xml:"file"`
	Path    string   `xml:",chardata"`
	Type    string   `xml:"type,attr,omitempty"`
}

type filelistsMetadata struct {
	XMLName  xml.Name              `xml:"filelists"`
	Xmlns    string                `xml:"xmlns,attr"`
	Packages int                   `xml:"packages,attr"`
	PkgList  []filelistsPackageXML `xml:"package"`
}

func GenerateFilelistsXML(packages []rpm.Package) ([]byte, error) {
	pkgEntries := make([]filelistsPackageXML, len(packages))
	for i, pkg := range packages {
		epochStr := ""
		if pkg.Epoch > 0 {
			epochStr = strconv.FormatUint(uint64(pkg.Epoch), 10)
		}

		files := make([]filelistsFileXML, len(pkg.Files))
		for j, f := range pkg.Files {
			files[j] = filelistsFileXML{Path: f.Path}
		}

		if len(files) == 0 {
			files = nil
		}

		pkgEntries[i] = filelistsPackageXML{
			PkgID:   pkg.SHA256,
			Name:    pkg.Name,
			Arch:    pkg.Arch,
			Version: versionRefXML{Epoch: epochStr, Ver: pkg.Version, Rel: pkg.Release},
			Files:   files,
		}
	}

	metadata := filelistsMetadata{
		Xmlns:    "http://linux.duke.edu/metadata/filelists",
		Packages: len(packages),
		PkgList:  pkgEntries,
	}

	output, err := xml.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rpm: marshaling filelists.xml: %w", err)
	}

	return append([]byte(xml.Header), output...), nil
}

type otherChangelogXML struct {
	XMLName xml.Name `xml:"changelog"`
	Author  string   `xml:"author,attr"`
	Date    int64    `xml:"date,attr"`
	Text    string   `xml:",chardata"`
}

type otherPackageXML struct {
	XMLName    xml.Name `xml:"package"`
	PkgID      string   `xml:"pkgid,attr"`
	Name       string   `xml:"name,attr"`
	Arch       string   `xml:"arch,attr"`
	Version    versionRefXML
	Changelogs []otherChangelogXML `xml:"changelog"`
}

type otherMetadata struct {
	XMLName  xml.Name          `xml:"otherdata"`
	Xmlns    string            `xml:"xmlns,attr"`
	Packages int               `xml:"packages,attr"`
	PkgList  []otherPackageXML `xml:"package"`
}

func GenerateOtherXML(packages []rpm.Package) ([]byte, error) {
	pkgEntries := make([]otherPackageXML, len(packages))
	for i, pkg := range packages {
		epochStr := ""
		if pkg.Epoch > 0 {
			epochStr = strconv.FormatUint(uint64(pkg.Epoch), 10)
		}

		changelogs := make([]otherChangelogXML, len(pkg.Changelogs))
		for j, cl := range pkg.Changelogs {
			changelogs[j] = otherChangelogXML{
				Author: cl.Author,
				Date:   cl.Time,
				Text:   cl.Description,
			}
		}

		pkgEntries[i] = otherPackageXML{
			PkgID:      pkg.SHA256,
			Name:       pkg.Name,
			Arch:       pkg.Arch,
			Version:    versionRefXML{Epoch: epochStr, Ver: pkg.Version, Rel: pkg.Release},
			Changelogs: changelogs,
		}
	}

	metadata := otherMetadata{
		Xmlns:    "http://linux.duke.edu/metadata/other",
		Packages: len(packages),
		PkgList:  pkgEntries,
	}

	output, err := xml.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("rpm: marshaling other.xml: %w", err)
	}

	return append([]byte(xml.Header), output...), nil
}
