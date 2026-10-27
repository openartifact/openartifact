// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package conan holds what the Conan v2 proxy and group repositories share:
// the read paths of the REST API, and the JSON documents (revision lists, file
// lists, search results) they verify, cache and merge.
package conan

import (
	"strings"
	"unicode/utf8"

	"github.com/openartifact/openartifact/internal/pathseg"
)

// Kind is the endpoint a Conan v2 read path addresses.
type Kind int

const (
	// KindOther is a path that is no Conan v2 read endpoint.
	KindOther Kind = iota
	// KindSearch is v2/conans/search: the recipes matching a pattern.
	KindSearch
	// KindRecipeLatest is .../latest of a recipe: its newest revision.
	KindRecipeLatest
	// KindRecipeRevisions is .../revisions of a recipe.
	KindRecipeRevisions
	// KindPackageSearch is the package search of a recipe (newest revision) or
	// of one recipe revision.
	KindPackageSearch
	// KindFiles is the file list of a recipe revision or package revision.
	KindFiles
	// KindFile is one file of a recipe revision or package revision.
	KindFile
	// KindPackageLatest is .../packages/{pid}/latest: the newest revision of a
	// package.
	KindPackageLatest
	// KindPackageRevisions is .../packages/{pid}/revisions.
	KindPackageRevisions
)

const (
	// SearchPath is the global recipe search.
	SearchPath = "v2/conans/search"

	conansPrefix = "v2/conans/"

	// maxRefPart bounds one reference component (name, version, user,
	// channel); maxRevisionLen the revisions and package ids; maxFileLen a file
	// path. They are the limits of the hosted handler's columns.
	maxRefPart     = 100
	maxRevisionLen = 64
	maxFileLen     = 512
)

// Ref is a recipe reference: name/version@user/channel, where "_" stands for an
// absent user or channel.
type Ref struct {
	Name, Version, User, Channel string
}

// Path is a parsed Conan v2 read path. RRev, PID and PRev are set as far as the
// path names them; File is the file path of KindFile.
type Path struct {
	Kind Kind
	Ref  Ref
	RRev string
	PID  string
	PRev string
	File string
}

// ParsePath classifies a path below the repository root, without a query. It
// accepts only well-formed Conan v2 read paths, so what it returns can be used
// to build an upstream URL, a cache key or a search pattern without further
// escaping: every component is a plain name of the characters Conan allows.
// ok is false for anything else, including the paths a Conan remote also
// serves but a read-only mirror does not (uploads, deletes).
func ParsePath(rel string) (Path, bool) {
	rel = strings.TrimPrefix(rel, "/")

	rest, ok := strings.CutPrefix(rel, conansPrefix)
	if !ok {
		return Path{}, false
	}

	if rest == "search" {
		return Path{Kind: KindSearch}, true
	}

	segs := strings.Split(rest, "/")
	if len(segs) < 5 {
		return Path{}, false
	}

	p := Path{Ref: Ref{Name: segs[0], Version: segs[1], User: segs[2], Channel: segs[3]}}

	for _, part := range segs[:4] {
		if !validRefPart(part) {
			return Path{}, false
		}
	}

	return parseRecipeTail(p, segs[4:])
}

// parseRecipeTail reads what follows name/version/user/channel.
func parseRecipeTail(p Path, tail []string) (Path, bool) {
	switch {
	case len(tail) == 1 && tail[0] == "latest":
		p.Kind = KindRecipeLatest
		return p, true
	case len(tail) == 1 && tail[0] == "revisions":
		p.Kind = KindRecipeRevisions
		return p, true
	case len(tail) == 1 && tail[0] == "search":
		p.Kind = KindPackageSearch
		return p, true
	case len(tail) >= 3 && tail[0] == "revisions" && ValidRevision(tail[1]):
		p.RRev = tail[1]
		return parseRevisionTail(p, tail[2:])
	}

	return Path{}, false
}

// parseRevisionTail reads what follows .../revisions/{rrev}.
func parseRevisionTail(p Path, tail []string) (Path, bool) {
	switch tail[0] {
	case "search":
		if len(tail) == 1 {
			p.Kind = KindPackageSearch
			return p, true
		}
	case "files":
		return parseFiles(p, tail[1:])
	case "packages":
		if len(tail) >= 3 && ValidRevision(tail[1]) {
			p.PID = tail[1]
			return parsePackageTail(p, tail[2:])
		}
	}

	return Path{}, false
}

// parsePackageTail reads what follows .../packages/{pid}.
func parsePackageTail(p Path, tail []string) (Path, bool) {
	switch {
	case len(tail) == 1 && tail[0] == "latest":
		p.Kind = KindPackageLatest
		return p, true
	case len(tail) == 1 && tail[0] == "revisions":
		p.Kind = KindPackageRevisions
		return p, true
	case len(tail) >= 3 && tail[0] == "revisions" && tail[2] == "files" && ValidRevision(tail[1]):
		p.PRev = tail[1]
		return parseFiles(p, tail[3:])
	}

	return Path{}, false
}

// parseFiles reads what follows .../files: nothing (the list) or a file path.
func parseFiles(p Path, file []string) (Path, bool) {
	if len(file) == 0 {
		p.Kind = KindFiles
		return p, true
	}

	name := strings.Join(file, "/")
	if !ValidFilename(name) {
		return Path{}, false
	}

	p.Kind, p.File = KindFile, name

	return p, true
}

// String is the canonical read path of p, without a leading slash.
func (p Path) String() string {
	if p.Kind == KindSearch {
		return SearchPath
	}

	var b strings.Builder

	b.WriteString(conansPrefix)
	b.WriteString(p.Ref.Name + "/" + p.Ref.Version + "/" + p.Ref.User + "/" + p.Ref.Channel)

	switch p.Kind {
	case KindRecipeLatest:
		b.WriteString("/latest")
	case KindRecipeRevisions:
		b.WriteString("/revisions")
	case KindPackageSearch:
		if p.RRev != "" {
			b.WriteString("/revisions/" + p.RRev)
		}

		b.WriteString("/search")
	case KindFiles, KindFile:
		b.WriteString("/revisions/" + p.RRev)

		if p.PID != "" {
			b.WriteString("/packages/" + p.PID + "/revisions/" + p.PRev)
		}

		b.WriteString("/files")

		if p.Kind == KindFile {
			b.WriteString("/" + p.File)
		}
	case KindPackageLatest:
		b.WriteString("/revisions/" + p.RRev + "/packages/" + p.PID + "/latest")
	case KindPackageRevisions:
		b.WriteString("/revisions/" + p.RRev + "/packages/" + p.PID + "/revisions")
	case KindSearch, KindOther:
	}

	return b.String()
}

// validRefPart reports whether s is a name, version, user or channel as Conan
// allows them: letters, digits, "_", "+", "." and "-", not starting with a
// symbol ("_" alone stands for an absent user or channel).
func validRefPart(s string) bool {
	if s == "" || len(s) > maxRefPart {
		return false
	}

	for i := range len(s) {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		case i > 0 && (c == '+' || c == '.' || c == '-'):
		default:
			return false
		}
	}

	return true
}

// ValidRevision reports whether s is a revision or package id: letters, digits,
// ".", "_" and "-" (Conan's are hex digests), and no directory name.
func ValidRevision(s string) bool {
	if len(s) > maxRevisionLen || !pathseg.Name(s) {
		return false
	}

	for i := range len(s) {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}

	return true
}

// ValidFilename reports whether name is a relative path of plain segments with
// no control characters, as Conan names its files (conanfile.py,
// metadata/sign/signature.json).
func ValidFilename(name string) bool {
	if len(name) > maxFileLen || !utf8.ValidString(name) || !pathseg.Path(name) {
		return false
	}

	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '?' || r == '#' || r == '%' {
			return false
		}
	}

	return true
}
