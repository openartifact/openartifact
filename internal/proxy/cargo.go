// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/presign"
	pkgcargo "github.com/openartifact/openartifact/pkg/cargo"
)

// Cargo proxy repositories mirror a sparse registry (default
// https://index.crates.io); clients point cargo at HOST/repos/REPO/index/:
//
//   - config.json is generated: dl and api point at this repository, so the
//     upstream's own dl host is never handed to clients.
//   - Index files (index/<prefix>/<crate>) are cached verbatim under the key
//     "index/<prefix>/<crate>" with the repository's cache TTL and serve-stale
//     rules.
//   - Crate files (api/v1/crates/<crate>/<version>/download) are cached under
//     that key, immutable. The upstream URL comes from the upstream's
//     config.json "dl" template (cached under "config.json") and the digest from
//     the crate's index entry; bytes that do not match its cksum are refused and
//     never cached.
//
// With a minimum age (see minage.go) the index file is rendered without the
// entries whose "pubtime" is too young (an entry without one stays), and a
// crate file of such a version is refused.

// cargoConfigKey is the cache key of the upstream's own config.json.
const cargoConfigKey = "config.json"

// cargoConfigMaxAge is how long shared caches may reuse the generated
// config.json of a public repository.
const cargoConfigMaxAge = 300

func (s *Service) serveCargo(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, err := pkgcargo.ParsePath(relPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch p.Kind {
	case pkgcargo.KindConfig:
		s.serveCargoConfig(w, r, repo)
	case pkgcargo.KindIndex:
		s.serveCargoIndex(w, r, repo, p)
	case pkgcargo.KindDownload:
		s.serveCargoCrate(w, r, repo, p)
	}
}

func (s *Service) serveCargoConfig(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	// The body carries the origin the client reached, so the content ETag
	// serveGeneratedJSON derives from it differs per origin.
	s.serveGeneratedJSON(w, r, repo, nil, pkgcargo.RenderConfig(s.publicBase(w, r), repo.Name, repo.Public), cargoConfigMaxAge)
}

func cargoIndexKey(indexPath string) string { return "index/" + indexPath }

// cargoIndexDoc is a crate's index file, cached verbatim.
func cargoIndexDoc(indexPath string) metaDoc {
	return metaDoc{
		key:   cargoIndexKey(indexPath),
		url:   func(upstream string) (string, error) { return upstreamURL(upstream, indexPath) },
		opts:  staticOpts(fetchOpts{verify: pkgcargo.VerifyIndex}),
		limit: pkgcargo.MaxIndexBytes,
		what:  "cargo index",
		bad:   badDocUpstream,
	}
}

// cargoConfigDoc is the upstream's own config.json, whose "dl" template tells
// where crate files live.
var cargoConfigDoc = metaDoc{ //nolint:gochecknoglobals // read-only descriptor
	key:   cargoConfigKey,
	opts:  staticOpts(fetchOpts{accept: jsonMediaType, verify: pkgcargo.VerifyConfig}),
	limit: 1 << 20,
	what:  "cargo config",
	bad:   badDocUpstream,
}

// serveCargoIndex answers an index file: verbatim, or with the versions the
// minimum-age gate holds back left out (a crate none of whose versions is old
// enough is not found, as a crate without versions is).
func (s *Service) serveCargoIndex(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcargo.Path) {
	if minAge(repo) == 0 {
		s.serveVerbatim(w, r, repo, cargoIndexDoc(p.IndexPath))
		return
	}

	now := time.Now()
	stamp := gateStamp(repo, now)

	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: cargoIndexDoc(p.IndexPath),
		maxAge:  cargoIndexMaxAge,
		// The rendering depends on the cached blob and on how many versions the
		// gate holds back at this time.
		etag: func(blobKey string) string { return `"` + blobKey + `~` + stamp + `"` },
		render: func(body []byte, res *resolution) ([]byte, string, error) {
			out, err := filterCargoIndex(repo, body, now)

			ct := res.entry.ContentType
			if ct == "" {
				ct = "text/plain"
			}

			return out, ct, err
		},
	})
}

// cargoIndexMaxAge is how long shared caches may reuse the gated rendering of
// a public repository's index file.
const cargoIndexMaxAge = 60

// filterCargoIndex leaves out of the index file doc the entries whose "pubtime"
// is younger than repo's minimum age at now; an entry without a pubtime stays.
// It is errNotFound when no entry is left, and wraps errUnusableDoc when doc
// does not parse.
func filterCargoIndex(repo *models.Repository, doc []byte, now time.Time) ([]byte, error) {
	entries, err := pkgcargo.ParseIndex(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errUnusableDoc, err)
	}

	out := make([]byte, 0, len(doc))

	for _, e := range entries {
		if tooNew(repo, e.Published(), now) {
			continue
		}

		out = append(out, e.Line...)
		out = append(out, '\n')
	}

	if len(out) == 0 {
		return nil, errNotFound
	}

	return out, nil
}

func (s *Service) serveCargoCrate(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcargo.Path) {
	now := time.Now()

	s.serveDownload(w, r, repo, "api/v1/crates/"+p.Crate+"/"+p.Version+"/download", func(ctx context.Context) (download, error) {
		return s.locateCargoCrate(ctx, repo, p, now)
	})
}

// cargoDownloadTemplate returns the "dl" of the upstream's config.json,
// fetching it when it is not cached.
func (s *Service) cargoDownloadTemplate(ctx context.Context, repo *models.Repository) (string, error) {
	cfg, _, err := parseDoc(ctx, s, repo, cargoConfigDoc, func(doc []byte, _ *resolution) (pkgcargo.Config, error) {
		return pkgcargo.ParseConfig(bytes.NewReader(doc)) //nolint:wrapcheck // logged by parseDoc
	})
	if errors.Is(err, errNotFound) {
		// A registry without config.json is not a sparse registry.
		s.log.Warnw("proxy upstream has no config.json", "repo", repo.Name)
		return "", errUpstream
	}

	if err != nil {
		return "", err
	}

	return cfg.DL, nil
}

// locateCargoCrate finds one crate file: the URL the upstream's dl template
// gives, cached only when it matches the cksum of its index entry. A version
// the index does not list is errNotFound, one younger than the repository's
// minimum age at now is errTooNew.
func (s *Service) locateCargoCrate(ctx context.Context, repo *models.Repository, p pkgcargo.Path, now time.Time) (download, error) {
	crate := strings.ToLower(p.Crate)

	entry, _, err := parseDoc(ctx, s, repo, cargoIndexDoc(pkgcargo.IndexPathFor(crate)),
		func(doc []byte, _ *resolution) (*pkgcargo.Entry, error) {
			entry, found, ferr := pkgcargo.FindVersion(doc, p.Version)
			if ferr != nil || !found {
				return nil, ferr //nolint:wrapcheck // logged by parseDoc
			}

			return &entry, nil
		})
	if err != nil {
		return download{}, err
	}

	if entry == nil {
		return download{}, errNotFound
	}

	if tooNew(repo, entry.Published(), now) {
		return download{}, fmt.Errorf("crate %s %s: %w", p.Crate, p.Version, errTooNew)
	}

	if !pkgcargo.ValidCksum(entry.Cksum) {
		s.log.Warnw("upstream cargo index entry has no usable cksum", "repo", repo.Name, "crate", p.Crate, "version", p.Version)
		return download{}, errUpstream
	}

	dl, err := s.cargoDownloadTemplate(ctx, repo)
	if err != nil {
		return download{}, err
	}

	return download{
		url:  pkgcargo.DownloadURL(dl, p.Crate, p.Version, entry.Cksum),
		opts: fetchOpts{wantSHA256: strings.ToLower(entry.Cksum)},
	}, nil
}

// cargoCrateDir is the key prefix of the crate download endpoint; everything
// outside it is index data or config.json.
const cargoCrateDir = "api/v1/crates/"

// CargoHooks is the proxy hooks of the cargo format.
var CargoHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve: (*Service).serveCargo,
	// Crate files, not index files or config.json.
	Immutable:  func(key string) bool { return strings.HasPrefix(key, cargoCrateDir) && keyName(key) == "download" },
	IsMetadata: func(key string) bool { return !strings.HasPrefix(key, cargoCrateDir) },
	BlobPath:   presign.CargoBlobPath,
}
