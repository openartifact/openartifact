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
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/pathseg"
	pkghelm "github.com/openartifact/openartifact/pkg/helm"
)

// Helm proxy repositories mirror a classic chart repository (upstream_url is
// the directory that holds index.yaml, for example https://charts.jetstack.io):
//
//   - index.yaml is cached verbatim under the key "index.yaml" with the
//     repository's cache TTL and serve-stale rules. Every response rewrites
//     each chart URL (absolute, or relative to the upstream) to
//     BASE_URL/repos/<repo>/charts/<file>, so the cache stays upstream-shaped.
//     Versions that only list URLs that cannot be mirrored (oci://) are left
//     out: this server could not serve them.
//   - Charts (GET /charts/<file>) are cached under "charts/<file>", immutable
//     (or for the repository TTL when the index lists no digest for them).
//     The upstream URL and digest come from the cached index entry whose URL
//     ends in <file>; when two entries list different URLs that end in the
//     same file name, the later one is served as <name>-<hash of its URL>.<ext>
//     (see helmFileNamer); bytes that do not match its digest are refused and never
//     cached. GET /charts/<file>.prov (helm pull --verify) fetches the
//     provenance file next to the chart.
//
// Helm OCI registries are not classic repositories: use a docker proxy.

const (
	helmIndexKey = "index.yaml"

	// maxHelmIndexBytes bounds an index read into memory for rewriting; large
	// public repositories publish indexes of tens of megabytes.
	maxHelmIndexBytes = 256 << 20

	// helmIndexMaxAge is how long shared caches may reuse a public
	// repository's index.yaml before revalidating (as for hosted helm).
	helmIndexMaxAge = 60

	helmChartsDir = "charts/"
	helmProvExt   = ".prov"
)

var errNotHelmIndex = errors.New("upstream answer is not a helm index")

func (s *Service) serveHelm(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	rel := strings.TrimPrefix(relPath, "/")

	if rel == helmIndexKey {
		s.serveHelmIndex(w, r, repo)
		return
	}

	file, ok := strings.CutPrefix(rel, helmChartsDir)
	if !ok || !pathseg.Name(file) {
		http.NotFound(w, r)
		return
	}

	if _, ok := CleanPath(rel); !ok {
		http.NotFound(w, r)
		return
	}

	s.serveHelmChart(w, r, repo, file)
}

// verifyHelmIndex refuses an upstream index.yaml that does not parse as one
// (an HTML error page served with 200, a truncated document).
func verifyHelmIndex(spool io.ReadSeeker) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	body, err := io.ReadAll(io.LimitReader(spool, maxHelmIndexBytes+1))
	if err != nil {
		return fmt.Errorf("read spool: %w", err)
	}

	if len(body) > maxHelmIndexBytes {
		return errNotHelmIndex
	}

	if _, err := pkghelm.ParseIndex(body); err != nil {
		return errNotHelmIndex
	}

	return nil
}

// resolveHelmIndex returns the cache entry of the upstream index.yaml,
// fetching it when absent or expired (and serving stale when the upstream is
// down).
func (s *Service) resolveHelmIndex(ctx context.Context, repo *models.Repository) (*resolution, error) {
	return s.resolveWith(ctx, repo, helmIndexKey, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		target, err := upstreamURL(repo.UpstreamURL, helmIndexKey)
		if err != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
			return nil, errInternal
		}

		return s.fetchURL(fctx, repo, helmIndexKey, target, fetchOpts{verify: verifyHelmIndex})
	})
}

// loadHelmIndex reads the body of the resolved index, holding one of the
// bounded in-memory document slots; the caller must call release once it is
// done parsing and rendering the body (release is nil when err != nil). When
// the cache row outlived its blob the row is dropped and the index refetched
// once, with the slot given up for the duration of the network fetch.
func (s *Service) loadHelmIndex(ctx context.Context, repo *models.Repository, res *resolution) (*resolution, []byte, func(), error) {
	if err := s.packuments.Acquire(ctx); err != nil {
		return nil, nil, nil, err //nolint:wrapcheck // caller checks for cancellation
	}

	body, err := s.readHelmIndexBlob(ctx, res.entry.BlobKey)
	if err == nil {
		return res, body, s.packuments.Release, nil
	}

	s.packuments.Release()

	if !res.fromCache {
		s.log.Errorw("failed to read fetched helm index", "repo", repo.Name, "error", err)
		return nil, nil, nil, errInternal
	}

	s.log.Warnw("proxy cache blob missing, refetching", "repo", repo.Name, "path", helmIndexKey, "error", err)

	if derr := s.db.DeleteProxyCacheEntry(ctx, repo.ID, helmIndexKey); derr != nil {
		s.log.Errorw("failed to drop stale proxy cache entry", "repo", repo.Name, "path", helmIndexKey, "error", derr)
	}

	res, err = s.resolveHelmIndex(ctx, repo)
	if err != nil {
		return nil, nil, nil, err
	}

	if aerr := s.packuments.Acquire(ctx); aerr != nil {
		return nil, nil, nil, aerr //nolint:wrapcheck // caller checks for cancellation
	}

	body, err = s.readHelmIndexBlob(ctx, res.entry.BlobKey)
	if err != nil {
		s.packuments.Release()
		s.log.Errorw("failed to read fetched helm index", "repo", repo.Name, "error", err)

		return nil, nil, nil, errInternal
	}

	return res, body, s.packuments.Release, nil
}

func (s *Service) readHelmIndexBlob(ctx context.Context, blobKey string) ([]byte, error) {
	rc, err := s.blobStore.Get(ctx, blobKey)
	if err != nil {
		return nil, fmt.Errorf("open cached helm index: %w", err)
	}
	defer func() { base.CloseLog(rc, s.log) }()

	body, err := io.ReadAll(io.LimitReader(rc, maxHelmIndexBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read cached helm index: %w", err)
	}

	if len(body) > maxHelmIndexBytes {
		return nil, fmt.Errorf("%w: index exceeds %d bytes", errInternal, maxHelmIndexBytes)
	}

	return body, nil
}

// helmIndexETag identifies one rendering of a cached index: the upstream body
// and everything the rewritten URLs derive from.
func (s *Service) helmIndexETag(repo *models.Repository, blobKey string) string {
	sum := sha256.Sum256([]byte(blobKey + "\x00" + s.baseURL + "\x00" + repo.Name + "\x00" + repo.UpstreamURL))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (s *Service) serveHelmIndex(w http.ResponseWriter, r *http.Request, repo *models.Repository) {
	res, err := s.resolveHelmIndex(r.Context(), repo)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	h := w.Header()

	if res.stale {
		h.Set("Warning", staleWarning)
	}

	if base.Revalidate(w, r, repo.Public, s.helmIndexETag(repo, res.entry.BlobKey), helmIndexMaxAge) {
		return
	}

	loaded, body, release, err := s.loadHelmIndex(r.Context(), repo, res)
	if err != nil {
		s.failWithoutValidators(w, r, err)
		return
	}

	if loaded.entry.BlobKey != res.entry.BlobKey {
		// The cached blob was gone and the index refetched: the validator
		// must describe the body actually served.
		h.Set("ETag", s.helmIndexETag(repo, loaded.entry.BlobKey))
	}

	out, err := s.renderHelmIndex(repo, body)
	release()

	if err != nil {
		if errors.Is(err, errNotHelmIndex) {
			// Do not keep serving a body that does not parse until its TTL ends.
			s.log.Warnw("upstream helm index unparsable", "repo", repo.Name, "error", err)

			if derr := s.db.DeleteProxyCacheEntry(r.Context(), repo.ID, helmIndexKey); derr != nil {
				s.log.Errorw("failed to drop unparsable proxy cache entry", "repo", repo.Name, "path", helmIndexKey, "error", derr)
			}

			err = errUpstream
		} else {
			s.log.Errorw("failed to render helm index", "repo", repo.Name, "error", err)
		}

		s.failWithoutValidators(w, r, err)

		return
	}

	h.Set("Content-Type", "application/yaml")
	h.Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(out)
	}
}

// helmUpstreamBase is the upstream repository URL with a trailing slash:
// relative chart URLs resolve against it, as helm does.
func helmUpstreamBase(upstream string) (*url.URL, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}

	u.Path = strings.TrimRight(u.Path, "/") + "/"
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""

	return u, nil
}

// resolveChartURL resolves a chart URL of the index against the upstream. Only
// http(s) charts can be mirrored.
func resolveChartURL(upstream *url.URL, raw string) (*url.URL, bool) {
	ref, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}

	target := upstream.ResolveReference(ref)
	if target.Scheme != "http" && target.Scheme != "https" {
		return nil, false
	}

	target.Fragment = ""

	return target, true
}

// renderHelmIndex points every chart URL of the cached upstream index at this
// repository.
func (s *Service) renderHelmIndex(repo *models.Repository, body []byte) ([]byte, error) {
	ix, err := pkghelm.ParseIndex(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNotHelmIndex, err)
	}

	upstream, err := helmUpstreamBase(repo.UpstreamURL)
	if err != nil {
		return nil, err
	}

	chartBase := s.baseURL + "/repos/" + repo.Name + "/" + helmChartsDir

	var namer helmFileNamer

	ix.RewriteURLs(func(cv pkghelm.ChartVersion) []string {
		var out []string

		for _, raw := range cv.URLs {
			target, ok := resolveChartURL(upstream, raw)
			if !ok {
				continue
			}

			file, ok := namer.name(target)
			if !ok {
				continue
			}

			if u := chartBase + url.PathEscape(file); !slices.Contains(out, u) {
				out = append(out, u)
			}
		}

		return out
	})

	out, err := ix.Marshal()
	if err != nil {
		return nil, fmt.Errorf("render helm index: %w", err)
	}

	return out, nil
}

func (s *Service) serveHelmChart(w http.ResponseWriter, r *http.Request, repo *models.Repository, file string) {
	key := helmChartsDir + file
	resolve := func() (*resolution, error) {
		return s.resolveWith(r.Context(), repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
			return s.fetchHelmChart(fctx, repo, file, key)
		})
	}

	res, err := resolve()
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}

// fetchHelmChart downloads file from the URL the cached index lists for it and
// caches it only when it matches the entry's digest. A file no version of the
// index lists is errNotFound without a negative cache entry: the index may
// simply be stale.
func (s *Service) fetchHelmChart(ctx context.Context, repo *models.Repository, file, key string) (*models.ProxyCacheEntry, error) {
	res, err := s.resolveHelmIndex(ctx, repo)
	if err != nil {
		return nil, err
	}

	_, body, release, err := s.loadHelmIndex(ctx, repo, res)
	if err != nil {
		return nil, err
	}

	ix, err := pkghelm.ParseIndex(body)
	release()
	if err != nil {
		s.log.Warnw("unreadable upstream helm index", "repo", repo.Name, "error", err)
		return nil, errUpstream
	}

	upstream, err := helmUpstreamBase(repo.UpstreamURL)
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		return nil, errInternal
	}

	target, digest, found := locateHelmFile(ix, upstream, file)
	if !found {
		return nil, errNotFound
	}

	want, ok := helmDigest(digest)
	if !ok {
		s.log.Warnw("upstream helm index lists an unusable digest, refusing", "repo", repo.Name, "path", key, "digest", digest)
		return nil, errVerify
	}

	return s.fetchURL(ctx, repo, key, target.String(), fetchOpts{
		// Credentials go only to the configured upstream, never to a host
		// an index points at.
		noAuth:     !sameOrigin(target, upstream),
		wantSHA256: want,
		unverified: want == "",
	})
}

// locateHelmFile returns where the charts/<file> of the index lives upstream
// and the digest it must have. A <chart>.prov file is not listed in an index:
// it sits next to its chart and has no digest.
func locateHelmFile(ix *pkghelm.Index, upstream *url.URL, file string) (*url.URL, string, bool) {
	if target, digest, found := findHelmChart(ix, upstream, file); found {
		return target, digest, true
	}

	chart, isProv := strings.CutSuffix(file, helmProvExt)
	if !isProv {
		return nil, "", false
	}

	target, _, found := findHelmChart(ix, upstream, chart)
	if !found {
		return nil, "", false
	}

	prov := *target
	prov.Path += helmProvExt
	prov.RawPath = ""

	return &prov, "", true
}

// findHelmChart returns the URL and digest of the first index entry that lists
// a chart URL ending in file.
func findHelmChart(ix *pkghelm.Index, upstream *url.URL, file string) (*url.URL, string, bool) {
	var namer helmFileNamer

	for cv := range ix.Versions() {
		for _, raw := range cv.URLs {
			target, ok := resolveChartURL(upstream, raw)
			if !ok {
				continue
			}

			if f, ok := namer.name(target); ok && f == file {
				return target, cv.Digest, true
			}
		}
	}

	return nil, "", false
}

// helmFileNamer assigns every chart URL of an index the file name it is served
// under (charts/<file>). That is the last segment of the URL path, so the first
// entry to claim a name keeps it; a later entry listing a different URL with
// the same last segment (stable/foo-1.0.0.tgz and incubator/foo-1.0.0.tgz)
// gets the name with a hash of its URL before the extension, instead of
// resolving to the first one's bytes. Rendering and lookup walk the index in
// the same document order, so they agree on every name.
type helmFileNamer struct {
	owner map[string]string // served file name -> URL that claimed it
}

// name returns the file name target is served under; false when the URL has no
// usable file name or both candidate names belong to other URLs.
func (n *helmFileNamer) name(target *url.URL) (string, bool) {
	file, ok := pkghelm.ChartFile(target)
	if !ok {
		return "", false
	}

	raw := target.String()

	if n.owner == nil {
		n.owner = make(map[string]string)
	}

	sum := sha256.Sum256([]byte(raw))
	ext := path.Ext(file)

	for _, candidate := range []string{file, strings.TrimSuffix(file, ext) + "-" + hex.EncodeToString(sum[:4]) + ext} {
		owner, taken := n.owner[candidate]
		if !taken {
			n.owner[candidate] = raw
			return candidate, true
		}

		if owner == raw {
			return candidate, true
		}
	}

	return "", false
}

// helmDigest normalizes an index digest to lowercase SHA-256 hex. An absent
// digest is accepted unverified (ok, ""); a present one that is not SHA-256
// hex is refused.
func helmDigest(digest string) (string, bool) {
	d := strings.ToLower(strings.TrimSpace(digest))
	d = strings.TrimPrefix(d, "sha256:")

	if d == "" {
		return "", true
	}

	if raw, err := hex.DecodeString(d); err != nil || len(raw) != sha256.Size {
		return "", false
	}

	return d, true
}
