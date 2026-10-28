// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package ansible

import (
	"net/url"
	"strings"
)

// PathKind says which Galaxy API resource a repository path names.
type PathKind int

// The read-only resources of a Galaxy API v3 that proxy and group repositories
// serve.
const (
	// KindRoot is /api/, the discovery document.
	KindRoot PathKind = iota + 1
	// KindV3 is /api/v3/, the document listing the API's entry points.
	KindV3
	// KindIndex is /api/v3/collections/.
	KindIndex
	// KindCollection is /api/v3/collections/NAMESPACE/NAME/.
	KindCollection
	// KindVersions is /api/v3/collections/NAMESPACE/NAME/versions/.
	KindVersions
	// KindVersion is /api/v3/collections/NAMESPACE/NAME/versions/VERSION/.
	KindVersion
	// KindDownload is /api/v3/artifacts/collections/NAMESPACE-NAME-VERSION.tar.gz.
	KindDownload
)

// Path is a parsed repository path.
type Path struct {
	Kind      PathKind
	Namespace string
	Name      string
	// Version is set for KindVersion and KindDownload.
	Version string
}

// ParsePath classifies a repository path ("api/v3/collections/ns/name/"), with
// or without leading and trailing slashes and with percent-escapes decoded. A
// name, version or file name that is not valid, or a path that is not one of
// the Galaxy resources, is not ok.
func ParsePath(rel string) (Path, bool) {
	rel = strings.Trim(rel, "/")

	if rel == "api" {
		return Path{Kind: KindRoot}, true
	}

	rest, ok := strings.CutPrefix(rel, "api/v3")
	if !ok {
		return Path{}, false
	}

	rest = strings.Trim(rest, "/")
	if rest == "" {
		return Path{Kind: KindV3}, true
	}

	parts := strings.Split(rest, "/")
	for i, p := range parts {
		dec, err := url.PathUnescape(p)
		if err != nil || dec == "" || strings.ContainsAny(dec, "/\\") {
			return Path{}, false
		}

		parts[i] = dec
	}

	switch {
	case len(parts) == 1 && parts[0] == "collections":
		return Path{Kind: KindIndex}, true
	case len(parts) == 3 && parts[0] == "artifacts" && parts[1] == "collections":
		ns, name, version, ok := ParseFileName(parts[2])

		return Path{Kind: KindDownload, Namespace: ns, Name: name, Version: version}, ok
	case len(parts) >= 3 && len(parts) <= 5 && parts[0] == "collections":
		return parseCollectionPath(parts[1:])
	}

	return Path{}, false
}

// parseCollectionPath classifies what follows "collections/": NAMESPACE/NAME,
// then optionally "versions" and a version.
func parseCollectionPath(parts []string) (Path, bool) {
	p := Path{Kind: KindCollection, Namespace: parts[0], Name: parts[1]}
	if !ValidName(p.Namespace) || !ValidName(p.Name) {
		return Path{}, false
	}

	switch len(parts) {
	case 2:
		return p, true
	case 3:
		p.Kind = KindVersions

		return p, parts[2] == "versions"
	case 4:
		p.Kind, p.Version = KindVersion, parts[3]

		return p, parts[2] == "versions" && ValidVersion(p.Version)
	}

	return Path{}, false
}
