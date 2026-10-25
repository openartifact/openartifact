// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package rpm

import (
	"encoding/xml"
	"fmt"
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

// glibcSymbol is a parsed `lib(GLIBC_x.y)[(64bit)]` requirement name.
type glibcSymbol struct {
	lib   string // everything before "(GLIBC_"
	ver   string // dotted numeric symbol version
	arch  bool   // trailing "(64bit)"
	valid bool
}

const (
	glibcMarker = "(GLIBC_"
	arch64Tag   = "(64bit)"
)

// parseGlibcSymbol is the hand-written equivalent of
// ^(.*)\(GLIBC_(\d+(?:\.\d+)*)\)((?:\(64bit\))?)$ (the regexp allocated a
// submatch slice per call, ~50 times per package).
func parseGlibcSymbol(name string) glibcSymbol {
	rest, arch := strings.CutSuffix(name, arch64Tag)

	rest, ok := strings.CutSuffix(rest, ")")
	if !ok {
		return glibcSymbol{}
	}

	i := strings.LastIndex(rest, glibcMarker)
	if i < 0 {
		return glibcSymbol{}
	}

	lib, ver := rest[:i], rest[i+len(glibcMarker):]
	if strings.IndexByte(lib, '\n') >= 0 || !isDottedNumber(ver) {
		return glibcSymbol{}
	}

	return glibcSymbol{lib: lib, ver: ver, arch: arch, valid: true}
}

// isDottedNumber reports whether s matches \d+(\.\d+)*.
func isDottedNumber(s string) bool {
	prevDigit := false

	for i := range len(s) {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			prevDigit = true
		case c == '.' && prevDigit:
			prevDigit = false
		default:
			return false
		}
	}

	return prevDigit
}

// compareDotted compares dotted numeric versions ("2.9" < "2.34").
func compareDotted(a, b string) int {
	for a != "" || b != "" {
		var x, y int

		var as, bs string

		as, a, _ = strings.Cut(a, ".")
		bs, b, _ = strings.Cut(b, ".")

		x, _ = strconv.Atoi(as)
		y, _ = strconv.Atoi(bs)

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

// glibcLib identifies a library+arch pair for GLIBC collapsing.
type glibcLib struct {
	lib  string
	arch bool
}

type glibcBest struct {
	idx int
	ver string
}

// bestGlibcRequires finds, per library+arch, the requirement with the highest GLIBC_x symbol version.
func bestGlibcRequires(best map[glibcLib]glibcBest, reqs []rpm.Dependency) {
	for i, d := range reqs {
		if d.Versioned() {
			continue
		}

		if g := parseGlibcSymbol(d.Name); g.valid {
			key := glibcLib{g.lib, g.arch}
			if b, ok := best[key]; !ok || compareDotted(g.ver, b.ver) > 0 {
				best[key] = glibcBest{i, g.ver}
			}
		}
	}
}

type glibcAction int

const (
	glibcKeep glibcAction = iota
	glibcDrop
	glibcLast
)

// dropGlibc decides how a requirement is treated by the GLIBC collapsing.
func dropGlibc(d rpm.Dependency, idx int, best map[glibcLib]glibcBest) glibcAction {
	if d.Versioned() {
		return glibcKeep
	}

	if g := parseGlibcSymbol(d.Name); g.valid {
		if best[glibcLib{g.lib, g.arch}].idx != idx {
			return glibcDrop
		}

		// createrepo_c emits the surviving GLIBC requirement last.
		return glibcLast
	}

	if base, ok := strings.CutSuffix(d.Name, "()(64bit)"); ok {
		if _, has := best[glibcLib{base, true}]; has {
			return glibcDrop
		}
	} else if base, ok := strings.CutSuffix(d.Name, "()"); ok {
		if _, has := best[glibcLib{base, false}]; has {
			return glibcDrop
		}
	}

	return glibcKeep
}

// requiresFilter applies the createrepo_c requirement filtering and keeps its
// scratch maps and slices between packages, so repeated use (one filter per
// metadata run) does not allocate per package. The slice returned by filter is
// only valid until the next call.
type requiresFilter struct {
	provides map[depKey]bool
	provided map[string]bool
	seen     map[depKey]bool
	best     map[glibcLib]glibcBest
	out      []rpm.Dependency
	glibc    []rpm.Dependency
}

func newRequiresFilter() *requiresFilter {
	return &requiresFilter{
		provides: make(map[depKey]bool),
		provided: make(map[string]bool),
		seen:     make(map[depKey]bool),
		best:     make(map[glibcLib]glibcBest),
	}
}

func (f *requiresFilter) filter(pkg *rpm.Package) []rpm.Dependency {
	clear(f.provides)
	clear(f.provided)
	clear(f.seen)
	clear(f.best)

	for _, d := range pkg.Provides {
		k := keyOf(d)
		k.pre = false
		f.provides[k] = true
		f.provided[d.Name] = true
	}

	for i := range pkg.Files {
		f.provided[pkg.Files[i].Path] = true
	}

	bestGlibcRequires(f.best, pkg.Requires)

	f.out = f.out[:0]
	f.glibc = f.glibc[:0]

	for i, d := range pkg.Requires {
		k := keyOf(d)
		if f.seen[k] {
			continue
		}

		f.seen[k] = true

		if !d.IsPre() && !d.IsRPMLib() {
			versioned := k.flags != ""
			if (versioned && f.provides[k]) || (!versioned && f.provided[d.Name]) {
				continue
			}
		}

		switch dropGlibc(d, i, f.best) {
		case glibcDrop:
			continue
		case glibcLast:
			f.glibc = append(f.glibc, d)
			continue
		case glibcKeep:
		}

		f.out = append(f.out, d)
	}

	return append(f.out, f.glibc...)
}
