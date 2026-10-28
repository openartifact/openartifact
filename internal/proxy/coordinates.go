// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"path"
	"regexp"
	"strings"

	"golang.org/x/mod/module"
)

// coordinateParsers maps a proxy repository format to the parser recovering
// package coordinates from one of its cache keys. Formats absent from the map
// either cache no versioned package artifact under a key that names its
// version (docker, deb, rpm, apk, pacman, conda, conan, ivy, sbt, raw,
// terraform) or are not proxied at all.
var coordinateParsers = map[string]func(key string) (string, string, bool){ //nolint:gochecknoglobals // constant lookup table
	"npm":      npmCoordinates,
	"pypi":     pypiCoordinates,
	"maven":    mavenCoordinates,
	"cargo":    cargoCoordinates,
	"rubygems": rubygemsCoordinates,
	"nuget":    nugetCoordinates,
	"go":       goCoordinates,
	"helm":     helmCoordinates,
}

// PackageCoordinates recovers the package name and version from the cache key
// (the upstream-relative key a proxy repository stores a file under) of a
// cached package artifact, in the shapes OSV advisories use: npm "name" or
// "@scope/name", PyPI project name as in the key (callers normalise), Maven
// "group:artifact", crates.io/RubyGems/NuGet id, Go module path (case
// decoded) with its "v" version prefix, Helm chart name.
//
// ok is false for every key that is not a package artifact (indexes,
// packuments, simple pages, checksums, signatures, .info/.mod/list files,
// ...), for malformed keys and for formats whose keys carry no version.
func PackageCoordinates(format, cacheKey string) (string, string, bool) {
	parse, found := coordinateParsers[format]
	if !found || cacheKey == "" || strings.ContainsAny(cacheKey, "\x00\\") {
		return "", "", false
	}

	name, version, ok := parse(cacheKey)
	if !ok || name == "" || version == "" {
		return "", "", false
	}

	return name, version, true
}

// npmCoordinates parses "<pkg>/-/<unscoped>-<version>.tgz".
func npmCoordinates(key string) (string, string, bool) {
	pkg, file, found := strings.Cut(key, "/-/")
	if !found || pkg == "" || strings.Contains(file, "/") {
		return "", "", false
	}

	stem, found := strings.CutSuffix(file, ".tgz")
	if !found {
		return "", "", false
	}

	unscoped := path.Base(pkg)
	if scope, _, scoped := strings.Cut(pkg, "/"); scoped && (!strings.HasPrefix(scope, "@") || strings.Count(pkg, "/") != 1) {
		return "", "", false
	}

	version, found := strings.CutPrefix(stem, unscoped+"-")
	if !found {
		return "", "", false
	}

	return pkg, version, true
}

var pypiSdistExts = [...]string{".tar.gz", ".tar.bz2", ".tar.xz", ".zip", ".tgz"} //nolint:gochecknoglobals // constant table

// pypiNormalize is the PEP 503 project name normalisation.
func pypiNormalize(s string) string {
	return strings.ToLower(strings.NewReplacer("_", "-", ".", "-").Replace(s))
}

// pypiCoordinates parses "files/<project>/<filename>".
func pypiCoordinates(key string) (string, string, bool) {
	rest, found := strings.CutPrefix(key, "files/")
	if !found {
		return "", "", false
	}

	project, file, found := strings.Cut(rest, "/")
	if !found || project == "" || strings.Contains(file, "/") {
		return "", "", false
	}

	if stem, isWheel := strings.CutSuffix(file, ".whl"); isWheel {
		parts := strings.Split(stem, "-")
		if len(parts) < 5 || pypiNormalize(parts[0]) != pypiNormalize(project) {
			return "", "", false
		}

		return project, parts[1], true
	}

	for _, ext := range pypiSdistExts {
		stem, isSdist := strings.CutSuffix(file, ext)
		if !isSdist {
			continue
		}

		// The project name may itself contain dashes: find the dash after
		// which the rest is the version.
		want := pypiNormalize(project)

		for i, c := range stem {
			if c == '-' && i+1 < len(stem) && pypiNormalize(stem[:i]) == want {
				return project, stem[i+1:], true
			}
		}
	}

	return "", "", false
}

var (
	mavenExts     = [...]string{".jar", ".war", ".aar", ".pom", ".zip"} //nolint:gochecknoglobals // constant table
	mavenSnapshot = regexp.MustCompile(`^\d{8}\.\d{6}-\d+$`)
)

// mavenCoordinates parses "<group path>/<artifact>/<version>/<artifact>-<version>[-<classifier>].<ext>".
func mavenCoordinates(key string) (string, string, bool) {
	segs := strings.Split(key, "/")
	if len(segs) < 4 {
		return "", "", false
	}

	n := len(segs)
	file, version, artifact := segs[n-1], segs[n-2], segs[n-3]

	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return "", "", false
		}
	}

	ext := ""

	for _, e := range mavenExts {
		if strings.HasSuffix(file, e) {
			ext = e
			break
		}
	}

	if ext == "" {
		return "", "", false
	}

	rest, found := strings.CutPrefix(strings.TrimSuffix(file, ext), artifact+"-")
	if !found || !mavenVersionMatches(rest, version) {
		return "", "", false
	}

	return strings.Join(segs[:n-3], ".") + ":" + artifact, version, true
}

// mavenVersionMatches reports whether rest (the file name without artifact
// prefix and extension) starts with the directory's version, or, in a
// SNAPSHOT directory, with a timestamped build of it, followed by an optional
// "-classifier".
func mavenVersionMatches(rest, version string) bool {
	if tail, ok := strings.CutPrefix(rest, version); ok && (tail == "" || tail[0] == '-') {
		return true
	}

	base, snapshot := strings.CutSuffix(version, "-SNAPSHOT")
	if !snapshot {
		return false
	}

	tail, ok := strings.CutPrefix(rest, base+"-")
	if !ok {
		return false
	}

	// "<yyyymmdd.hhmmss>-<build>" then an optional "-classifier".
	parts := strings.SplitN(tail, "-", 3)

	return len(parts) >= 2 && mavenSnapshot.MatchString(parts[0]+"-"+parts[1])
}

// cargoCoordinates parses "api/v1/crates/<crate>/<version>/download".
func cargoCoordinates(key string) (string, string, bool) {
	segs := strings.Split(key, "/")
	if len(segs) != 6 || segs[0] != "api" || segs[1] != "v1" || segs[2] != "crates" || segs[5] != "download" {
		return "", "", false
	}

	if segs[3] == "" || segs[4] == "" {
		return "", "", false
	}

	return segs[3], segs[4], true
}

var (
	gemVersionRe = regexp.MustCompile(`^\d+(\.[0-9A-Za-z]+)*$`)
	// semverRe matches a (possibly v-prefixed) MAJOR.MINOR.PATCH[-pre][+build].
	semverRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
)

// rubygemsCoordinates parses "gems/<name>-<version>[-<platform>].gem". The
// version is the first dash-separated part that looks like a version and is
// followed by nothing or a platform (which starts with a letter).
func rubygemsCoordinates(key string) (string, string, bool) {
	file, found := strings.CutPrefix(key, "gems/")
	if !found || strings.Contains(file, "/") {
		return "", "", false
	}

	stem, found := strings.CutSuffix(file, ".gem")
	if !found {
		return "", "", false
	}

	parts := strings.Split(stem, "-")
	for i := 1; i < len(parts); i++ {
		if !gemVersionRe.MatchString(parts[i]) {
			continue
		}

		if i+1 < len(parts) && (parts[i+1] == "" || parts[i+1][0] < 'A') {
			continue
		}

		return strings.Join(parts[:i], "-"), parts[i], true
	}

	return "", "", false
}

// nugetCoordinates parses "v3/package/<id>/<version>/<id>.<version>.nupkg".
func nugetCoordinates(key string) (string, string, bool) {
	segs := strings.Split(key, "/")
	if len(segs) != 5 || segs[0] != "v3" || segs[1] != "package" || segs[2] == "" || segs[3] == "" {
		return "", "", false
	}

	id, version := segs[2], segs[3]
	if segs[4] != id+"."+version+".nupkg" {
		return "", "", false
	}

	return id, version, true
}

// goCoordinates parses the case-encoded GOPROXY path
// "<module>/@v/<version>.zip". The .info, .mod, list and @latest documents
// are not artifacts.
func goCoordinates(key string) (string, string, bool) {
	esc, rest, found := strings.Cut(key, "/@v/")
	if !found {
		return "", "", false
	}

	encoded, found := strings.CutSuffix(rest, ".zip")
	if !found || encoded == "" {
		return "", "", false
	}

	mod, err := module.UnescapePath(esc)
	if err != nil {
		return "", "", false
	}

	version, err := module.UnescapeVersion(encoded)
	if err != nil {
		return "", "", false
	}

	return mod, version, true
}

// helmCoordinates parses "charts/<file>", where file (possibly below a
// sub-directory) is "<name>-<version>.tgz": the name is what precedes the
// first dash after which the rest is a semantic version.
func helmCoordinates(key string) (string, string, bool) {
	rel, found := strings.CutPrefix(key, helmChartsDir)
	if !found {
		return "", "", false
	}

	stem, found := strings.CutSuffix(path.Base(rel), ".tgz")
	if !found {
		return "", "", false
	}

	for i := 1; i < len(stem)-1; i++ {
		if stem[i] == '-' && semverRe.MatchString(stem[i+1:]) {
			return stem[:i], stem[i+1:], true
		}
	}

	return "", "", false
}
