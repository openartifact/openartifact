// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
)

// Composer proxy repositories mirror a Composer 2 repository (default
// https://repo.packagist.org); clients configure {"type": "composer", "url":
// ".../repos/REPO"}.
//
//   - packages.json is generated: a metadata-url pointing at this repository's
//     own p2 documents. It carries no "available-packages" (an upstream cannot be
//     enumerated) and no list or search endpoint: Packagist serves those from
//     another host than its metadata, so they are not part of the mirror.
//   - p2/VENDOR/NAME.json and p2/VENDOR/NAME~dev.json are fetched upstream and
//     cached verbatim under the same keys with the repository's cache TTL and
//     serve-stale rules. A request renders the cached document for the client:
//     it is expanded, the "dist" of every version is pointed at this repository
//     and it is minified again (pkgcomposer.RenderSource). The "source" blocks,
//     the dists' "reference" and "shasum" and everything else stay as published.
//     The cache stays upstream-shaped and independent of the server's own URL.
//   - A dist (dists/VENDOR/NAME/VERSION/REF.zip) is fetched from the URL the
//     cached document publishes for that version, never from anything the client
//     sent: REF is a digest of that URL and reference (Dist.ProxyRef), so a
//     request that does not name what the document publishes is a 404. The URL
//     is checked against the outbound policy and redirects (GitHub's zipball
//     redirects to codeload) are followed by the same SSRF-safe client. The
//     repository's credentials are sent only to the upstream's own origin, never
//     to the host a dist lives on. A dist is refused, and not cached, when its
//     SHA-1 differs from a non-empty "shasum"; otherwise the reference (a commit)
//     is what vouches for it. Stable versions are cached as immutable, dev
//     versions (branches) for the repository TTL.
//   - A version whose dist is not a zip loses its dist, so Composer installs it
//     from its "source".
const (
	// composerDistDir is the key prefix of the dists.
	composerDistDir = "dists/"

	// composerMaxDoc bounds a p2 document read into memory.
	composerMaxDoc = 64 << 20

	// composerMaxAge is how long shared caches may reuse a generated document of
	// a public repository.
	composerMaxAge = 60
)

func (s *Service) serveComposer(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, ok := pkgcomposer.ParsePath(relPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch p.Kind {
	case pkgcomposer.KindRoot:
		s.serveComposerRoot(w, r, repo)
	case pkgcomposer.KindMetadata:
		s.serveComposerMetadata(w, r, repo, p)
	case pkgcomposer.KindDist:
		s.serveComposerDist(w, r, repo, p)
	case pkgcomposer.KindList, pkgcomposer.KindSearch, pkgcomposer.KindUpload:
		http.NotFound(w, r)
	}
}

// composerRoot is the absolute URL of the repository, without a trailing slash.
func (s *Service) composerRoot(repo *models.Repository) string {
	return strings.TrimSuffix(s.baseURL, "/") + "/repos/" + url.PathEscape(repo.Name)
}

func composerKey(name string, dev bool) string {
	if dev {
		return "p2/" + name + "~dev.json"
	}

	return "p2/" + name + ".json"
}

func composerDistKey(p pkgcomposer.Path) string {
	return composerDistDir + p.Name + "/" + p.Version + "/" + p.Ref + ".zip"
}

func (s *Service) serveComposerRoot(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	s.writeGeneratedJSON(w, r, repo, nil,
		[]byte(`{"packages":[],"metadata-url":"`+s.composerRoot(repo)+`/p2/%package%.json"}`), composerMaxAge)
}

// composerDocument returns the cached upstream p2 document of name, fetching it
// when it is absent or expired, and its bytes.
func (s *Service) composerDocument(ctx context.Context, repo *models.Repository, name string, dev bool) (*resolution, []byte, error) {
	key := composerKey(name, dev)

	res, err := s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		target, uerr := upstreamURL(repo.UpstreamURL, key)
		if uerr != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", uerr)
			return nil, errInternal
		}

		return s.fetchURL(fctx, repo, key, target, fetchOpts{
			accept:      jsonMediaType,
			contentType: jsonMediaType,
			verify:      composerVerifyDocument(name),
		})
	})
	if err != nil {
		return nil, nil, err
	}

	body, err := s.readComposerDocument(ctx, repo, res)
	if err != nil {
		return nil, nil, err
	}

	return res, body, nil
}

// composerVerifyDocument refuses an upstream answer that is not the p2 document
// of name (an HTML page served with 200, a truncated or foreign document).
func composerVerifyDocument(name string) func(io.ReadSeeker) error {
	return func(spool io.ReadSeeker) error {
		body, err := io.ReadAll(io.LimitReader(spool, composerMaxDoc+1))
		if err != nil {
			return fmt.Errorf("read document: %w", err)
		}

		if len(body) > composerMaxDoc {
			return fmt.Errorf("%w: document too large", pkgcomposer.ErrDocument)
		}

		_, err = pkgcomposer.ParseDocument(body, name)

		return err //nolint:wrapcheck // logged by store
	}
}

// readComposerDocument loads a cached document under the document gate.
func (s *Service) readComposerDocument(ctx context.Context, repo *models.Repository, res *resolution) ([]byte, error) {
	if err := s.packuments.Acquire(ctx); err != nil {
		return nil, err //nolint:wrapcheck // caller checks for cancellation
	}
	defer s.packuments.Release()

	body, err := s.readCachedDoc(ctx, res.entry, composerMaxDoc)
	if err != nil {
		s.log.Errorw("failed to read cached composer document", "repo", repo.Name, "key", res.entry.CacheKey, "error", err)
		return nil, errInternal
	}

	return body, nil
}

func (s *Service) serveComposerMetadata(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcomposer.Path) {
	res, doc, err := s.composerDocument(r.Context(), repo, p.Name, p.Dev)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	root := s.composerRoot(repo)

	// What is served depends on the cached document and on the server's URL.
	sum := sha256.Sum256([]byte(res.entry.BlobKey + "\x00" + root))

	if res.stale {
		w.Header().Set("Warning", staleWarning)
	}

	if base.Revalidate(w, r, repo.Public, `"`+hex.EncodeToString(sum[:16])+`"`, composerMaxAge) {
		return
	}

	body, err := pkgcomposer.RenderSource(doc, p.Name, root)
	if err != nil {
		s.log.Errorw("unusable cached composer document", "repo", repo.Name, "key", res.entry.CacheKey, "error", err)
		s.failWithoutValidators(w, r, errInternal)

		return
	}

	w.Header().Set("Content-Type", jsonMediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (s *Service) serveComposerDist(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcomposer.Path) {
	key := composerDistKey(p)
	if len(p.Version) > pkgcomposer.MaxVersionLen || len(key) > maxPathLen {
		http.NotFound(w, r)
		return
	}

	resolve := func() (*resolution, error) {
		return s.resolveWith(r.Context(), repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
			return s.fetchComposerDist(fctx, repo, p, key)
		})
	}

	res, err := resolve()
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}

// composerPublishedDist finds the dist the upstream's documents publish for the
// version of p. A version that is a branch lives in the "~dev" document, any
// other in the plain one; a version Composer cannot normalize is looked up in
// both. errNotFound when none lists it.
func (s *Service) composerPublishedDist(ctx context.Context, repo *models.Repository, p pkgcomposer.Path) (pkgcomposer.Dist, error) {
	devs := []bool{false, true}

	if normalized, err := pkgcomposer.Normalize(p.Version); err == nil {
		devs = []bool{pkgcomposer.IsDev(normalized)}
	}

	for _, dev := range devs {
		_, doc, err := s.composerDocument(ctx, repo, p.Name, dev)
		if errors.Is(err, errNotFound) {
			continue
		}

		if err != nil {
			return pkgcomposer.Dist{}, err
		}

		versions, perr := pkgcomposer.ParseDocument(doc, p.Name)
		if perr != nil {
			s.log.Errorw("cached composer document is unusable", "repo", repo.Name, "package", p.Name, "error", perr)
			return pkgcomposer.Dist{}, errInternal
		}

		if d, ok := pkgcomposer.FindDist(versions, p.Version); ok {
			return d, nil
		}
	}

	return pkgcomposer.Dist{}, errNotFound
}

// fetchComposerDist downloads the dist the upstream's documents publish for the
// version of p and caches it. The reference in the request must be the one that
// dist has (a stale or forged URL is errNotFound, without a negative cache
// entry: the document may simply be stale).
func (s *Service) fetchComposerDist(
	ctx context.Context,
	repo *models.Repository,
	p pkgcomposer.Path,
	key string,
) (*models.ProxyCacheEntry, error) {
	dist, err := s.composerPublishedDist(ctx, repo, p)
	if err != nil {
		return nil, err
	}

	if dist.ProxyRef() != p.Ref {
		return nil, errNotFound
	}

	return s.fetchURL(ctx, repo, key, dist.URL, fetchOpts{
		// Credentials go only to the configured upstream, never to the host a
		// document points a dist at.
		noAuth: !sameOriginStr(repo.UpstreamURL, dist.URL),
		verify: func(spool io.ReadSeeker) error {
			return pkgcomposer.VerifySHA1(spool, dist.SHA1) //nolint:wrapcheck // logged by store
		},
	})
}

// composerDistImmutable reports whether key is the dist of a released version:
// a branch's dist moves with its reference, so it is cached for the TTL only.
func composerDistImmutable(key string) bool {
	p, ok := pkgcomposer.ParsePath(key)
	if !ok || p.Kind != pkgcomposer.KindDist {
		return false
	}

	normalized, err := pkgcomposer.Normalize(p.Version)

	return err == nil && !pkgcomposer.IsDev(normalized)
}
