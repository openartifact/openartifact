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

	"github.com/openartifact/openartifact/pkg/lockfile"
)

// fetchConcurrency bounds the metadata requests --fetch runs at once.
const fetchConcurrency = 8

// warmMetadata requests the package-level metadata of every distinct package of
// deps through repo, the way the package manager does before it downloads: a
// proxy behind repo (repo itself, or a member of a group) fetches the document
// from its upstream and caches it, which is what the availability check reads.
// Nothing is downloaded but metadata. A request that does not answer 200 (a
// package the upstream does not have, an error) is counted, not fatal: the
// check then reports that package as it finds it.
func warmMetadata(ctx context.Context, api *ciAPI, repo string, kind lockfile.Kind, deps []lockfile.Dep) *fetchSummary {
	seen := map[string]bool{}

	var names []string

	for _, d := range deps {
		if !seen[d.Name] {
			seen[d.Name] = true
			names = append(names, d.Name)
		}
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		failed int
		sem    = make(chan struct{}, fetchConcurrency)
	)

	for _, name := range names {
		sem <- struct{}{}

		wg.Go(func() {
			defer func() { <-sem }()

			if !fetchOK(ctx, api, metadataPath(repo, kind, name)) {
				mu.Lock()
				failed++
				mu.Unlock()
			}
		})
	}

	wg.Wait()

	return &fetchSummary{Packages: len(names), Failed: failed}
}

// metadataPath is the native path of a package's metadata document in repo:
// composer's p2 document, npm's packument (a scoped name with its slash
// escaped, as npm requests it).
func metadataPath(repo string, kind lockfile.Kind, name string) string {
	base := "/repos/" + escape(repo) + "/"

	if kind == lockfile.Composer {
		return base + "p2/" + name + ".json"
	}

	if scope, pkg, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
		return base + scope + "%2f" + pkg
	}

	return base + name
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
