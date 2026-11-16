// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package oacli

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"unicode"

	pkgcargo "github.com/openartifact/openartifact/pkg/cargo"
	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
	"github.com/openartifact/openartifact/pkg/lockfile"
)

// fetchConcurrency bounds the metadata requests --fetch runs at once.
const fetchConcurrency = 8

// warmMetadata requests the metadata document of every package of deps through
// repo (once per document), the way the package manager does before it downloads: a
// proxy behind repo (repo itself, or a member of a group) fetches the document
// from its upstream and caches it, which is what the availability check reads.
// Nothing is downloaded but metadata. A request that does not answer 200 (a
// package the upstream does not have, an error) is counted, not fatal: the
// check then reports that package as it finds it. So is a name that is not a
// package name (metadataPath): a lockfile never chooses the path of a request.
func warmMetadata(ctx context.Context, api *ciAPI, repo string, kind lockfile.Kind, deps []lockfile.Dep) *fetchSummary {
	seen := map[string]bool{}

	var (
		paths   []string
		refused = map[string]bool{}
	)

	for _, d := range deps {
		p, ok := metadataPath(repo, kind, d)
		if !ok {
			refused[d.Name] = true
			continue
		}

		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		failed = len(refused)
		sem    = make(chan struct{}, fetchConcurrency)
	)

	for _, p := range paths {
		sem <- struct{}{}

		wg.Go(func() {
			defer func() { <-sem }()

			if !fetchOK(ctx, api, p) {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		})
	}

	wg.Wait()

	return &fetchSummary{Packages: len(paths) + len(refused), Failed: failed}
}

// metadataPath is the native path of the metadata document in repo that lists
// d's version: composer's p2 document (p2/<name>~dev.json for a branch such as
// dev-main or 1.x-dev, as Composer requests it), npm's packument (a scoped name
// with its slash escaped, as npm requests it), the project page of the simple
// API (Python, the name normalized by the lockfile), the sparse index file of a
// crate, the compact index info file of a gem (as Bundler requests it) and the
// flat container version list of a NuGet package (as dotnet restore does).
//
// The name comes from the lockfile, which may be someone else's (a pull
// request): it only ever fills the package part of the path, percent-escaped.
// ok is false for a name that is not a package name of the format (an empty,
// "." or ".." segment, a control character, a "/" where the format has none),
// which is not requested.
func metadataPath(repo string, kind lockfile.Kind, d lockfile.Dep) (string, bool) {
	base := "/repos/" + escape(repo) + "/"
	name := d.Name

	// Composer names are vendor/package and npm ones may be @scope/package;
	// every other format names a package in one segment.
	slashes := 0
	if kind == lockfile.Composer || kind == lockfile.NPM || kind == lockfile.PNPM || kind == lockfile.Yarn {
		slashes = 1
	}

	if !packageName(name, slashes) {
		return "", false
	}

	switch kind {
	case lockfile.Composer:
		if composerDev(d.Version) {
			return base + "p2/" + escapePath(name) + "~dev.json", true
		}

		return base + "p2/" + escapePath(name) + ".json", true
	case lockfile.Poetry, lockfile.UV, lockfile.Pylock:
		return base + "simple/" + escape(name) + "/", true
	case lockfile.Cargo:
		if !crateName(name) {
			return "", false
		}

		return base + "index/" + escapePath(pkgcargo.IndexPathFor(strings.ToLower(name))), true
	case lockfile.Bundler:
		return base + "info/" + escape(name), true
	case lockfile.NuGet:
		return base + "v3/package/" + escape(strings.ToLower(name)) + "/index.json", true
	}

	if scope, pkg, ok := strings.Cut(name, "/"); ok {
		if !strings.HasPrefix(scope, "@") {
			return "", false
		}

		return base + escape(scope) + "%2f" + escape(pkg), true
	}

	return base + escape(name), true
}

// packageName reports whether name can be a package name with at most slashes
// "/" in it: no empty, "." or ".." segment and no control character.
func packageName(name string, slashes int) bool {
	if name == "" || strings.Count(name, "/") > slashes || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}

	for seg := range strings.SplitSeq(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}

	return true
}

// crateName reports whether name is a crate name (an ASCII letter, then letters,
// digits, "-" and "_"). The sparse index path is cut from the name's bytes, so
// a name such as "..ab" would put a dot segment into it.
func crateName(name string) bool {
	for i, c := range name {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || (c < '0' || c > '9') && c != '-' && c != '_') {
			return false
		}
	}

	return name != ""
}

// composerDev reports whether a locked Composer version is a branch, which
// Composer repositories list in the "~dev" document.
func composerDev(version string) bool {
	if n, err := pkgcomposer.Normalize(version); err == nil {
		return pkgcomposer.IsDev(n)
	}

	return pkgcomposer.IsDev(version)
}

// fetchOK GETs apiPath and reads the body through (a proxy caches the document
// as it streams it), reporting whether it answered 200.
func fetchOK(ctx context.Context, api *ciAPI, apiPath string) bool {
	resp, err := api.t.request(ctx, http.MethodGet, apiPath, nil, -1, nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode == http.StatusOK
}
