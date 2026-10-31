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
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/publicurl"
	pkgcomposer "github.com/openartifact/openartifact/pkg/composer"
)

// Composer proxy repositories mirror a Composer 2 repository (default
// https://repo.packagist.org); clients configure {"type": "composer", "url":
// ".../repos/REPO"}.
//
//   - packages.json is generated: a path-absolute metadata-url pointing at this
//     repository's own p2 documents. It carries no "available-packages" (an upstream cannot be
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
//   - With a minimum age (min_age_hours) a version whose "time" is younger is
//     left out of the rendered document (judged on the expanded entries; a
//     package with nothing left is a 404) and its dist is refused (errTooNew).
//     A version without a "time" passes.
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

// composerRepoPath is the path of the repository below an origin or prefix,
// without a trailing slash.
func composerRepoPath(repo *models.Repository) string {
	return "/repos/" + url.PathEscape(repo.Name)
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
	// metadata-url is path-absolute: Composer resolves a value starting with "/"
	// against the scheme and host of the repository URL
	// (ComposerRepository::canonicalizeUrl), so the document names no origin.
	prefix := publicurl.PathPrefix(s.baseURL)

	s.serveGeneratedJSON(w, r, repo, nil,
		[]byte(`{"packages":[],"metadata-url":"`+prefix+composerRepoPath(repo)+`/p2/%package%.json"}`), composerMaxAge)
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
	// dist.url is downloaded by Composer as-is, so it is absolute and its origin
	// is part of the validator below; the rewrite happens at serve time.
	root := s.publicBase(w, r) + composerRepoPath(repo)

	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveRendered(w, r, repo, renderedDoc{
		metaDoc: composerDoc(p.Name, p.Dev),
		maxAge:  composerMaxAge,
		// What is served depends on the cached document, on the origin of the dist
		// URLs and on which versions the gate (minimum age, curation) lets through now.
		etag: func(blobKey string) string {
			sum := sha256.Sum256([]byte(blobKey + "\x00" + root + "\x00" + g.Stamp()))
			return `"` + hex.EncodeToString(sum[:16]) + `"`
		},
		render: func(doc []byte, _ *resolution) ([]byte, string, error) {
			out, err := renderComposerDoc(g, doc, p.Name, root)
			return out, jsonMediaType, err
		},
	})
}

// renderComposerDoc renders the cached upstream p2 document of name for a
// client of the repository at root: the versions the gate holds back (minimum
// age at its time, curation rules) are left out (judged on the expanded entries, so a version that only
// inherited its "time" in the minified upstream document is dated right), and
// errNotFound when none is left, as for a package the upstream does not list.
func renderComposerDoc(g versionGate, doc []byte, name, root string) ([]byte, error) {
	var keep func(pkgcomposer.Version) bool

	if g.Active() {
		keep = func(v pkgcomposer.Version) bool {
			hidden, _ := g.Hidden(name, v.VersionString(), v.Published())
			return !hidden
		}
	}

	out, err := pkgcomposer.RenderSource(doc, name, root, keep)
	if errors.Is(err, pkgcomposer.ErrNoneLeft) {
		return nil, errNotFound
	}

	return out, err //nolint:wrapcheck // logged by serveRendered
}

func (s *Service) serveComposerDist(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgcomposer.Path) {
	key := composerDistKey(p)
	if len(p.Version) > pkgcomposer.MaxVersionLen || len(key) > maxPathLen {
		http.NotFound(w, r)
		return
	}

	g, err := s.gateFor(r.Context(), repo, time.Now())
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	// A dist already in the cache is served without locate, and with it the
	// minimum age (a file that was served once stays served). Curation is
	// checked first anyway, from the path alone, so a version curated after it
	// was cached is refused too.
	if g.Set.Active() {
		if gerr := checkGate(g, p.Name, p.Version, time.Time{}); gerr != nil {
			s.writeResolveError(w, r, gerr)
			return
		}
	}

	s.serveDownload(w, r, repo, key, func(ctx context.Context) (download, error) {
		return s.locateComposerDist(ctx, repo, p, g)
	})
}

// composerPublishedDist finds the dist the upstream's documents publish for the
// version of p. A version that is a branch lives in the "~dev" document, any
// other in the plain one; a version Composer cannot normalize is looked up in
// both. It also returns when the upstream published the version (zero when it
// does not say). errNotFound when none lists it.
func (s *Service) composerPublishedDist(
	ctx context.Context, repo *models.Repository, p pkgcomposer.Path,
) (pkgcomposer.Dist, time.Time, error) {
	type published struct {
		dist pkgcomposer.Dist
		time time.Time
	}

	devs := []bool{false, true}

	if normalized, err := pkgcomposer.Normalize(p.Version); err == nil {
		devs = []bool{pkgcomposer.IsDev(normalized)}
	}

	for _, dev := range devs {
		d, _, err := parseDoc(ctx, s, repo, composerDoc(p.Name, dev), func(doc []byte, _ *resolution) (*published, error) {
			versions, perr := pkgcomposer.ParseDocument(doc, p.Name)
			if perr != nil {
				return nil, perr //nolint:wrapcheck // logged by parseDoc
			}

			if v, ok := pkgcomposer.FindVersion(versions, p.Version); ok {
				if d, ok := v.ZipDist(); ok {
					return &published{dist: d, time: v.Published()}, nil
				}
			}

			return nil, nil //nolint:nilnil // not listed: the other document may list it
		})
		if errors.Is(err, errNotFound) {
			continue
		}

		if err != nil {
			return pkgcomposer.Dist{}, time.Time{}, err
		}

		if d != nil {
			return d.dist, d.time, nil
		}
	}

	return pkgcomposer.Dist{}, time.Time{}, errNotFound
}

// locateComposerDist finds the dist the upstream's documents publish for the
// version of p; the gate's refusal (checkGate) when the version is held back.
// The reference in the request must be the one that dist has (a
// stale or forged URL is errNotFound, without a negative cache entry: the
// document may simply be stale). It is cached only when its SHA-1 matches a
// non-empty "shasum".
func (s *Service) locateComposerDist(ctx context.Context, repo *models.Repository, p pkgcomposer.Path, g versionGate) (download, error) {
	dist, published, err := s.composerPublishedDist(ctx, repo, p)
	if err != nil {
		return download{}, err
	}

	if dist.ProxyRef() != p.Ref {
		return download{}, errNotFound
	}

	if gerr := checkGate(g, p.Name, p.Version, published); gerr != nil {
		return download{}, gerr
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

// ComposerHooks is the proxy hooks of the composer format.
var ComposerHooks = Hooks{ //nolint:gochecknoglobals // format hook table
	Serve: (*Service).serveComposer,
	// Dists of released versions; the reference in their key names the content.
	Immutable:  composerDistImmutable,
	IsMetadata: func(key string) bool { return !strings.HasPrefix(key, composerDistDir) },
	BlobPath:   presign.ComposerBlobPath,
	Published:  (*Service).composerPublished,
}

// composerPublished dates version of the package name from the cached p2
// documents (release and dev), never fetching.
func (s *Service) composerPublished(ctx context.Context, repo *models.Repository, name, version string) (time.Time, bool) {
	for _, dev := range []bool{false, true} {
		body, ok := s.cachedDocBody(ctx, repo, composerDoc(name, dev))
		if !ok {
			continue
		}

		versions, err := pkgcomposer.ParseDocument(body, name)
		if err != nil {
			continue
		}

		if v, found := pkgcomposer.FindVersion(versions, version); found {
			if published := v.Published(); !published.IsZero() {
				return published, true
			}
		}
	}

	return time.Time{}, false
}
