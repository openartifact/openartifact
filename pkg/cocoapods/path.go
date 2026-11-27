// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package cocoapods is the grammar of a CocoaPods CDN spec repository
// (https://cdn.cocoapods.org/ and every repository a `pod repo add-cdn` or a
// Podfile `source 'https://...'` line names): the request paths, the
// CocoaPods-version.yml document, the all_pods_versions_X_Y_Z.txt shards that
// list the versions of the pods whose name hashes into them, and the podspec
// JSON documents, including the rewrite that makes a podspec's source archive
// download through a proxy repository. The proxy repositories of the cocoapods
// format are built on it.
package cocoapods

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/openartifact/openartifact/internal/lazyre"
	"github.com/openartifact/openartifact/internal/pathseg"
)

// The fixed documents of a CDN repository.
const (
	// VersionFile is the repository's own description: the CocoaPods versions it
	// supports and the prefix lengths its shards use.
	VersionFile = "CocoaPods-version.yml"
	// DeprecatedFile lists the podspecs the repository no longer carries.
	DeprecatedFile = "deprecated_podspecs.txt"
	// AllPodsFile lists the name of every pod the repository carries; `pod search`
	// and `pod spec cat` read it.
	AllPodsFile = "all_pods.txt"

	specsDir  = "Specs/"
	dlDir     = "dl/"
	shardBase = "all_pods_versions_"
	shardExt  = ".txt"
	specExt   = ".podspec.json"

	// MaxSegmentLen bounds a pod name, a version and a file name taken from a path.
	MaxSegmentLen = 255

	// maxDirs bounds the shard directories of a path.
	maxDirs = 8

	// RefLen is the length of the reference in a download path: hex digits of a digest of
	// where the source comes from.
	RefLen = 32
)

// Kind is what a request path below a repository names.
type Kind int

// The request paths of a CDN repository.
const (
	// KindVersionFile is CocoaPods-version.yml.
	KindVersionFile Kind = iota + 1
	// KindShard is all_pods_versions_X_Y_Z.txt.
	KindShard
	// KindDeprecated is deprecated_podspecs.txt.
	KindDeprecated
	// KindAllPods is all_pods.txt.
	KindAllPods
	// KindPodspec is Specs/X/Y/Z/Name/Version/Name.podspec.json.
	KindPodspec
	// KindDownload is dl/X/Y/Z/Name/Version/REF/FILE: the source archive of a
	// version, which only a proxy repository serves.
	KindDownload
)

// Path is a parsed request path. Which fields are set depends on the kind.
type Path struct {
	Kind Kind
	// Dir is the shard directory of a podspec or download, "X/Y/Z" ("" when the
	// repository does not shard).
	Dir string
	// Name and Version name the pod version of a podspec or download.
	Name    string
	Version string
	// Ref and File are the reference and the file name of a download.
	Ref  string
	File string
	// Shard is the file name of a shard.
	Shard string
}

var (
	hexRe       = lazyre.New(`^[0-9a-f]+$`)
	shardNameRe = lazyre.New(`^all_pods_versions(_[0-9a-f]{1,32})*\.txt$`)
)

// ValidShardName reports whether file is the name of a shard document.
func ValidShardName(file string) bool {
	return len(file) <= MaxSegmentLen && strings.HasPrefix(file, shardBase) && shardNameRe().MatchString(file)
}

// ValidName reports whether name can be the name of a pod in a path: one plain
// segment without control characters or whitespace other than a plain space (the
// trunk has a pod named "ID.me WebVerify"). The CocoaPods grammar is narrower; a
// repository that holds a name outside it still serves.
func ValidName(name string) bool { return validSegment(name) }

// ValidVersion reports whether version can be a pod version in a path.
func ValidVersion(version string) bool { return validSegment(version) }

func validSegment(s string) bool {
	if len(s) > MaxSegmentLen || !pathseg.Name(s) {
		return false
	}

	return !strings.ContainsFunc(s, badRune)
}

// ValidRef reports whether ref is the reference of a download path.
func ValidRef(ref string) bool { return len(ref) == RefLen && hexRe().MatchString(ref) }

// ParsePath maps the path below a repository, as a router hands it over (each
// segment may still be percent-encoded), to what it names; false when the path
// is none of the protocol's.
func ParsePath(rel string) (Path, bool) {
	rel = strings.TrimPrefix(rel, "/")

	switch rel {
	case VersionFile:
		return Path{Kind: KindVersionFile}, true
	case DeprecatedFile:
		return Path{Kind: KindDeprecated}, true
	case AllPodsFile:
		return Path{Kind: KindAllPods}, true
	}

	if strings.HasPrefix(rel, shardBase) {
		return Path{Kind: KindShard, Shard: rel}, ValidShardName(rel)
	}

	switch {
	case strings.HasPrefix(rel, specsDir):
		return parseSpecs(strings.TrimPrefix(rel, specsDir))
	case strings.HasPrefix(rel, dlDir):
		return parseDownload(strings.TrimPrefix(rel, dlDir))
	}

	return Path{}, false
}

// unescapeAll unescapes every segment, failing on an invalid escape.
func unescapeAll(rel string) ([]string, bool) {
	parts := strings.Split(rel, "/")

	for i, p := range parts {
		s, err := url.PathUnescape(p)
		if err != nil {
			return nil, false
		}

		parts[i] = s
	}

	return parts, true
}

// dirOf joins the shard directories; false when one is not hex.
func dirOf(parts []string) (string, bool) {
	if len(parts) > maxDirs {
		return "", false
	}

	for _, d := range parts {
		if len(d) > 32 || !hexRe().MatchString(d) {
			return "", false
		}
	}

	return strings.Join(parts, "/"), true
}

func parseSpecs(rest string) (Path, bool) {
	parts, ok := unescapeAll(rest)
	if !ok || len(parts) < 3 {
		return Path{}, false
	}

	n := len(parts)
	name, version, file := parts[n-3], parts[n-2], parts[n-1]

	dir, ok := dirOf(parts[:n-3])
	if !ok || !ValidName(name) || !ValidVersion(version) || file != name+specExt || len(file) > MaxSegmentLen {
		return Path{}, false
	}

	return Path{Kind: KindPodspec, Dir: dir, Name: name, Version: version}, true
}

func parseDownload(rest string) (Path, bool) {
	parts, ok := unescapeAll(rest)
	if !ok || len(parts) < 4 {
		return Path{}, false
	}

	n := len(parts)
	name, version, ref, file := parts[n-4], parts[n-3], parts[n-2], parts[n-1]

	dir, ok := dirOf(parts[:n-4])
	if !ok || !ValidName(name) || !ValidVersion(version) || !ValidRef(ref) || !validSegment(file) {
		return Path{}, false
	}

	return Path{Kind: KindDownload, Dir: dir, Name: name, Version: version, Ref: ref, File: file}, true
}

// SpecPath is the path of the podspec of one pod version below a repository
// (and below an upstream), the form a CDN serves it under.
func SpecPath(dir, name, version string) string {
	return specsDir + joinDir(dir) + name + "/" + version + "/" + name + specExt
}

// DownloadPath is the path below a proxy repository of the source archive of one
// pod version: the coordinates, the reference that names where the archive comes
// from and its file name; every segment is escaped.
func DownloadPath(dir, name, version, ref, file string) string {
	return dlDir + joinDir(dir) + url.PathEscape(name) + "/" + url.PathEscape(version) + "/" + ref + "/" + url.PathEscape(file)
}

// ArchiveKey is the cache key of a source archive: the download path without the
// shard directory, which the coordinates already determine.
func ArchiveKey(name, version, ref, file string) string {
	return dlDir + name + "/" + version + "/" + ref + "/" + file
}

// ArchiveKeyPrefix is the cache key prefix every archive of the pod starts with.
func ArchiveKeyPrefix(name string) string { return dlDir + name + "/" }

// ParseArchiveKey splits a cache key built by ArchiveKey; false for any other key.
func ParseArchiveKey(key string) (string, string, bool) {
	rest, found := strings.CutPrefix(key, dlDir)
	if !found {
		return "", "", false
	}

	parts := strings.Split(rest, "/")
	if len(parts) != 4 || !ValidName(parts[0]) || !ValidVersion(parts[1]) || !ValidRef(parts[2]) || !validSegment(parts[3]) {
		return "", "", false
	}

	return parts[0], parts[1], true
}

func joinDir(dir string) string {
	if dir == "" {
		return ""
	}

	return dir + "/"
}

// badRune reports whether r may not appear in a name or a version: control
// characters, the replacement character and whitespace other than ' '.
func badRune(r rune) bool {
	return unicode.IsControl(r) || r == unicode.ReplacementChar || (unicode.IsSpace(r) && r != ' ')
}
