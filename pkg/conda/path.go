// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conda

import "strings"

// Kind is the type of a channel request path.
type Kind int

const (
	// KindOther is a path a proxy or group does not serve (repodata.jlap,
	// sharded repodata, index.html, anything unknown).
	KindOther Kind = iota
	// KindRepodata is <subdir>/repodata.json or <subdir>/current_repodata.json,
	// plain or compressed (.zst, .bz2).
	KindRepodata
	// KindPackage is <subdir>/<name>-<version>-<build>.conda or .tar.bz2.
	KindPackage
	// KindChanneldata is channeldata.json at the channel root.
	KindChanneldata
)

// Compression is how a repodata file is encoded.
type Compression int

const (
	// CompNone is plain JSON.
	CompNone Compression = iota
	// CompZstd is a zstd stream (repodata.json.zst).
	CompZstd
	// CompBzip2 is a bzip2 stream (repodata.json.bz2).
	CompBzip2
)

// Suffix is the file name suffix of the compression, "" for plain JSON.
func (c Compression) Suffix() string {
	switch c {
	case CompNone:
		return ""
	case CompZstd:
		return ".zst"
	case CompBzip2:
		return ".bz2"
	}

	return ""
}

// ChanneldataFile is the channel-root document listing every package name.
const ChanneldataFile = "channeldata.json"

const (
	repodataBase        = "repodata.json"
	currentRepodataBase = "current_repodata.json"
)

// Path is a parsed channel request path.
type Path struct {
	Kind Kind
	// Subdir is the platform directory of KindRepodata and KindPackage paths.
	Subdir string
	// File is the file name below Subdir (the whole path for KindChanneldata).
	File string
	// Current reports a current_repodata.json (the latest versions only).
	Current bool
	// Comp is the encoding of a KindRepodata file.
	Comp Compression
}

// Key is the repository-relative path of the file.
func (p Path) Key() string {
	if p.Subdir == "" {
		return p.File
	}

	return p.Subdir + "/" + p.File
}

// RepodataFile is the file name of a repodata document.
func RepodataFile(current bool, comp Compression) string {
	if current {
		return currentRepodataBase + comp.Suffix()
	}

	return repodataBase + comp.Suffix()
}

// ParsePath classifies a request path relative to the channel root (a leading
// slash is allowed). ok is false when the path is not one a channel serves; a
// path that is shaped like a channel file but not one a proxy or group serves
// (repodata.jlap) is KindOther with ok true.
func ParsePath(rel string) (Path, bool) {
	rel = strings.TrimPrefix(rel, "/")

	if rel == ChanneldataFile {
		return Path{Kind: KindChanneldata, File: rel}, true
	}

	subdir, file, found := strings.Cut(rel, "/")
	if !found || !ValidSubdir(subdir) || file == "" || file == "." || file == ".." ||
		strings.Contains(file, "/") || !PlainSegment(file) {
		return Path{}, false
	}

	p := Path{Subdir: subdir, File: file}

	if current, comp, ok := parseRepodataFile(file); ok {
		p.Kind, p.Current, p.Comp = KindRepodata, current, comp
		return p, true
	}

	if _, ok := ParseFilename(file); ok {
		p.Kind = KindPackage
		return p, true
	}

	p.Kind = KindOther

	return p, true
}

func parseRepodataFile(file string) (bool, Compression, bool) {
	for _, c := range []Compression{CompNone, CompZstd, CompBzip2} {
		switch file {
		case repodataBase + c.Suffix():
			return false, c, true
		case currentRepodataBase + c.Suffix():
			return true, c, true
		}
	}

	return false, CompNone, false
}
