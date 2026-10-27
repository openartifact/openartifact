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
//
//nolint:cyclop // one case per format
func IsProxyBlobPath(format, relPath string) bool {
	relPath = strings.TrimPrefix(relPath, "/")
	base := path.Base(relPath)

	switch format {
	case "maven":
		return !isMavenMetadata(relPath, base)
	case "deb":
		return strings.HasPrefix(relPath, "pool/")
	case "rpm":
		return isRPMBlob(relPath, base)
	case "pacman":
		return strings.Contains(base, ".pkg.tar") && !strings.HasSuffix(base, ".sig")
	case "npm":
		return strings.HasSuffix(base, ".tgz") && strings.Contains(relPath, "/-/")
	case "helm":
		// Chart archives only; index.yaml and .prov files are streamed.
		return strings.HasPrefix(relPath, "charts/") && !strings.HasSuffix(base, ".prov")
	case "cargo":
		return strings.HasPrefix(relPath, "api/v1/crates/") && base == "download"
	case "go":
		return strings.HasSuffix(base, ".zip") && strings.Contains(relPath, "/@v/")
	case "nuget", "rubygems":
		return isPackageArchive(format, relPath, base)
	case "conan", "conda":
		return isConanCondaBlob(format, relPath, base)
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

// isConanCondaBlob reports whether relPath is a package archive of a Conan
// revision (conan_package.tgz, conan_sources.tgz; lists, manifests and recipes
// are streamed) or a conda package file (repodata and channeldata are streamed).
func isConanCondaBlob(format, relPath, base string) bool {
	if format == "conda" {
		return strings.HasSuffix(base, ".conda") || strings.HasSuffix(base, ".tar.bz2")
	}

	return strings.Contains(relPath, "/files/") && isConanArchive(base)
}

// isPackageArchive is IsProxyBlobPath for nuget (.nupkg of the flat container)
// and rubygems (gems/<file>.gem).
func isPackageArchive(format, relPath, base string) bool {
	if format == "nuget" {
		return strings.HasPrefix(relPath, "v3/package/") && strings.HasSuffix(base, ".nupkg")
	}

	return strings.HasPrefix(relPath, "gems/") && strings.HasSuffix(base, ".gem")
}

func isConanArchive(base string) bool {
	for _, ext := range []string{".tgz", ".tar.gz", ".txz", ".tar.xz", ".tzst", ".tar.zst"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}

	return false
}

func isRPMBlob(relPath, base string) bool {
	if relPath == "repodata" || strings.HasPrefix(relPath, "repodata/") || strings.Contains(relPath, "/repodata/") {
		return false
	}

	return strings.HasSuffix(base, ".rpm") || strings.HasSuffix(base, ".drpm")
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
