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
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
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
	s.serveGenerated(w, r, repo, nil,
		[]byte(`{"packages":[],"metadata-url":"`+s.composerRoot(repo)+`/p2/%package%.json"}`), jsonMediaType, composerMaxAge)
}

// composerDoc is the upstream p2 document of name, cached verbatim.
func composerDoc(name string, dev bool) metaDoc {
	return metaDoc{
		key:   composerKey(name, dev),
		opts:  staticOpts(jsonDocOpts(composerVerifyDocument(name))),
		limit: composerMaxDoc,
		what:  "composer document",
		bad:   badDocInternal,
	}
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

func (s *Service) serveComposerMetadata(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcomposer.Path) {
	root := s.composerRoot(repo)

	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: composerDoc(p.Name, p.Dev),
		maxAge:  composerMaxAge,
		// What is served depends on the cached document and on the server's URL.
		etag: func(blobKey string) string {
			sum := sha256.Sum256([]byte(blobKey + "\x00" + root))
			return `"` + hex.EncodeToString(sum[:16]) + `"`
		},
		render: func(doc []byte, _ *resolution) ([]byte, string, error) {
			out, err := pkgcomposer.RenderSource(doc, p.Name, root)
			return out, jsonMediaType, err
		},
	})
}

func (s *Service) serveComposerDist(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcomposer.Path) {
	key := composerDistKey(p)
	if len(p.Version) > pkgcomposer.MaxVersionLen || len(key) > maxPathLen {
		http.NotFound(w, r)
		return
	}

	s.serveDownload(w, r, repo, key, func(ctx context.Context) (download, error) {
		return s.locateComposerDist(ctx, repo, p)
	})
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
		d, _, err := parseDoc(ctx, s, repo, composerDoc(p.Name, dev), func(doc []byte, _ *resolution) (*pkgcomposer.Dist, error) {
			versions, perr := pkgcomposer.ParseDocument(doc, p.Name)
			if perr != nil {
				return nil, perr //nolint:wrapcheck // logged by parseDoc
			}

			if d, ok := pkgcomposer.FindDist(versions, p.Version); ok {
				return &d, nil
			}

			return nil, nil //nolint:nilnil // not listed: the other document may list it
		})
		if errors.Is(err, errNotFound) {
			continue
		}

		if err != nil {
			return pkgcomposer.Dist{}, err
		}

		if d != nil {
			return *d, nil
		}
	}

	return pkgcomposer.Dist{}, errNotFound
}

// locateComposerDist finds the dist the upstream's documents publish for the
// version of p. The reference in the request must be the one that dist has (a
// stale or forged URL is errNotFound, without a negative cache entry: the
// document may simply be stale). It is cached only when its SHA-1 matches a
// non-empty "shasum".
func (s *Service) locateComposerDist(ctx context.Context, repo *models.Repository, p pkgcomposer.Path) (download, error) {
	dist, err := s.composerPublishedDist(ctx, repo, p)
	if err != nil {
		return download{}, err
	}

	if dist.ProxyRef() != p.Ref {
		return download{}, errNotFound
	}

	return download{url: dist.URL, opts: fetchOpts{
		verify: func(spool io.ReadSeeker) error {
			return pkgcomposer.VerifySHA1(spool, dist.SHA1) //nolint:wrapcheck // logged by store
		},
	}}, nil
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
