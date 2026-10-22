// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	pkgnpm "github.com/openartifact/openartifact/pkg/npm"
)

// npm proxy repositories mirror a registry (default https://registry.npmjs.org):
//
//   - Packuments (GET /<pkg>, GET /@scope%2fname) are cached verbatim under the
//     key "<pkg>" (full) or "-/install-v1/<pkg>" (abbreviated, for clients that
//     send Accept: application/vnd.npm.install-v1+json) with the repository's
//     cache TTL and serve-stale rules. Every response rewrites each
//     dist.tarball to this repository, so the cache stays upstream-shaped and
//     independent of the server's own URL.
//   - Tarballs (GET /<pkg>/-/<file>) are cached under "<pkg>/-/<file>",
//     immutable. The upstream URL and digest come from the packument's dist
//     object for that file; bytes that do not match dist.integrity (or
//     dist.shasum) are refused and never cached.
//   - GET /<pkg>/<version-or-tag> is cut from the full packument.

// maxPackumentBytes bounds a packument read into memory for rewriting; large
// packages' full packuments reach tens of megabytes.
const maxPackumentBytes = 256 << 20

// maxConcurrentPackuments bounds the packuments rewritten in memory at once.
const maxConcurrentPackuments = 8

// packumentMaxAge is how long shared caches may reuse a public repository's
// packument before revalidating (as for hosted npm repositories).
const packumentMaxAge = 60

// SetBaseURL sets the server base URL that rewritten tarball URLs point at
// (the value hosted npm repositories use). Call it before serving requests.
func (s *Service) SetBaseURL(baseURL string) { s.baseURL = strings.TrimRight(baseURL, "/") }

func (s *Service) serveNPM(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	p, err := pkgnpm.ParsePath(relPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch p.Kind {
	case pkgnpm.KindTarball:
		s.serveNPMTarball(w, r, repo, p)
	case pkgnpm.KindVersion:
		s.serveNPMPackument(w, r, repo, p, false)
	default:
		s.serveNPMPackument(w, r, repo, p, pkgnpm.WantsAbbreviated(r.Header.Get("Accept")))
	}
}

// npmPackumentKey is the cache key of a package's (abbreviated) packument.
func npmPackumentKey(pkg string, abbreviated bool) string {
	if abbreviated {
		return "-/install-v1/" + pkg
	}

	return pkg
}

// npmPackumentURL appends the package to the upstream base URL with the scope
// separator escaped (@scope%2fname), as npm sends it.
func npmPackumentURL(baseURL, pkg string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse upstream url: %w", err)
	}

	u.RawPath = strings.TrimRight(u.EscapedPath(), "/") + "/" + url.PathEscape(pkg)
	u.Path = strings.TrimRight(u.Path, "/") + "/" + pkg
	u.RawQuery = ""
	u.Fragment = ""

	return u.String(), nil
}

// resolveNPMPackument returns the cache entry of pkg's packument, fetching it
// upstream when it is absent or expired (and serving stale when the upstream
// is down).
func (s *Service) resolveNPMPackument(ctx context.Context, repo *models.Repository, pkg string, abbreviated bool) (*resolution, error) {
	key := npmPackumentKey(pkg, abbreviated)

	return s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		target, err := npmPackumentURL(repo.UpstreamURL, pkg)
		if err != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
			return nil, errInternal
		}

		accept := "application/json"
		if abbreviated {
			accept = pkgnpm.UpstreamAccept
		}

		return s.fetchURL(fctx, repo, key, target, fetchOpts{accept: accept, verify: verifyJSONObject})
	})
}

// verifyJSONObject refuses an upstream packument that is not a JSON object
// (an HTML error page served with 200, a truncated document).
func verifyJSONObject(spool io.ReadSeeker) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	var top map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(spool, maxPackumentBytes+1)).Decode(&top); err != nil || top == nil {
		return errNotPackument
	}

	return nil
}

var errNotPackument = errors.New("upstream answer is not a packument")

// readNPMPackument loads the cached packument blob.
func (s *Service) readNPMPackument(ctx context.Context, entry *models.ProxyCacheEntry) ([]byte, error) {
	rc, err := s.blobStore.Get(ctx, entry.BlobKey)
	if err != nil {
		return nil, fmt.Errorf("open cached packument: %w", err)
	}
	defer func() { base.CloseLog(rc, s.log) }()

	body, err := io.ReadAll(io.LimitReader(rc, maxPackumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read cached packument: %w", err)
	}

	if len(body) > maxPackumentBytes {
		return nil, errInternal
	}

	return body, nil
}

func (s *Service) serveNPMPackument(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgnpm.Path, abbreviated bool) {
	res, err := s.resolveNPMPackument(r.Context(), repo, p.Package, abbreviated)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	tarballBase := s.baseURL + "/repos/" + repo.Name
	h := w.Header()

	// The rendered document depends on the cached blob and the server URL; a
	// version manifest and a packument of the same blob differ by path.
	sum := sha256.Sum256([]byte(res.entry.BlobKey + "\x00" + tarballBase + "\x00" + p.Version))

	h.Add("Vary", "Accept")

	if res.stale {
		h.Set("Warning", staleWarning)
	}

	if base.Revalidate(w, r, repo.Public, `"`+hex.EncodeToString(sum[:])+`"`, packumentMaxAge) {
		return
	}

	body, err := s.renderCachedPackument(r.Context(), res.entry, p, tarballBase)
	if errors.Is(err, context.Canceled) {
		h.Del("ETag")
		h.Del("Cache-Control")

		return
	}

	switch {
	case errors.Is(err, errNoVersion):
		h.Del("ETag")
		h.Del("Cache-Control")
		http.NotFound(w, r)

		return
	case err != nil:
		s.log.Errorw("failed to render npm packument", "repo", repo.Name, "package", p.Package, "error", err)
		h.Del("ETag")
		h.Del("Cache-Control")
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}

	ct := "application/json"
	if abbreviated && strings.HasPrefix(res.entry.ContentType, pkgnpm.AbbreviatedContentType) {
		ct = pkgnpm.AbbreviatedContentType
	}

	h.Set("Content-Type", ct)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

var errNoVersion = errors.New("version not found")

// renderCachedPackument reads the cached packument and renders it for the
// client, holding one of the bounded packument slots while the whole document
// is in memory.
func (s *Service) renderCachedPackument(ctx context.Context, entry *models.ProxyCacheEntry, p pkgnpm.Path, tarballBase string) ([]byte, error) {
	if err := s.packuments.Acquire(ctx); err != nil {
		return nil, err //nolint:wrapcheck // caller checks for cancellation
	}
	defer s.packuments.Release()

	body, err := s.readNPMPackument(ctx, entry)
	if err != nil {
		return nil, err
	}

	return renderNPM(body, p, tarballBase)
}

// renderNPM points the packument's tarball URLs at this repository and, for a
// version path, cuts that version's manifest out.
func renderNPM(doc []byte, p pkgnpm.Path, tarballBase string) ([]byte, error) {
	out, err := pkgnpm.RewriteTarballs(doc, p.Package, tarballBase)
	if err != nil {
		return nil, fmt.Errorf("rewrite packument: %w", err)
	}

	if p.Kind != pkgnpm.KindVersion {
		return out, nil
	}

	manifest, found, err := pkgnpm.VersionManifest(out, p.Version)
	if err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}

	if !found {
		return nil, errNoVersion
	}

	return manifest, nil
}

func (s *Service) serveNPMTarball(w http.ResponseWriter, r *http.Request, repo *models.Repository, p pkgnpm.Path) {
	key := p.Package + "/-/" + p.File
	resolve := func() (*resolution, error) {
		return s.resolveWith(r.Context(), repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
			return s.fetchNPMTarball(fctx, repo, p, key)
		})
	}

	res, err := resolve()
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}

// fetchNPMTarball downloads p.File from the URL its version's dist object in
// the packument names, and caches it only when it matches dist.integrity /
// dist.shasum. A file no version of the packument lists is errNotFound
// without a negative cache entry: the packument may simply be stale.
func (s *Service) fetchNPMTarball(ctx context.Context, repo *models.Repository, p pkgnpm.Path, key string) (*models.ProxyCacheEntry, error) {
	res, err := s.resolveNPMPackument(ctx, repo, p.Package, false)
	if err != nil {
		return nil, err
	}

	doc, err := s.readNPMPackument(ctx, res.entry)
	if err != nil {
		s.log.Errorw("failed to read cached npm packument", "repo", repo.Name, "package", p.Package, "error", err)
		return nil, errInternal
	}

	dist, found, err := pkgnpm.FindTarball(doc, p.Package, p.File)
	if err != nil {
		s.log.Warnw("unreadable upstream npm packument", "repo", repo.Name, "package", p.Package, "error", err)
		return nil, errUpstream
	}

	if !found {
		return nil, errNotFound
	}

	var vouched bool

	return s.fetchURL(ctx, repo, key, dist.Tarball, fetchOpts{
		// Credentials go only to the configured upstream, never to a
		// host a packument points at.
		noAuth:  !sameOriginStr(repo.UpstreamURL, dist.Tarball),
		vouched: &vouched,
		verify: func(spool io.ReadSeeker) error {
			if _, serr := spool.Seek(0, io.SeekStart); serr != nil {
				return fmt.Errorf("rewind spool: %w", serr)
			}

			ok, verr := pkgnpm.VerifyDist(spool, dist)
			vouched = ok

			return verr //nolint:wrapcheck // logged by store
		},
	})
}

// sameOriginStr reports whether the URL strings a and b share scheme, host and
// port; an unparsable URL is never the same origin.
func sameOriginStr(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)

	return errA == nil && errB == nil && sameOrigin(ua, ub)
}
