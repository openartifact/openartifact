// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package conan

import (
	"net/url"
	"strings"
)

// V1Kind is the endpoint a Conan 1 read path addresses.
type V1Kind int

const (
	// V1Other is a path that is no Conan 1 read endpoint.
	V1Other V1Kind = iota
	// V1Search is v1/conans/search.
	V1Search
	// V1Snapshot is v1/conans/{ref}: file name to MD5 of the newest revision.
	V1Snapshot
	// V1PackageSearch is v1/conans/{ref}/search.
	V1PackageSearch
	// V1DownloadURLs is v1/conans/{ref}/download_urls.
	V1DownloadURLs
	// V1Digest is v1/conans/{ref}/digest.
	V1Digest
	// V1PackageSnapshot is v1/conans/{ref}/packages/{pid}.
	V1PackageSnapshot
	// V1PackageDownloadURLs is v1/conans/{ref}/packages/{pid}/download_urls.
	V1PackageDownloadURLs
	// V1PackageDigest is v1/conans/{ref}/packages/{pid}/digest.
	V1PackageDigest
	// V1File is v1/files/{ref}/{rrev}/export/{file}: a file of a recipe revision.
	V1File
	// V1PackageFile is v1/files/{ref}/{rrev}/package/{pid}/{prev}/{file}.
	V1PackageFile
)

const (
	v1ConansPrefix = "v1/conans/"
	v1FilesPrefix  = "v1/files/"
)

// V1Path is a parsed Conan 1 read path. RRev, PID and PRev are set as far as
// the path names them; File is the file path of V1File and V1PackageFile.
type V1Path struct {
	Kind V1Kind
	Ref  Ref
	RRev string
	PID  string
	PRev string
	File string
}

// ParseV1Path classifies a path below the repository root, without a query,
// with the guarantees of ParsePath: only well-formed Conan 1 read paths are
// accepted, and every component is a plain name.
func ParseV1Path(rel string) (V1Path, bool) {
	rel = strings.TrimPrefix(rel, "/")

	if rest, ok := strings.CutPrefix(rel, v1ConansPrefix); ok {
		return parseV1Conans(rest)
	}

	if rest, ok := strings.CutPrefix(rel, v1FilesPrefix); ok {
		return parseV1Files(rest)
	}

	return V1Path{}, false
}

func parseV1Conans(rest string) (V1Path, bool) {
	if rest == "search" {
		return V1Path{Kind: V1Search}, true
	}

	segs := strings.Split(rest, "/")
	if len(segs) < 4 {
		return V1Path{}, false
	}

	p := V1Path{Ref: Ref{Name: segs[0], Version: segs[1], User: segs[2], Channel: segs[3]}}

	for _, part := range segs[:4] {
		if !validRefPart(part) {
			return V1Path{}, false
		}
	}

	tail := segs[4:]

	switch {
	case len(tail) == 0:
		p.Kind = V1Snapshot
	case len(tail) == 1:
		kind, ok := map[string]V1Kind{"search": V1PackageSearch, "download_urls": V1DownloadURLs, "digest": V1Digest}[tail[0]]
		if !ok {
			return V1Path{}, false
		}

		p.Kind = kind
	case tail[0] == "packages" && ValidRevision(tail[1]):
		p.PID = tail[1]

		kind, ok := v1PackageKind(tail[2:])
		if !ok {
			return V1Path{}, false
		}

		p.Kind = kind
	default:
		return V1Path{}, false
	}

	return p, true
}

// v1PackageKind is the endpoint of what follows packages/{pid}.
func v1PackageKind(rest []string) (V1Kind, bool) {
	switch {
	case len(rest) == 0:
		return V1PackageSnapshot, true
	case len(rest) == 1 && rest[0] == "download_urls":
		return V1PackageDownloadURLs, true
	case len(rest) == 1 && rest[0] == "digest":
		return V1PackageDigest, true
	}

	return V1Other, false
}

func parseV1Files(rest string) (V1Path, bool) {
	segs := strings.Split(rest, "/")
	if len(segs) < 7 {
		return V1Path{}, false
	}

	p := V1Path{Ref: Ref{Name: segs[0], Version: segs[1], User: segs[2], Channel: segs[3]}, RRev: segs[4]}

	for _, part := range segs[:4] {
		if !validRefPart(part) {
			return V1Path{}, false
		}
	}

	if !ValidRevision(p.RRev) {
		return V1Path{}, false
	}

	var file []string

	switch {
	case segs[5] == "export":
		p.Kind, file = V1File, segs[6:]
	case segs[5] == "package" && len(segs) >= 8 && ValidRevision(segs[6]) && ValidRevision(segs[7]):
		p.Kind, p.PID, p.PRev, file = V1PackageFile, segs[6], segs[7], segs[8:]
	default:
		return V1Path{}, false
	}

	p.File = strings.Join(file, "/")
	if p.File == "" || !ValidFilename(p.File) {
		return V1Path{}, false
	}

	return p, true
}

// recipePath is the shared prefix of the recipe endpoints: name/version/user/channel.
func (r Ref) recipePath() string {
	return r.Name + "/" + r.Version + "/" + r.User + "/" + r.Channel
}

// String is the canonical read path of p, without a leading slash.
func (p V1Path) String() string {
	switch p.Kind {
	case V1Search:
		return v1ConansPrefix + "search"
	case V1Snapshot:
		return v1ConansPrefix + p.Ref.recipePath()
	case V1PackageSearch:
		return v1ConansPrefix + p.Ref.recipePath() + "/search"
	case V1DownloadURLs:
		return v1ConansPrefix + p.Ref.recipePath() + "/download_urls"
	case V1Digest:
		return v1ConansPrefix + p.Ref.recipePath() + "/digest"
	case V1PackageSnapshot:
		return v1ConansPrefix + p.Ref.recipePath() + "/packages/" + p.PID
	case V1PackageDownloadURLs:
		return v1ConansPrefix + p.Ref.recipePath() + "/packages/" + p.PID + "/download_urls"
	case V1PackageDigest:
		return v1ConansPrefix + p.Ref.recipePath() + "/packages/" + p.PID + "/digest"
	case V1File:
		return v1FilesPrefix + p.Ref.recipePath() + "/" + p.RRev + "/export/" + p.File
	case V1PackageFile:
		return v1FilesPrefix + p.Ref.recipePath() + "/" + p.RRev + "/package/" + p.PID + "/" + p.PRev + "/" + p.File
	case V1Other:
	}

	return ""
}

// V2File is the Conan 2 path of the file a V1File or V1PackageFile addresses.
func (p V1Path) V2File() (Path, bool) {
	switch p.Kind {
	case V1File:
		return Path{Kind: KindFile, Ref: p.Ref, RRev: p.RRev, File: p.File}, true
	case V1PackageFile:
		return Path{Kind: KindFile, Ref: p.Ref, RRev: p.RRev, PID: p.PID, PRev: p.PRev, File: p.File}, true
	case V1Other, V1Search, V1Snapshot, V1PackageSearch, V1DownloadURLs, V1Digest,
		V1PackageSnapshot, V1PackageDownloadURLs, V1PackageDigest:
	}

	return Path{}, false
}

// V1FileURL is the URL below base (the repository's URL, no trailing slash) of
// a file of a revision: the recipe's when pid is empty, else the package's.
func V1FileURL(base string, ref Ref, rrev, pid, prev, file string) string {
	segs := strings.Split(file, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}

	p := V1Path{Kind: V1File, Ref: ref, RRev: rrev, File: strings.Join(segs, "/")}
	if pid != "" {
		p.Kind, p.PID, p.PRev = V1PackageFile, pid, prev
	}

	return strings.TrimRight(base, "/") + "/" + p.String()
}

// RepoURL is the URL of repository repo of the server at origin, which a client
// is configured with.
func RepoURL(origin, repo string) string {
	return strings.TrimRight(origin, "/") + "/repos/" + url.PathEscape(repo)
}

// NameOf is the package name a Conan 1 or 2 read path addresses, "" when it
// names none (the searches, paths that are not read paths).
func NameOf(rel string) string {
	if p, ok := ParsePath(rel); ok {
		return p.Ref.Name
	}

	if p, ok := ParseV1Path(rel); ok {
		return p.Ref.Name
	}

	return ""
}
