// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
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
	body := pkgcargo.RenderConfig(s.baseURL, repo.Name, repo.Public)
	sum := sha256.Sum256(body)

	if base.Revalidate(w, r, repo.Public, `"`+hex.EncodeToString(sum[:])+`"`, cargoConfigMaxAge) {
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func cargoIndexKey(indexPath string) string { return "index/" + indexPath }

// resolveCargoIndex returns the cache entry of a crate's index file, fetching
// it upstream when it is absent or expired (and serving stale when the
// upstream is down).
func (s *Service) resolveCargoIndex(ctx context.Context, repo *models.Repository, p pkgcargo.Path) (*resolution, error) {
	key := cargoIndexKey(p.IndexPath)

	return s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		target, err := upstreamURL(repo.UpstreamURL, p.IndexPath)
		if err != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
			return nil, errInternal
		}

		return s.fetchURL(fctx, repo, key, target, fetchOpts{verify: pkgcargo.VerifyIndex})
	})
}

func (s *Service) serveCargoIndex(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcargo.Path) {
	key := cargoIndexKey(p.IndexPath)
	resolve := func() (*resolution, error) { return s.resolveCargoIndex(r.Context(), repo, p) }

	res, err := resolve()
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}

func (s *Service) serveCargoCrate(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcargo.Path) {
	key := "api/v1/crates/" + p.Crate + "/" + p.Version + "/download"
	resolve := func() (*resolution, error) {
		return s.resolveWith(r.Context(), repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
			return s.fetchCargoCrate(fctx, repo, p, key)
		})
	}

	res, err := resolve()
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}

// readCargoBlob loads a cached index file or config.json.
func (s *Service) readCargoBlob(ctx context.Context, entry *models.ProxyCacheEntry, limit int64) ([]byte, error) {
	rc, err := s.blobStore.Get(ctx, entry.BlobKey)
	if err != nil {
		return nil, fmt.Errorf("open cached cargo metadata: %w", err)
	}
	defer func() { base.CloseLog(rc, s.log) }()

	body, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read cached cargo metadata: %w", err)
	}

	if int64(len(body)) > limit {
		return nil, errInternal
	}

	return body, nil
}

// cargoDownloadTemplate returns the "dl" of the upstream's config.json,
// fetching it when it is not cached.
func (s *Service) cargoDownloadTemplate(ctx context.Context, repo *models.Repository) (string, error) {
	res, err := s.resolveWith(ctx, repo, cargoConfigKey, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		target, uerr := upstreamURL(repo.UpstreamURL, cargoConfigKey)
		if uerr != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", uerr)
			return nil, errInternal
		}

		return s.fetchURL(fctx, repo, cargoConfigKey, target, fetchOpts{accept: "application/json", verify: pkgcargo.VerifyConfig})
	})
	if errors.Is(err, errNotFound) {
		// A registry without config.json is not a sparse registry.
		s.log.Warnw("proxy upstream has no config.json", "repo", repo.Name)
		return "", errUpstream
	}

	if err != nil {
		return "", err
	}

	doc, err := s.readCargoBlob(ctx, res.entry, 1<<20)
	if err != nil {
		s.log.Errorw("failed to read cached cargo config", "repo", repo.Name, "error", err)
		return "", errInternal
	}

	cfg, err := pkgcargo.ParseConfig(bytes.NewReader(doc))
	if err != nil {
		s.log.Warnw("unreadable upstream cargo config", "repo", repo.Name, "error", err)
		return "", errUpstream
	}

	return cfg.DL, nil
}

// fetchCargoCrate downloads one crate file from the URL the upstream's dl
// template gives and caches it only when it matches the cksum of its index
// entry. A version the index does not list is errNotFound without a negative
// cache entry: the index may simply be stale.
func (s *Service) fetchCargoCrate(ctx context.Context, repo *models.Repository, p pkgcargo.Path, key string) (*models.ProxyCacheEntry, error) {
	idx := pkgcargo.Path{Kind: pkgcargo.KindIndex, Crate: strings.ToLower(p.Crate)}
	idx.IndexPath = pkgcargo.IndexPathFor(idx.Crate)

	res, err := s.resolveCargoIndex(ctx, repo, idx)
	if err != nil {
		return nil, err
	}

	if aerr := s.packuments.Acquire(ctx); aerr != nil {
		return nil, aerr //nolint:wrapcheck // caller checks for cancellation
	}

	doc, err := s.readCargoBlob(ctx, res.entry, pkgcargo.MaxIndexBytes)
	if err != nil {
		s.packuments.Release()
		s.log.Errorw("failed to read cached cargo index", "repo", repo.Name, "crate", p.Crate, "error", err)

		return nil, errInternal
	}

	entry, found, err := pkgcargo.FindVersion(doc, p.Version)
	s.packuments.Release()

	if err != nil {
		s.log.Warnw("unreadable upstream cargo index", "repo", repo.Name, "crate", p.Crate, "error", err)
		return nil, errUpstream
	}

	if !found {
		return nil, errNotFound
	}

	if !pkgcargo.ValidCksum(entry.Cksum) {
		s.log.Warnw("upstream cargo index entry has no usable cksum", "repo", repo.Name, "crate", p.Crate, "version", p.Version)
		return nil, errUpstream
	}

	dl, err := s.cargoDownloadTemplate(ctx, repo)
	if err != nil {
		return nil, err
	}

	target := pkgcargo.DownloadURL(dl, p.Crate, p.Version, entry.Cksum)

	return s.fetchURL(ctx, repo, key, target, fetchOpts{
		// Credentials go only to the configured upstream, never to the
		// host its config.json points downloads at.
		noAuth:     !sameOriginStr(repo.UpstreamURL, target),
		wantSHA256: strings.ToLower(entry.Cksum),
	})
}
