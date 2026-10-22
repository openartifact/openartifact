// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package presign

import (
	"path"
	"strings"
)

// IsProxyBlobPath reports whether relPath, a file of a proxy repository of the
// given format, is a package/binary download rather than repository metadata.
// Only the former may be redirected; metadata is always streamed so the server
// keeps control of it (and clients re-validate it with plain conditional GETs).
func IsProxyBlobPath(format, relPath string) bool {
	relPath = strings.TrimPrefix(relPath, "/")
	base := path.Base(relPath)

	switch format {
	case "maven":
		return !isMavenMetadata(relPath, base)
	case "deb":
		return strings.HasPrefix(relPath, "pool/")
	case "rpm":
		if relPath == "repodata" || strings.HasPrefix(relPath, "repodata/") || strings.Contains(relPath, "/repodata/") {
			return false
		}

		return strings.HasSuffix(base, ".rpm") || strings.HasSuffix(base, ".drpm")
	case "raw":
		return true
	case "pypi":
		// Distribution files only; the PEP 658 .metadata siblings and the
		// simple index pages are streamed.
		return strings.HasPrefix(relPath, "files/") && !strings.HasSuffix(base, ".metadata")
	default:
		return false
	}
}

func isMavenMetadata(relPath, base string) bool {
	if strings.HasPrefix(base, "maven-metadata.xml") || base == "archetype-catalog.xml" || strings.HasPrefix(relPath, ".index/") {
		return true
	}

	for _, suffix := range []string{".md5", ".sha1", ".sha256", ".sha512", ".asc"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}

	return false
}
