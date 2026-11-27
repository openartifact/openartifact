// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cran

import (
	"strings"
)

// Kind is the type of a repository request path.
type Kind int

const (
	// KindOther is a path a CRAN repository does not serve.
	KindOther Kind = iota
	// KindIndex is a tree's PACKAGES, PACKAGES.gz or PACKAGES.rds.
	KindIndex
	// KindPackage is a package file of a tree: <pkg>_<version>.tar.gz, .zip
	// or .tgz below the tree's directory.
	KindPackage
	// KindArchiveRoot is the listing of the source tree's Archive directory.
	KindArchiveRoot
	// KindArchiveDir is the listing of Archive/<pkg>/.
	KindArchiveDir
	// KindArchivePackage is Archive/<pkg>/<pkg>_<version>.tar.gz: an older
	// source package. Its canonical path is the source tree's.
	KindArchivePackage
)

// Variant is the encoding of a PACKAGES index.
type Variant int

const (
	// VariantText is the plain PACKAGES file.
	VariantText Variant = iota
	// VariantGzip is PACKAGES.gz.
	VariantGzip
	// VariantRDS is PACKAGES.rds, a gzip-compressed serialized R matrix.
	VariantRDS
)

// File is the file name of the variant.
func (v Variant) File() string {
	switch v {
	case VariantGzip:
		return "PACKAGES.gz"
	case VariantRDS:
		return "PACKAGES.rds"
	case VariantText:
	}

	return "PACKAGES"
}

// ContentType is the media type the variant is served with.
func (v Variant) ContentType() string {
	switch v {
	case VariantGzip:
		return "application/gzip"
	case VariantRDS:
		return "application/octet-stream"
	case VariantText:
	}

	return "text/plain; charset=utf-8"
}

// SourceTree is the directory of source packages.
const SourceTree = "src/contrib"

// ArchiveDir is the directory below SourceTree that holds older source
// versions, one subdirectory per package.
const ArchiveDir = "Archive"

// Tree is one package tree of a repository: the directory its PACKAGES index
// and its package files live in.
type Tree struct {
	// Dir is the tree's path relative to the repository root:
	// "src/contrib", "bin/windows/contrib/4.4", "bin/macosx/contrib/4.4" or
	// "bin/macosx/big-sur-arm64/contrib/4.4".
	Dir string
	// Binary is false for the source tree.
	Binary bool
	// Ext is the file extension of the packages of the tree: ".tar.gz",
	// ".zip" or ".tgz".
	Ext string
}

// Source is the source tree.
var Source = Tree{Dir: SourceTree, Ext: ".tar.gz"} //nolint:gochecknoglobals // constant value

// Path is a parsed repository request path.
type Path struct {
	Kind Kind
	Tree Tree
	// Variant is the encoding of a KindIndex path.
	Variant Variant
	// File is the package file name of KindPackage and KindArchivePackage.
	File string
	// Package is the package name of every kind but the index and the archive
	// root; Version is the version in File.
	Package, Version string
}

// Canonical is the path the package file is stored and listed under: the
// tree's directory plus the file name (never the Archive spelling).
func (p Path) Canonical() string {
	return p.Tree.Dir + "/" + p.File
}

// ParsePath classifies a path relative to the repository root. A leading slash
// is allowed. ok is false for a path that is not one a CRAN repository serves.
func ParsePath(rel string) (Path, bool) {
	rel = strings.TrimPrefix(rel, "/")

	if rel == SourceTree+"/"+ArchiveDir || rel == SourceTree+"/"+ArchiveDir+"/" {
		return Path{Kind: KindArchiveRoot, Tree: Source}, true
	}

	if rest, ok := strings.CutPrefix(rel, SourceTree+"/"+ArchiveDir+"/"); ok {
		return parseArchive(rest)
	}

	dir, last, found := cutLast(rel)
	if !found {
		return Path{}, false
	}

	tree, ok := ParseTree(dir)
	if !ok {
		return Path{}, false
	}

	for _, v := range []Variant{VariantText, VariantGzip, VariantRDS} {
		if last == v.File() {
			return Path{Kind: KindIndex, Tree: tree, Variant: v}, true
		}
	}

	pkg, version, ok := ParseFile(last, tree.Ext)
	if !ok {
		return Path{}, false
	}

	return Path{Kind: KindPackage, Tree: tree, File: last, Package: pkg, Version: version}, true
}

func parseArchive(rest string) (Path, bool) {
	pkg, file, hasFile := strings.Cut(rest, "/")
	if !ValidPackageName(pkg) {
		return Path{}, false
	}

	if !hasFile || file == "" {
		return Path{Kind: KindArchiveDir, Tree: Source, Package: pkg}, true
	}

	name, version, ok := ParseFile(file, Source.Ext)
	if !ok || name != pkg {
		return Path{}, false
	}

	return Path{Kind: KindArchivePackage, Tree: Source, File: file, Package: pkg, Version: version}, true
}

func cutLast(s string) (string, string, bool) {
	dir, last, found := strings.CutLast(s, "/")
	if !found {
		return "", s, false
	}

	return dir, last, true
}

// ParseTree recognises a tree directory.
func ParseTree(dir string) (Tree, bool) {
	if dir == SourceTree {
		return Source, true
	}

	parts := strings.Split(dir, "/")

	// bin/windows/contrib/<R x.y> or bin/macosx/[<flavour>/]contrib/<R x.y>
	if len(parts) < 4 || parts[0] != "bin" {
		return Tree{}, false
	}

	rver := parts[len(parts)-1]
	if !validRMinor(rver) || parts[len(parts)-2] != "contrib" {
		return Tree{}, false
	}

	switch mid := parts[1 : len(parts)-2]; {
	case len(mid) == 1 && mid[0] == "windows":
		return Tree{Dir: dir, Binary: true, Ext: ".zip"}, true
	case len(mid) == 1 && mid[0] == "macosx":
		return Tree{Dir: dir, Binary: true, Ext: ".tgz"}, true
	case len(mid) == 2 && mid[0] == "macosx" && validFlavour(mid[1]):
		return Tree{Dir: dir, Binary: true, Ext: ".tgz"}, true
	}

	return Tree{}, false
}

// ParseFile splits "<package>_<version><ext>".
func ParseFile(file, ext string) (string, string, bool) {
	stem, trimmed := strings.CutSuffix(file, ext)
	if !trimmed {
		return "", "", false
	}

	pkg, version, found := strings.Cut(stem, "_")
	if !found || !ValidPackageName(pkg) || !ValidVersion(version) {
		return "", "", false
	}

	return pkg, version, true
}

// MaxNameLen bounds a package name.
const MaxNameLen = 128

// ValidPackageName reports whether name is an R package name: ASCII letters,
// digits and dots, starting with a letter, ending with a letter or digit, at
// least two characters.
func ValidPackageName(name string) bool {
	if len(name) < 2 || len(name) > MaxNameLen {
		return false
	}

	for i := range len(name) {
		c := name[i]

		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		digit := c >= '0' && c <= '9'

		switch {
		case i == 0 && !letter:
			return false
		case i == len(name)-1 && c == '.':
			return false
		case !letter && !digit && c != '.':
			return false
		}
	}

	return true
}

func validRMinor(s string) bool {
	major, minor, ok := strings.Cut(s, ".")
	if !ok || len(major) == 0 || len(major) > 2 || len(minor) == 0 || len(minor) > 2 {
		return false
	}

	return allDigits(major) && allDigits(minor)
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return s != ""
}

func validFlavour(s string) bool {
	if s == "" || len(s) > 64 || s == "contrib" {
		return false
	}

	for i := range len(s) {
		c := s[i]

		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '-' || c == '_' || c == '.') && i > 0:
		default:
			return false
		}
	}

	return true
}
