// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/mod/module"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/pypisimple"
)

// maxCachedKeys bounds the cache keys read for one package: more files than
// this (one per version and platform) are not listed as cached.
const maxCachedKeys = 20000

// cacheKeyPrefixes maps a proxy format to the function giving the prefix every
// cache key of a package's files starts with (the keys of the formats of
// coordinateParsers name the version of the file; the rest are absent, their
// cached files cannot be tied to a version). ok is false for a package name the
// format cannot have.
var cacheKeyPrefixes = map[string]func(pkg string) (string, bool){ //nolint:gochecknoglobals // constant lookup table
	"cargo": func(pkg string) (string, bool) { return cargoCrateDir + strings.ToLower(pkg) + "/", true },
	"go": func(pkg string) (string, bool) {
		esc, err := module.EscapePath(pkg)
		return esc + "/@v/", err == nil
	},
	"helm":     func(string) (string, bool) { return helmChartsDir, true },
	"npm":      func(pkg string) (string, bool) { return pkg + "/-/", true },
	"nuget":    func(pkg string) (string, bool) { return "v3/package/" + strings.ToLower(pkg) + "/", true },
	"pypi":     func(pkg string) (string, bool) { return "files/" + pypisimple.NormalizeName(pkg) + "/", true },
	"rubygems": func(pkg string) (string, bool) { return "gems/" + pkg + "-", true },
}

// versionID is the form versions of format are compared in: NuGet lowercases the
// versions of its download paths.
func versionID(format, version string) string {
	if format == "nuget" {
		return strings.ToLower(version)
	}

	return version
}

// cachedVersions is the set of versions of pkg (as versionID spells them) a file
// of which is in the cache of repo, read from the cache keys alone. known is false
// when the format's keys name no versions.
func (s *Service) cachedVersions(ctx context.Context, repo *models.Repository, pkg string) (map[string]struct{}, bool, error) {
	prefixOf, found := cacheKeyPrefixes[repo.Format]
	if !found {
		return nil, false, nil
	}

	prefix, ok := prefixOf(pkg)
	if !ok {
		return nil, true, nil
	}

	keys, err := s.db.ListCachedProxyKeys(ctx, repo.ID, prefix, maxCachedKeys)
	if err != nil {
		return nil, false, fmt.Errorf("list cached files of %q: %w", pkg, err)
	}

	cached := make(map[string]struct{})

	for _, key := range keys {
		if v, ok := cachedKeyVersion(repo.Format, pkg, key); ok {
			cached[versionID(repo.Format, v)] = struct{}{}
		}
	}

	return cached, true, nil
}

// cachedKeyVersion is the version of pkg the package file cached under key
// belongs to; ok is false for any other key under the package's prefix (an
// index, a checksum, a file of another package that shares the prefix).
func cachedKeyVersion(format, pkg, key string) (string, bool) {
	if format == "pypi" {
		project := pypisimple.NormalizeName(pkg)

		rest, found := strings.CutPrefix(key, "files/"+project+"/")
		if !found || strings.Contains(rest, "/") || strings.HasSuffix(rest, pypisimple.MetadataSuffix) {
			return "", false
		}

		v := pypisimple.SpelledVersion(project, rest)

		return v, v != ""
	}

	name, version, ok := PackageCoordinates(format, key)
	if !ok {
		return "", false
	}

	return version, strings.EqualFold(name, pkg)
}
