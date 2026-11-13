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
// check then reports that package as it finds it.
func warmMetadata(ctx context.Context, api *ciAPI, repo string, kind lockfile.Kind, deps []lockfile.Dep) *fetchSummary {
	seen := map[string]bool{}

	var paths []string

	for _, d := range deps {
		if p := metadataPath(repo, kind, d); !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		failed int
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

	return &fetchSummary{Packages: len(paths), Failed: failed}
}

// metadataPath is the native path of the metadata document in repo that lists
// d's version: composer's p2 document (p2/<name>~dev.json for a branch such as
// dev-main or 1.x-dev, as Composer requests it), npm's packument (a scoped name
// with its slash escaped, as npm requests it).
func metadataPath(repo string, kind lockfile.Kind, d lockfile.Dep) string {
	base := "/repos/" + escape(repo) + "/"
	name := d.Name

	if kind == lockfile.Composer {
		if composerDev(d.Version) {
			return base + "p2/" + name + "~dev.json"
		}

		return base + "p2/" + name + ".json"
	}

	if scope, pkg, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
		return base + scope + "%2f" + pkg
	}

	return base + name
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
