// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"errors"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/artifactpath"
	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/ocipath"
	pkgapk "github.com/openartifact/openartifact/pkg/apk"
	pkgconan "github.com/openartifact/openartifact/pkg/conan"
)

// Curation on download for the formats without a gate of their own.
//
// The formats that render their metadata (npm, pypi, ...) gate in their own
// Serve. The ones below mirror the upstream byte for byte: their package files
// are refused here, before the cache is read (a rule added after a file was
// cached applies at once), and the indexes that list those packages are served
// as the upstream has them. A blocked package therefore stays listed in apt,
// dnf, apk and pacman indexes but its file answers 403 with the reason. Only
// files of exactly one package version are gated: indexes, metadata, checksum
// and signature files pass untouched. Each parser below names what it gates.

// curationRefusal refuses the download of rel when the repository's curation
// makes the version it belongs to unavailable. It reports whether it answered.
func (s *Service) curationRefusal(w http.ResponseWriter, r *http.Request, repo *models.Repository, h Hooks, rel string) bool {
	if h.Coordinates == nil || repo.CurationMode == "" || repo.CurationMode == curation.ModeOff {
		return false
	}

	key, ok := CleanPath(rel)
	if !ok {
		return false
	}

	pkg, version, ok := h.Coordinates(key)
	if !ok {
		return false
	}

	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err == nil {
		err = checkGate(g, pkg, version, time.Time{})
	}

	if err == nil {
		return false
	}

	s.writeResolveError(w, r, err)

	return true
}

// ociCurationRefusal is curationRefusal for a docker manifest request: the
// package is the image name and the version the tag or digest as requested.
func (s *Service) ociCurationRefusal(w http.ResponseWriter, r *http.Request, repo *models.Repository, image, ref string) bool {
	if repo.CurationMode == "" || repo.CurationMode == curation.ModeOff {
		return false
	}

	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err == nil {
		err = checkGate(g, image, ref, time.Time{})
	}

	if err == nil {
		return false
	}

	if ge, ok := errors.AsType[*gateError](err); ok {
		ocipath.WriteError(w, http.StatusForbidden, "DENIED", ge.hold.Detail)
		return true
	}

	s.writeOCIResolveError(w, ocipath.KindManifest, err)

	return true
}

// isChecksumFile reports whether name is a checksum or signature sidecar.
func isChecksumFile(name string) bool {
	for _, ext := range ivySidecarExts {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}

	return false
}

// mavenDownloadCoordinates is the Maven coordinates ("group:artifact" and the
// version directory) of every file of a version directory named
// "<artifact>-<version>...", whatever its extension (.jar, .pom, .module,
// -bin.tar.gz, ...). Checksums, signatures and maven-metadata*.xml are not
// files of a version and pass.
func mavenDownloadCoordinates(key string) (string, string, bool) {
	if pkg, version, ok := mavenCoordinates(key); ok {
		return pkg, version, true
	}

	segs := strings.Split(key, "/")
	if len(segs) < 4 {
		return "", "", false
	}

	n := len(segs)
	file, version, artifact := segs[n-1], segs[n-2], segs[n-3]

	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return "", "", false
		}
	}

	rest, found := strings.CutPrefix(file, artifact+"-")
	if !found || isChecksumFile(file) || strings.HasPrefix(file, "maven-metadata") || !mavenFileVersion(rest, version) {
		return "", "", false
	}

	return strings.Join(segs[:n-3], ".") + ":" + artifact, version, true
}

var mavenSnapshotPrefix = regexp.MustCompile(`^\d{8}\.\d{6}-\d+[-.]`)

// mavenFileVersion reports whether rest (a file name below "<artifact>-")
// begins with version, or in a SNAPSHOT directory with a timestamped build of
// it, followed by a classifier or an extension.
func mavenFileVersion(rest, version string) bool {
	if tail, ok := strings.CutPrefix(rest, version); ok && tail != "" && (tail[0] == '-' || tail[0] == '.') {
		return true
	}

	base, snapshot := strings.CutSuffix(version, "-SNAPSHOT")
	if !snapshot {
		return false
	}

	tail, ok := strings.CutPrefix(rest, base+"-")

	return ok && mavenSnapshotPrefix.MatchString(tail)
}

// ivyDownloadCoordinates reads key like the hosted ivy and sbt repositories do
// (artifactpath.Split) and names the package "group:module", as for Maven. Every
// file of the version except checksums, signatures and maven-metadata*.xml is
// gated, ivy.xml and the POM included.
func ivyDownloadCoordinates(key string) (string, string, bool) {
	cur, _, ok := artifactpath.Split(key)
	if !ok || isIvySidecar(key) || isIvyMetadata(key) {
		return "", "", false
	}

	return cur.Group + ":" + cur.Module, cur.Version, true
}

// debCoordinates parses the "<name>_<version>_<arch>.deb" (or .udeb) file of a
// pool directory or a flat repository; the version is the file name's, which
// leaves out the epoch. Sources (.dsc, tarballs) and the indexes under dists/
// are not gated.
func debCoordinates(key string) (string, string, bool) {
	stem, found := strings.CutSuffix(path.Base(key), ".deb")
	if !found {
		stem, found = strings.CutSuffix(path.Base(key), ".udeb")
	}

	if !found || inDir(key, "dists") {
		return "", "", false
	}

	parts := strings.Split(stem, "_")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}

	return parts[0], parts[1], true
}

// rpmCoordinates parses "<name>-<version>-<release>.<arch>.rpm" (source RPMs
// included); the version is "<version>-<release>" and, as in file names, has no
// epoch. Delta RPMs (.drpm) and everything under repodata/ are not gated.
func rpmCoordinates(key string) (string, string, bool) {
	stem, found := strings.CutSuffix(path.Base(key), ".rpm")
	if !found || inDir(key, "repodata") {
		return "", "", false
	}

	dot := strings.LastIndexByte(stem, '.')
	if dot < 1 {
		return "", "", false
	}

	nameVersion, release, found := cutLastDash(stem[:dot])
	if !found {
		return "", "", false
	}

	name, version, found := cutLastDash(nameVersion)
	if !found || name == "" || version == "" || release == "" {
		return "", "", false
	}

	return name, version + "-" + release, true
}

// apkCoordinates parses the package files of a "<branch>/<repo>/<arch>"
// directory: "<name>-<version>-r<N>.apk" gives the name and "<version>-r<N>".
// APKINDEX files are not gated.
func apkCoordinates(key string) (string, string, bool) {
	if !apkPackageKey(key) {
		return "", "", false
	}

	return pkgapk.ParseFileName(keyName(key))
}

// pacmanCoordinates parses "<name>-<version>-<rel>-<arch>.pkg.tar.<ext>" with
// any compression (the epoch is a colon in the file name, as the mirror shows
// it); the version is "<version>-<rel>". Signatures (.sig) and the sync
// databases are not gated.
func pacmanCoordinates(key string) (string, string, bool) {
	stem, ext, found := strings.Cut(keyName(key), ".pkg.tar.")
	if !found || stem == "" || ext == "" || strings.Contains(ext, ".") {
		return "", "", false
	}

	rest, _, found := cutLastDash(stem)
	if !found {
		return "", "", false
	}

	rest, rel, found := cutLastDash(rest)
	if !found {
		return "", "", false
	}

	name, ver, found := cutLastDash(rest)
	if !found || name == "" || ver == "" || rel == "" {
		return "", "", false
	}

	return name, ver + "-" + rel, true
}

// conanCoordinates names the recipe of a Conan 2 path (its revisions, file
// lists and files): package = recipe name, version = recipe version. Searches
// and the Conan 1 endpoints are not gated.
func conanCoordinates(key string) (string, string, bool) {
	p, ok := pkgconan.ParsePath(key)
	if !ok || p.Ref.Name == "" || p.Ref.Version == "" {
		return "", "", false
	}

	return p.Ref.Name, p.Ref.Version, true
}

// cutLastDash splits s around its last "-".
func cutLastDash(s string) (string, string, bool) {
	i := strings.LastIndexByte(s, '-') //nolint:modernize // CutLast unavailable in supported toolchains
	if i < 0 {
		return s, "", false
	}

	return s[:i], s[i+1:], true
}
