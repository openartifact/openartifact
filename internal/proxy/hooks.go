// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/registry"
)

// Hooks is everything the proxy decides per format. Each proxy-capable format
// has one value (CargoHooks, NPMHooks, ...), defined next to the format's code
// and registered with Register at startup; nothing is registered implicitly. A
// nil field is the default documented on it, which is what the formats with
// nothing to say there (apk, deb, ivy, maven, nix, pacman, raw, rpm, sbt)
// always had.
//
// A format that is not registered is served by the path-mirroring default with
// no immutable, metadata or package-extension rule, and is never redirected to
// the object store.
type Hooks struct {
	// Serve answers a GET/HEAD for rel, the path below the repository root,
	// for the formats that map upstream paths to their own cache keys and
	// verification (pipeline.go). Nil mirrors the upstream path by path
	// through resolve, with the generic quarantine, presign and stale
	// handling of serveResolved.
	Serve func(s *Service, w http.ResponseWriter, r *http.Request, repo *models.Repository, rel string)

	// Fetch downloads key from target, the upstream URL of key, and records
	// the cache entry, for the formats that verify a file against a published
	// digest or have a flow of their own (apk, ivy, nix, sbt). Nil is a plain
	// fetch of target.
	Fetch func(s *Service, ctx context.Context, repo *models.Repository, key, target string) (*models.ProxyCacheEntry, error)

	// Immutable reports whether the cached file at key never changes once
	// published (see Immutable). Nil means no key is: every file uses the
	// repository TTL.
	Immutable func(key string) bool

	// IsMetadata reports whether key names an index or metadata file
	// (small by nature, capped at the service's metadata limit) rather than a
	// package file. Nil means no key is.
	IsMetadata func(key string) bool

	// IsLargeMetadata reports whether key is a metadata file that outgrows the
	// common metadata limit (conda repodata); it is capped at the service's
	// separate large-index limit instead of IsMetadata's. Nil means none is.
	IsLargeMetadata func(key string) bool

	// PackageExts lists the file extensions that name a binary package for the
	// formats whose files are not checked against a digest: an upstream HTML
	// answer (captive portal, WAF challenge) for such a path is refused rather
	// than cached. Empty disables the check.
	PackageExts []string

	// CapTTL narrows the cache lifetime ttl (the repository TTL, or the
	// immutable one) of key, for files that must refresh sooner than the
	// repository default (pacman sync databases). Nil leaves ttl unchanged.
	CapTTL func(key string, ttl time.Duration) time.Duration

	// WriteEntry answers a cached file the format serves in its own way
	// (rubygems compact index, pacman database not-modified) and reports
	// whether it did; false falls through to the generic entry writer. It
	// runs after the quarantine and presign steps of serveResolved. Nil
	// always falls through.
	WriteEntry func(
		s *Service, w http.ResponseWriter, r *http.Request, repo *models.Repository, entry *models.ProxyCacheEntry, key string, body io.Reader,
	) bool

	// Coordinates derives the package and version a download of rel (the path
	// below the repository root) belongs to, in the names the curation rules
	// match (see internal/curation), for the formats that have no gate of their
	// own in Serve: Service.Serve refuses the download of a version the
	// repository's curation makes unavailable. ok is false for anything that is
	// not the file of one package version (metadata, indexes). Nil means the
	// format's downloads are not checked here (either it gates in Serve, or
	// curation does not apply to it).
	Coordinates func(rel string) (pkg, version string, ok bool)

	// Published answers the publish time the upstream states for version of pkg
	// from the format's CACHED metadata (never an upstream fetch), for the
	// availability explanation of the REST API; ok is false when the metadata is
	// not cached or does not date the version. Nil means the format has no
	// publish time (see Service.PublishTime).
	Published func(s *Service, ctx context.Context, repo *models.Repository, pkg, version string) (time.Time, bool)

	// BlobPath reports whether rel, a file below the repository root, is a
	// package download that may be redirected to the object store rather than
	// metadata (see presign.Register). Register hands it to presign. Nil means
	// nothing is redirected.
	BlobPath func(rel string) bool
}

// hooks holds the Hooks of every registered proxy format.
var hooks registry.Registry[Hooks] //nolint:gochecknoglobals // the format registry, filled by Register at startup

// Register records the proxy hooks of format, including its presign
// classifier. It panics when format is registered twice.
func Register(format string, h Hooks) {
	hooks.Register(format, h)

	if h.BlobPath != nil {
		presign.Register(format, h.BlobPath)
	}
}

// Immutable reports whether the file at key never changes once published, so a
// cached copy never needs revalidation. Formats whose bodies are verified
// against a published digest (see fetchOpts) pin by name alone; the ones that
// have no such check here (deb, Maven, pacman, rpm) pin only under layouts whose file
// names are never reused, and everything else is cached for the repository TTL
// so a wrong copy is replaced on revalidation. The rule of each format is its
// Hooks.Immutable:
//   - apk: `.apk` files of a `<branch>/<repo>/<arch>/<file>` key; they are
//     only cached once their checksum matched the upstream index (apk.go).
//   - composer: dists of released versions (not branches), which are refused
//     when they differ from a non-empty shasum; the reference in the key names
//     the content (composer.go).
//   - deb: .deb/.udeb below a pool/ directory (the Debian archive layout, where
//     a file name is never reused); the same names in a flat repository, where
//     a publisher may overwrite a package, use the repository TTL.
//   - ivy, sbt: released files, not checksum or signature files, maven-metadata*.xml
//     or -SNAPSHOT files; a file is pinned only when its body matched the
//     checksum the upstream publishes (ivy.go), otherwise the repository TTL
//     applies.
//   - maven: released artifacts, not maven-metadata*.xml, archetype-catalog.xml
//     (both are regenerated in place) or -SNAPSHOT files; a checksum or
//     signature file (.md5 .sha1 .sha256 .sha512 .asc) is as immutable as the
//     file it belongs to.
//   - nix: nar/ files, cached only after their body matched the FileHash and
//     FileSize of their narinfo (nix.go); narinfo and nix-cache-info move.
//   - pacman: nothing. A mirror may republish a package file name, so package
//     files use the repository TTL.
//   - rpm: .rpm below a Packages/ directory (the layout of Fedora, CentOS, RHEL
//     and most vendor repositories, whose NEVRA-named files are not rebuilt in
//     place); flat or other layouts use the repository TTL. No cheap integrity
//     signal (the repodata checksums are not checked by this proxy) exists to
//     pin the rest.
//
// An unregistered format has no immutable file.
func Immutable(format, key string) bool {
	h, ok := hooks.Lookup(format)

	return ok && h.Immutable != nil && h.Immutable(key)
}

// isHTMLForPackage reports whether the upstream answered a package file's path
// with an HTML page: a captive portal, WAF challenge or proxy error page
// returned as 200. For the formats with Hooks.PackageExts nothing else would
// catch it, and caching it as the release would pin the page forever (and serve
// it to every client), so the body is refused instead.
func isHTMLForPackage(format, key string, resp *http.Response) bool {
	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if !strings.HasPrefix(ct, "text/html") && !strings.HasPrefix(ct, "application/xhtml+xml") {
		return false
	}

	h, _ := hooks.Lookup(format)

	return slices.ContainsFunc(h.PackageExts, func(ext string) bool { return strings.HasSuffix(key, ext) })
}

// keyName is the last path segment of a cache key.
func keyName(key string) string { return key[strings.LastIndexByte(key, '/')+1:] }
