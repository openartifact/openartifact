// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

// Reference implementation of the primary/filelists/other generators: the
// reflection-based encoding/xml version that the streaming writers replaced.
// The equivalence tests assert byte-identical output against it.

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	rpm "github.com/openartifact/openartifact/pkg/rpm"
)

type refPrimaryPackageXML struct {
	XMLName     xml.Name `xml:"package"`
	Type        string   `xml:"type,attr"`
	Name        string   `xml:"name"`
	Arch        string   `xml:"arch"`
	Version     refPrimaryVersionXML
	Checksum    refPrimaryChecksumXML
	Summary     string `xml:"summary"`
	Description string `xml:"description"`
	Packager    string `xml:"packager,omitempty"`
	URL         string `xml:"url,omitempty"`
	Time        refPrimaryTimeXML
	Size        refPrimarySizeXML
	Location    locationXML `xml:"location"`
	Format      refPrimaryFormatXML
}

type refPrimaryVersionXML struct {
	XMLName xml.Name `xml:"version"`
	Epoch   string   `xml:"epoch,attr,omitempty"`
	Ver     string   `xml:"ver,attr"`
	Rel     string   `xml:"rel,attr"`
}

type refPrimaryChecksumXML struct {
	XMLName xml.Name `xml:"checksum"`
	Type    string   `xml:"type,attr"`
	PkgID   string   `xml:"pkgid,attr"`
	Value   string   `xml:",chardata"`
}

type refPrimaryTimeXML struct {
	XMLName xml.Name `xml:"time"`
	File    int64    `xml:"file,attr"`
	Build   int64    `xml:"build,attr"`
}

type refPrimarySizeXML struct {
	XMLName   xml.Name `xml:"size"`
	Package   int64    `xml:"package,attr"`
	Installed int64    `xml:"installed,attr"`
	Archive   int64    `xml:"archive,attr"`
}

type refPrimaryFormatXML struct {
	XMLName     xml.Name `xml:"format"`
	Xmlns       string   `xml:"xmlns:rpm,attr"`
	License     string   `xml:"rpm:license,omitempty"`
	Vendor      string   `xml:"rpm:vendor,omitempty"`
	Group       string   `xml:"rpm:group,omitempty"`
	BuildHost   string   `xml:"rpm:buildhost,omitempty"`
	SourceRPM   string   `xml:"rpm:sourcerpm,omitempty"`
	HeaderRange refPrimaryHeaderRangeXML
	Provides    []refPrimaryEntryXML `xml:"rpm:provides>rpm:entry,omitempty"`
	Requires    []refPrimaryEntryXML `xml:"rpm:requires>rpm:entry,omitempty"`
	Conflicts   []refPrimaryEntryXML `xml:"rpm:conflicts>rpm:entry,omitempty"`
	Obsoletes   []refPrimaryEntryXML `xml:"rpm:obsoletes>rpm:entry,omitempty"`
	Suggests    []refPrimaryEntryXML `xml:"rpm:suggests>rpm:entry,omitempty"`
	Enhances    []refPrimaryEntryXML `xml:"rpm:enhances>rpm:entry,omitempty"`
	Recommends  []refPrimaryEntryXML `xml:"rpm:recommends>rpm:entry,omitempty"`
	Supplements []refPrimaryEntryXML `xml:"rpm:supplements>rpm:entry,omitempty"`
}

type refPrimaryHeaderRangeXML struct {
	XMLName xml.Name `xml:"rpm:header-range"`
	Start   int64    `xml:"start,attr"`
	End     int64    `xml:"end,attr"`
}

type refPrimaryEntryXML struct {
	XMLName xml.Name `xml:"rpm:entry"`
	Name    string   `xml:"name,attr"`
	Flags   string   `xml:"flags,attr,omitempty"`
	Epoch   string   `xml:"epoch,attr,omitempty"`
	Ver     string   `xml:"ver,attr,omitempty"`
	Rel     string   `xml:"rel,attr,omitempty"`
	Pre     string   `xml:"pre,attr,omitempty"`
}

type refPrimaryMetadata struct {
	XMLName  xml.Name               `xml:"metadata"`
	Xmlns    string                 `xml:"xmlns,attr"`
	XmlnsRpm string                 `xml:"xmlns:rpm,attr"`
	Packages int                    `xml:"packages,attr"`
	PkgList  []refPrimaryPackageXML `xml:"package"`
}

func refGeneratePrimaryXML(packages []rpm.Package, baseURL string) ([]byte, error) {
	pkgEntries := make([]refPrimaryPackageXML, len(packages))
	for i, pkg := range packages {
		epochStr := ""
		if pkg.Epoch > 0 {
			epochStr = strconv.FormatUint(uint64(pkg.Epoch), 10)
		}

		pkgEntries[i] = refPrimaryPackageXML{
			Type:        "rpm",
			Name:        pkg.Name,
			Arch:        pkg.Arch,
			Version:     refPrimaryVersionXML{Epoch: epochStr, Ver: pkg.Version, Rel: pkg.Release},
			Checksum:    refPrimaryChecksumXML{Type: "sha256", PkgID: "YES", Value: pkg.SHA256},
			Summary:     pkg.Summary,
			Description: pkg.Description,
			Packager:    pkg.Packager,
			URL:         pkg.URL,
			Time:        refPrimaryTimeXML{File: pkg.BuildTime, Build: pkg.BuildTime},
			Size:        refPrimarySizeXML{Package: pkg.PackageSize, Installed: pkg.InstalledSize, Archive: pkg.ArchiveSize},
			Location:    locationXML{Href: "Packages/" + url.PathEscape(fmt.Sprintf("%s-%s-%s.%s.rpm", pkg.Name, pkg.Version, pkg.Release, pkg.Arch))},
			Format: refPrimaryFormatXML{
				Xmlns:       "http://linux.duke.edu/metadata/rpm",
				License:     pkg.License,
				Vendor:      pkg.Vendor,
				Group:       pkg.Group,
				BuildHost:   pkg.BuildHost,
				SourceRPM:   pkg.SourceRPM,
				HeaderRange: refPrimaryHeaderRangeXML{Start: pkg.HeaderStart, End: pkg.HeaderEnd},
				Provides:    refDepsToPrimaryEntries(pkg.Provides, false),
				Requires:    refDepsToPrimaryEntries(refFilterRequires(&pkg), true),
				Conflicts:   refDepsToPrimaryEntries(pkg.Conflicts, false),
				Obsoletes:   refDepsToPrimaryEntries(pkg.Obsoletes, false),
				Suggests:    refDepsToPrimaryEntries(pkg.Suggests, false),
				Enhances:    refDepsToPrimaryEntries(pkg.Enhances, false),
				Recommends:  refDepsToPrimaryEntries(pkg.Recommends, false),
				Supplements: refDepsToPrimaryEntries(pkg.Supplements, false),
			},
		}
	}

	metadata := refPrimaryMetadata{
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

// refDepsToPrimaryEntries renders dependencies the way createrepo_c does. When
// isRequires is set, rpmlib(...) features are skipped and script-time
// requirements carry pre="1".
func refDepsToPrimaryEntries(deps []rpm.Dependency, isRequires bool) []refPrimaryEntryXML {
	if len(deps) == 0 {
		return nil
	}

	out := make([]refPrimaryEntryXML, 0, len(deps))

	for _, dep := range deps {
		if isRequires && dep.IsRPMLib() {
			continue
		}

		e := refDepToPrimaryEntry(dep)
		if isRequires && dep.IsPre() {
			e.Pre = "1"
		}

		out = append(out, e)
	}

	return out
}

func refDepToPrimaryEntry(dep rpm.Dependency) refPrimaryEntryXML {
	e := refPrimaryEntryXML{Name: dep.Name, Flags: rpm.SenseFlagsString(dep.Flags)}
	if e.Flags == "" {
		return e
	}

	e.Epoch = strconv.FormatUint(uint64(dep.Epoch), 10)
	e.Ver, e.Rel = rpm.SplitVersionRelease(dep.Version)

	return e
}

type refFilelistsPackageXML struct {
	XMLName xml.Name `xml:"package"`
	PkgID   string   `xml:"pkgid,attr"`
	Name    string   `xml:"name,attr"`
	Arch    string   `xml:"arch,attr"`
	Version refVersionRefXML
	Files   []refFilelistsFileXML
}

type refVersionRefXML struct {
	XMLName xml.Name `xml:"version"`
	Epoch   string   `xml:"epoch,attr,omitempty"`
	Ver     string   `xml:"ver,attr"`
	Rel     string   `xml:"rel,attr"`
}

type refFilelistsFileXML struct {
	XMLName xml.Name `xml:"file"`
	Path    string   `xml:",chardata"`
	Type    string   `xml:"type,attr,omitempty"`
}

type refFilelistsMetadata struct {
	XMLName  xml.Name                 `xml:"filelists"`
	Xmlns    string                   `xml:"xmlns,attr"`
	Packages int                      `xml:"packages,attr"`
	PkgList  []refFilelistsPackageXML `xml:"package"`
}

func refGenerateFilelistsXML(packages []rpm.Package) ([]byte, error) {
	pkgEntries := make([]refFilelistsPackageXML, len(packages))
	for i, pkg := range packages {
		epochStr := ""
		if pkg.Epoch > 0 {
			epochStr = strconv.FormatUint(uint64(pkg.Epoch), 10)
		}

		files := make([]refFilelistsFileXML, len(pkg.Files))
		for j, f := range pkg.Files {
			files[j] = refFilelistsFileXML{Path: f.Path}
		}

		if len(files) == 0 {
			files = nil
		}

		pkgEntries[i] = refFilelistsPackageXML{
			PkgID:   pkg.SHA256,
			Name:    pkg.Name,
			Arch:    pkg.Arch,
			Version: refVersionRefXML{Epoch: epochStr, Ver: pkg.Version, Rel: pkg.Release},
			Files:   files,
		}
	}

	metadata := refFilelistsMetadata{
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

type refOtherChangelogXML struct {
	XMLName xml.Name `xml:"changelog"`
	Author  string   `xml:"author,attr"`
	Date    int64    `xml:"date,attr"`
	Text    string   `xml:",chardata"`
}

type refOtherPackageXML struct {
	XMLName    xml.Name `xml:"package"`
	PkgID      string   `xml:"pkgid,attr"`
	Name       string   `xml:"name,attr"`
	Arch       string   `xml:"arch,attr"`
	Version    refVersionRefXML
	Changelogs []refOtherChangelogXML `xml:"changelog"`
}

type refOtherMetadata struct {
	XMLName  xml.Name             `xml:"otherdata"`
	Xmlns    string               `xml:"xmlns,attr"`
	Packages int                  `xml:"packages,attr"`
	PkgList  []refOtherPackageXML `xml:"package"`
}

func refGenerateOtherXML(packages []rpm.Package) ([]byte, error) {
	pkgEntries := make([]refOtherPackageXML, len(packages))
	for i, pkg := range packages {
		epochStr := ""
		if pkg.Epoch > 0 {
			epochStr = strconv.FormatUint(uint64(pkg.Epoch), 10)
		}

		changelogs := make([]refOtherChangelogXML, len(pkg.Changelogs))
		for j, cl := range pkg.Changelogs {
			changelogs[j] = refOtherChangelogXML{
				Author: cl.Author,
				Date:   cl.Time,
				Text:   cl.Description,
			}
		}

		pkgEntries[i] = refOtherPackageXML{
			PkgID:      pkg.SHA256,
			Name:       pkg.Name,
			Arch:       pkg.Arch,
			Version:    refVersionRefXML{Epoch: epochStr, Ver: pkg.Version, Rel: pkg.Release},
			Changelogs: changelogs,
		}
	}

	metadata := refOtherMetadata{
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

// BenchmarkReference* are the pre-streaming baselines for the
// BenchmarkGenerate*XML benchmarks.
func BenchmarkReference(b *testing.B) {
	gens := map[string]func([]rpm.Package) ([]byte, error){
		"primary":   func(p []rpm.Package) ([]byte, error) { return refGeneratePrimaryXML(p, "") },
		"filelists": refGenerateFilelistsXML,
		"other":     refGenerateOtherXML,
	}

	for name, gen := range gens {
		b.Run(name, func(b *testing.B) {
			benchEachCount(b, func(b *testing.B, pkgs []rpm.Package) {
				b.Helper()

				var size int

				for b.Loop() {
					out, err := gen(pkgs)
					if err != nil {
						b.Fatal(err)
					}

					size = len(out)
				}

				b.SetBytes(int64(size))
			})
		})
	}
}

// Reference requirement filter (pre-allocation-reduction version).

var refGlibcSymbolRe = regexp.MustCompile(`^(.*)\(GLIBC_(\d+(?:\.\d+)*)\)((?:\(64bit\))?)$`)

// refCompareDotted compares dotted numeric versions ("2.9" < "2.34").
func refCompareDotted(a, b string) int {
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

type refDepKey struct {
	name, flags, ver string
	epoch            uint32
	pre              bool
}

func refKeyOf(d rpm.Dependency) refDepKey {
	return refDepKey{d.Name, rpm.SenseFlagsString(d.Flags), d.Version, d.Epoch, d.IsPre()}
}

type refGlibcBest struct {
	idx int
	ver string
}

// refBestGlibcRequires finds, per library+arch, the requirement with the highest GLIBC_x symbol version.
func refBestGlibcRequires(reqs []rpm.Dependency) map[string]refGlibcBest {
	best := map[string]refGlibcBest{}

	for i, d := range reqs {
		if m := refGlibcSymbolRe.FindStringSubmatch(d.Name); m != nil && !d.Versioned() {
			key := m[1] + "|" + m[3]
			if b, ok := best[key]; !ok || refCompareDotted(m[2], b.ver) > 0 {
				best[key] = refGlibcBest{i, m[2]}
			}
		}
	}

	return best
}

type refGlibcAction int

const (
	refGlibcKeep refGlibcAction = iota
	refGlibcDrop
	refGlibcLast
)

// refDropGlibc decides how a requirement is treated by the GLIBC collapsing.
func refDropGlibc(d rpm.Dependency, idx int, best map[string]refGlibcBest) refGlibcAction {
	if d.Versioned() {
		return refGlibcKeep
	}

	if m := refGlibcSymbolRe.FindStringSubmatch(d.Name); m != nil {
		if best[m[1]+"|"+m[3]].idx != idx {
			return refGlibcDrop
		}

		// createrepo_c emits the surviving GLIBC requirement last.
		return refGlibcLast
	}

	if base, ok := strings.CutSuffix(d.Name, "()(64bit)"); ok {
		if _, has := best[base+"|(64bit)"]; has {
			return refGlibcDrop
		}
	} else if base, ok := strings.CutSuffix(d.Name, "()"); ok {
		if _, has := best[base+"|"]; has {
			return refGlibcDrop
		}
	}

	return refGlibcKeep
}

// refFilterRequires mirrors createrepo_c: duplicates are dropped, requirements the
// package satisfies itself (own provides or packaged files) are dropped unless
// they are script-time requirements, and versioned GLIBC symbol requirements
// collapse to the highest version per library.
func refFilterRequires(pkg *rpm.Package) []rpm.Dependency {
	provides := make(map[refDepKey]bool, len(pkg.Provides))
	provided := make(map[string]bool, len(pkg.Provides)+len(pkg.Files))

	for _, d := range pkg.Provides {
		k := refKeyOf(d)
		k.pre = false
		provides[k] = true
		provided[d.Name] = true
	}

	for _, f := range pkg.Files {
		provided[f.Path] = true
	}

	best := refBestGlibcRequires(pkg.Requires)

	var glibc []rpm.Dependency

	seen := make(map[refDepKey]bool, len(pkg.Requires))
	out := make([]rpm.Dependency, 0, len(pkg.Requires))

	for i, d := range pkg.Requires {
		k := refKeyOf(d)
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

		switch refDropGlibc(d, i, best) {
		case refGlibcDrop:
			continue
		case refGlibcLast:
			glibc = append(glibc, d)
			continue
		case refGlibcKeep:
		}

		out = append(out, d)
	}

	return append(out, glibc...)
}
