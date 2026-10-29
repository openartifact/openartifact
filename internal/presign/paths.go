// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package presign

import (
	"path"
	"strings"

	"github.com/openartifact/openartifact/internal/registry"
)

// blobPaths holds, per proxy format, the BlobPath classifier it registered.
// Formats without an entry (apk, docker, ivy, sbt: served by their own handler
// or with no package/metadata split worth redirecting) are never redirected.
var blobPaths registry.Registry[func(rel string) bool] //nolint:gochecknoglobals // the format registry, filled by Register at startup

// Register records the classifier of format's proxy repositories: blobPath
// reports whether rel, a file path below the repository root without its
// leading slash, is a package/binary download (true) rather than repository
// metadata (false). It panics when format is registered twice.
func Register(format string, blobPath func(rel string) bool) {
	blobPaths.Register(format, blobPath)
}

// IsProxyBlobPath reports whether relPath, a file of a proxy repository of the
// given format, is a package/binary download rather than repository metadata.
// Only the former may be redirected; metadata is always streamed so the server
// keeps control of it (and clients re-validate it with plain conditional GETs).
// A format that registered no classifier is never redirected.
func IsProxyBlobPath(format, relPath string) bool {
	blobPath, ok := blobPaths.Lookup(format)
	if !ok {
		return false
	}

	return blobPath(strings.TrimPrefix(relPath, "/"))
}

// AnsibleBlobPath accepts collection tarballs only; the version documents are
// streamed.
func AnsibleBlobPath(rel string) bool {
	return strings.HasPrefix(rel, "api/v3/artifacts/collections/")
}

// CargoBlobPath accepts the crate download endpoint.
func CargoBlobPath(rel string) bool {
	return strings.HasPrefix(rel, "api/v1/crates/") && path.Base(rel) == "download"
}

// ComposerBlobPath accepts dists only; packages.json and the p2 documents are
// generated.
func ComposerBlobPath(rel string) bool { return strings.HasPrefix(rel, "dists/") }

// ConanBlobPath accepts the package archives of a Conan revision
// (conan_package.tgz, conan_sources.tgz); lists, manifests and recipes are
// streamed.
func ConanBlobPath(rel string) bool {
	return strings.Contains(rel, "/files/") && isConanArchive(path.Base(rel))
}

// CondaBlobPath accepts conda package files; repodata and channeldata are
// streamed.
func CondaBlobPath(rel string) bool {
	base := path.Base(rel)

	return strings.HasSuffix(base, ".conda") || strings.HasSuffix(base, ".tar.bz2")
}

// DebBlobPath accepts everything below pool/.
func DebBlobPath(rel string) bool { return strings.HasPrefix(rel, "pool/") }

// GoBlobPath accepts module zips.
func GoBlobPath(rel string) bool {
	return strings.HasSuffix(path.Base(rel), ".zip") && strings.Contains(rel, "/@v/")
}

// HelmBlobPath accepts chart archives only; index.yaml and .prov files are
// streamed.
func HelmBlobPath(rel string) bool {
	return strings.HasPrefix(rel, "charts/") && !strings.HasSuffix(path.Base(rel), ".prov")
}

// MavenBlobPath accepts everything but the repository metadata and the
// checksum and signature files next to it.
func MavenBlobPath(rel string) bool { return !isMavenMetadata(rel, path.Base(rel)) }

// NixBlobPath accepts NARs only; narinfo and nix-cache-info are streamed.
func NixBlobPath(rel string) bool { return strings.HasPrefix(rel, "nar/") }

// NPMBlobPath accepts tarballs (<pkg>/-/<file>.tgz).
func NPMBlobPath(rel string) bool {
	return strings.HasSuffix(path.Base(rel), ".tgz") && strings.Contains(rel, "/-/")
}

// NuGetBlobPath accepts the .nupkg files of the flat container.
func NuGetBlobPath(rel string) bool {
	return strings.HasPrefix(rel, "v3/package/") && strings.HasSuffix(path.Base(rel), ".nupkg")
}

// PacmanBlobPath accepts package files, not their signatures or the databases.
func PacmanBlobPath(rel string) bool {
	base := path.Base(rel)

	return strings.Contains(base, ".pkg.tar") && !strings.HasSuffix(base, ".sig")
}

// PyPIBlobPath accepts distribution files only; the PEP 658 .metadata siblings
// and the simple index pages are streamed.
func PyPIBlobPath(rel string) bool {
	return strings.HasPrefix(rel, "files/") && !strings.HasSuffix(path.Base(rel), ".metadata")
}

// RawBlobPath accepts every file: a raw repository has no metadata.
func RawBlobPath(string) bool { return true }

// RPMBlobPath accepts .rpm and .drpm files outside the repodata directory.
func RPMBlobPath(rel string) bool { return isRPMBlob(rel, path.Base(rel)) }

// RubyGemsBlobPath accepts gems/<file>.gem.
func RubyGemsBlobPath(rel string) bool {
	return strings.HasPrefix(rel, "gems/") && strings.HasSuffix(path.Base(rel), ".gem")
}

// TerraformBlobPath accepts module archives and provider zips; documents, the
// SHA256SUMS file and its signature are streamed.
func TerraformBlobPath(rel string) bool { return isTerraformArchive(rel, path.Base(rel)) }

// isTerraformArchive reports whether relPath is a module archive
// (modules/<name>/<system>/<version>/<file>) or a provider zip
// (providers/<type>/<version>/<os>_<arch>/<file>) of a Terraform proxy.
func isTerraformArchive(relPath, base string) bool {
	if !strings.HasPrefix(relPath, "modules/") && !strings.HasPrefix(relPath, "providers/") {
		return false
	}

	for _, ext := range []string{".zip", ".tar.gz", ".tgz", ".tar.bz2", ".tbz2", ".tar.xz", ".txz", ".tar"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}

	return false
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
