// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package composer

import (
	"net/url"
	"strings"
)

// Kind is what a request path below a repository names.
type Kind int

// The request paths of a Composer repository.
const (
	// KindRoot is packages.json.
	KindRoot Kind = iota + 1
	// KindMetadata is a p2 document, p2/vendor/name.json or p2/vendor/name~dev.json.
	KindMetadata
	// KindList is list.json.
	KindList
	// KindSearch is search.json.
	KindSearch
	// KindUpload is packages/upload/vendor/name/version (hosted repositories).
	KindUpload
	// KindDist is dists/vendor/name/version/ref.zip.
	KindDist
)

// Path is a parsed request path. Name is "vendor/package"; the other fields are
// set per kind.
type Path struct {
	Kind Kind
	Name string
	// Dev is set on a p2 document of the "~dev" variant.
	Dev bool
	// Version is the version of an upload or a dist, unescaped.
	Version string
	// Ref is the content reference of a dist: the part of its file name before ".zip".
	Ref string
}

// unescapeAll unescapes every path segment, failing on invalid escapes.
func unescapeAll(parts []string) ([]string, bool) {
	out := make([]string, len(parts))

	for i, p := range parts {
		s, err := url.PathUnescape(p)
		if err != nil {
			return nil, false
		}

		out[i] = s
	}

	return out, true
}

// ParsePath maps the path below a repository to what it names; false when the
// path is none of the protocol's.
func ParsePath(rel string) (Path, bool) {
	rel = strings.TrimPrefix(rel, "/")

	switch rel {
	case "packages.json":
		return Path{Kind: KindRoot}, true
	case "list.json":
		return Path{Kind: KindList}, true
	case "search.json":
		return Path{Kind: KindSearch}, true
	}

	parts, ok := unescapeAll(strings.Split(rel, "/"))
	if !ok {
		return Path{}, false
	}

	switch {
	case len(parts) == 3 && parts[0] == "p2":
		pkg, dev, ok := MetadataName(parts[2])
		name := parts[1] + "/" + pkg

		return Path{Kind: KindMetadata, Name: name, Dev: dev}, ok && ValidName(name)
	case len(parts) == 5 && parts[0] == "packages" && parts[1] == "upload":
		name := parts[2] + "/" + parts[3]

		return Path{Kind: KindUpload, Name: name, Version: parts[4]}, ValidName(name) && parts[4] != ""
	case len(parts) >= distSegments && parts[0] == "dists":
		// A version with a "/" (a branch such as dev-feature/x) arrives as
		// several segments when the caller already unescaped the path.
		name := parts[1] + "/" + parts[2]
		version := strings.Join(parts[3:len(parts)-1], "/")
		ref, isZip := strings.CutSuffix(parts[len(parts)-1], ".zip")

		return Path{Kind: KindDist, Name: name, Version: version, Ref: ref}, isZip && ValidName(name) && version != ""
	}

	return Path{}, false
}

// DistPath is the path below the repository of the dist of one version: the
// coordinates and the content reference, so a URL never changes its bytes.
func DistPath(name, version, ref string) string {
	return "dists/" + name + "/" + url.PathEscape(version) + "/" + ref + ".zip"
}

// DistRef is the content reference of a dist URL built by DistPath (of this or
// another repository); false when the URL is not one.
func DistRef(distURL string) (string, bool) {
	u, err := url.Parse(distURL)
	if err != nil {
		return "", false
	}

	// The five trailing segments: dists/vendor/package/version/ref.zip.
	segs := strings.Split(u.EscapedPath(), "/")
	if len(segs) < distSegments {
		return "", false
	}

	p, ok := ParsePath(strings.Join(segs[len(segs)-distSegments:], "/"))
	if !ok || p.Kind != KindDist || p.Ref == "" {
		return "", false
	}

	return p.Ref, true
}

// distSegments is the number of path segments of a dist path.
const distSegments = 5
