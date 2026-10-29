// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
)

// The proxy pipeline.
//
// Every proxy format mirrors its upstream through the same stages; a format
// supplies only the parts that differ (which document, how to check it, how to
// read it, how to rewrite it, where a download lives):
//
//  1. Fetch: an upstream document is fetched through the SSRF-safe client
//     (fetchURL), size-capped, checked by a per-document verify hook, stored
//     content-addressed in the blob store and recorded in proxy_cache_entries.
//  2. Cache: resolveWith serves the entry until its TTL ends, collapses
//     concurrent fetches of one key into one flight, negative-caches a 404 and
//     serves a stale copy (Warning: 110) while the upstream is down.
//  3. Rewrite: a metadata document is served from the cached upstream copy,
//     re-rendered for this repository (download URLs point at the proxy, never
//     at the upstream), under the shared bounded in-memory document gate and
//     with a validator that covers the cached blob and everything the render
//     depends on (serveRendered).
//  4. Download: a file is fetched only from the location the cached metadata
//     names (never from anything the client sent), checked against the
//     digest the metadata publishes, and credentials go to the upstream's own
//     origin only (serveDownload).
//
// A document is described once by a metaDoc. serveVerbatim (stage 1 and 2
// only), serveRendered (1 to 3) and serveDownload (4, with the metadata it
// needs read through parseDoc) are the three entry points a format calls; the
// per-format files hold the hooks.

// badDoc says what a cached document that no longer parses means.
type badDoc int

const (
	// badDocInternal: the document was checked when it was fetched, so a cached
	// copy that does not parse is a bug on this side (errInternal).
	badDocInternal badDoc = iota
	// badDocUpstream: the upstream sent something the fetch could not check
	// (errUpstream); the copy stays cached.
	badDocUpstream
	// badDocDrop: like badDocUpstream, but the cache entry is dropped so the
	// next request fetches the document again instead of serving the same
	// unusable body until its TTL ends.
	badDocDrop
)

// errUnusableDoc is wrapped by a render hook to say the cached upstream
// document does not parse: serveRendered drops its cache entry and answers 502.
var errUnusableDoc = errors.New("unusable upstream document")

// metaDoc describes one upstream metadata document a proxy mirrors.
type metaDoc struct {
	// key is the cache key; it is also the upstream path unless url says
	// otherwise.
	key string
	// url maps the repository's upstream base URL to the URL the document is
	// fetched from. Nil means the base URL plus key.
	url func(upstream string) (string, error)
	// locate finds the URL from other cached metadata (a registration page
	// is where the registration index says it is). It runs inside the fetch
	// flight, takes precedence over url, and its error is returned
	// unchanged: errNotFound, errUpstream, errInternal.
	locate func(fctx context.Context) (string, error)
	// opts returns the fetch options of the document (accept header,
	// verification, content type, transform). ctx is the flight's context and
	// target the URL being fetched. Nil means no options.
	opts func(fctx context.Context, target string) fetchOpts
	// limit bounds the document held in memory; a bigger cached blob is a
	// server error.
	limit int
	// what names the document in log messages ("helm index").
	what string
	// bad is what an unparsable cached copy means (parseDoc).
	bad badDoc
}

// staticOpts is a metaDoc.opts for fixed fetch options.
func staticOpts(o fetchOpts) func(context.Context, string) fetchOpts {
	return func(context.Context, string) fetchOpts { return o }
}

// jsonDocOpts are the fetch options of a JSON document checked by verify.
func jsonDocOpts(verify func(io.ReadSeeker) error) fetchOpts {
	return fetchOpts{accept: jsonMediaType, contentType: jsonMediaType, verify: verify}
}

// fetchDoc is the fetchFunc of d.
func (s *Service) fetchDoc(repo *models.Repository, d metaDoc) fetchFunc {
	return func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		var (
			target string
			err    error
		)

		switch {
		case d.locate != nil:
			if target, err = d.locate(fctx); err != nil {
				return nil, err
			}
		case d.url != nil:
			target, err = d.url(repo.UpstreamURL)
		default:
			target, err = upstreamURL(repo.UpstreamURL, d.key)
		}

		if err != nil {
			s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
			return nil, errInternal
		}

		var o fetchOpts
		if d.opts != nil {
			o = d.opts(fctx, target)
		}

		return s.fetchURL(fctx, repo, d.key, target, o)
	}
}

// resolveDoc returns the cache entry of d, fetching it when it is absent or
// expired (and serving stale when the upstream is down).
func (s *Service) resolveDoc(ctx context.Context, repo *models.Repository, d metaDoc) (*resolution, error) {
	return s.resolveWith(ctx, repo, d.key, s.fetchDoc(repo, d))
}

// loadDoc reads the body of the resolved d, holding one of the bounded
// in-memory document slots; the caller must call release once it is done with
// the body (release is nil when err != nil). When the cache row outlived its
// blob the row is dropped and the document refetched once, with the slot given
// up for the duration of the network fetch; the returned resolution is the one
// the body belongs to.
func (s *Service) loadDoc(
	ctx context.Context, repo *models.Repository, d metaDoc, res *resolution,
) (*resolution, []byte, func(), error) {
	return s.loadCachedBody(ctx, repo, d.key, res, d.limit, d.what, func() (*resolution, error) {
		return s.resolveDoc(ctx, repo, d)
	})
}

// readDoc is resolveDoc followed by loadDoc.
func (s *Service) readDoc(ctx context.Context, repo *models.Repository, d metaDoc) (*resolution, []byte, func(), error) {
	res, err := s.resolveDoc(ctx, repo, d)
	if err != nil {
		return nil, nil, nil, err
	}

	return s.loadDoc(ctx, repo, d, res)
}

// parseDoc resolves and reads d and hands its body to parse. A document that
// does not parse is reported according to d.bad. The body must not be kept:
// parse runs while the document slot is held.
func parseDoc[T any](
	ctx context.Context,
	s *Service,
	repo *models.Repository,
	d metaDoc,
	parse func(body []byte, res *resolution) (T, error),
) (T, *resolution, error) {
	var zero T

	res, body, release, err := s.readDoc(ctx, repo, d)
	if err != nil {
		return zero, nil, err
	}

	v, perr := parse(body, res)

	release()

	if perr != nil {
		return zero, nil, s.unusableDoc(ctx, repo, d, perr)
	}

	return v, res, nil
}

// unusableDoc logs a cached copy of d that does not parse and returns the
// error that answers the request.
func (s *Service) unusableDoc(ctx context.Context, repo *models.Repository, d metaDoc, err error) error {
	switch d.bad {
	case badDocInternal:
		s.log.Errorw("cached "+d.what+" is unusable", "repo", repo.Name, "path", d.key, "error", err)
		return errInternal
	case badDocDrop:
		s.dropUnusable(ctx, repo, d, err)
	case badDocUpstream:
		s.log.Warnw("unreadable upstream "+d.what, "repo", repo.Name, "path", d.key, "error", err)
	}

	return errUpstream
}

// dropUnusable removes the cache entry of d, whose body does not parse, so it
// is fetched again rather than served until its TTL ends.
func (s *Service) dropUnusable(ctx context.Context, repo *models.Repository, d metaDoc, err error) {
	s.log.Warnw("upstream "+d.what+" unparsable", "repo", repo.Name, "path", d.key, "error", err)

	if derr := s.db.DeleteProxyCacheEntry(ctx, repo.ID, d.key); derr != nil {
		s.log.Errorw("failed to drop unparsable proxy cache entry", "repo", repo.Name, "path", d.key, "error", derr)
	}
}

// serveVerbatim answers a request for d with the upstream's own bytes: stages
// fetch (with d's verification) and cache only.
func (s *Service) serveVerbatim(w http.ResponseWriter, r *http.Request, repo *models.Repository, d metaDoc) {
	s.serveCachedFetch(w, r, repo, d.key, s.fetchDoc(repo, d))
}

// serveCachedFetch answers a request for the cached file key: it resolves the
// entry through the cache (fetch runs on a miss) and serves it, refetching once
// when the cached blob turns out to be gone.
func (s *Service) serveCachedFetch(w http.ResponseWriter, r *http.Request, repo *models.Repository, key string, fetch fetchFunc) {
	resolve := func() (*resolution, error) { return s.resolveWith(r.Context(), repo, key, fetch) }

	res, err := resolve()
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, resolve)
}

// renderedDoc is a metadata document served re-rendered for this repository
// from its cached upstream copy.
type renderedDoc struct {
	metaDoc

	// maxAge is how long shared caches may reuse the rendering of a public
	// repository.
	maxAge int
	// vary names the request header the rendering depends on (Accept), if any.
	vary string
	// etag is the validator of the rendering of the cached blob blobKey: it
	// must cover everything render depends on besides the body.
	etag func(blobKey string) string
	// render turns the cached upstream body into the response body and its
	// content type. res is the resolution the body belongs to (its entry's
	// content type tells what the upstream sent). An error wrapping
	// errUnusableDoc means the body does not parse (the entry is dropped, the
	// client gets 502); errNotFound answers 404; any other error is a 500.
	render func(body []byte, res *resolution) ([]byte, string, error)
}

// serveRendered answers a request for rd: it resolves the cached document,
// answers 304 from the validator before the document is even read, then reads
// it under the document gate and renders it.
func (s *Service) serveRendered(w http.ResponseWriter, r *http.Request, repo *models.Repository, rd renderedDoc) {
	res, err := s.resolveDoc(r.Context(), repo, rd.metaDoc)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	h := w.Header()

	if rd.vary != "" {
		h.Add("Vary", rd.vary)
	}

	if res.stale {
		h.Set("Warning", staleWarning)
	}

	if base.Revalidate(w, r, repo.Public, rd.etag(res.entry.BlobKey), rd.maxAge) {
		return
	}

	loaded, body, release, err := s.loadDoc(r.Context(), repo, rd.metaDoc, res)
	if err != nil {
		s.failWithoutValidators(w, r, err)
		return
	}

	if loaded.entry.BlobKey != res.entry.BlobKey {
		// The cached blob was gone and the document refetched: the validator
		// must describe the body actually served.
		h.Set("ETag", rd.etag(loaded.entry.BlobKey))
	}

	out, contentType, err := rd.render(body, loaded)

	release()

	if err != nil {
		s.failRender(w, r, repo, rd.metaDoc, err)
		return
	}

	writeBody(w, r, contentType, out)
}

// failRender answers a render hook's error (see renderedDoc.render).
func (s *Service) failRender(w http.ResponseWriter, r *http.Request, repo *models.Repository, d metaDoc, err error) {
	switch {
	case errors.Is(err, errNotFound):
	case errors.Is(err, errUnusableDoc):
		// Do not keep serving a body that does not parse until its TTL ends.
		s.dropUnusable(r.Context(), repo, d, err)

		err = errUpstream
	default:
		s.log.Errorw("failed to render "+d.what, "repo", repo.Name, "path", d.key, "error", err)

		err = errInternal
	}

	s.failWithoutValidators(w, r, err)
}

// serveGeneratedJSON answers a JSON document generated by the proxy (or built
// from a cached upstream one, res) with a validator over its bytes. res is nil
// for a document that comes from no upstream copy; a stale one is flagged.
func (s *Service) serveGeneratedJSON(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	res *resolution,
	body []byte,
	maxAge int,
) {
	if res != nil && res.stale {
		w.Header().Set("Warning", staleWarning)
	}

	if base.Revalidate(w, r, repo.Public, base.ContentETag(body), maxAge) {
		return
	}

	writeBody(w, r, jsonMediaType, body)
}

// writeBody writes a complete in-memory document as a 200 answer.
func writeBody(w http.ResponseWriter, r *http.Request, contentType string, body []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body) //nolint:gosec // G705: package metadata whose request-derived part is a validated host, never HTML
	}
}

// download is a file located by the cached metadata of its format.
type download struct {
	// url is where the file lives; it comes from the metadata, never from the
	// client.
	url string
	// opts carries the integrity check (wantSHA256, verify, vouched,
	// unverified) the metadata publishes for the file.
	opts fetchOpts
}

// serveDownload answers a request for the file cached under key. On a miss
// locate finds it in the cached metadata of the format: errNotFound when no
// version of the metadata lists it (without a negative cache entry, the
// metadata may simply be stale), errUpstream when the metadata is unusable. The
// URL goes through the outbound policy of fetchURL, and the repository's
// credentials are only sent when the file is on the upstream's own origin.
func (s *Service) serveDownload(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	key string,
	locate func(ctx context.Context) (download, error),
) {
	s.serveCachedFetch(w, r, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		d, err := locate(fctx)
		if err != nil {
			return nil, err
		}

		return s.fetchDownload(fctx, repo, key, d)
	})
}

// fetchDownload fetches d and caches it as key.
func (s *Service) fetchDownload(ctx context.Context, repo *models.Repository, key string, d download) (*models.ProxyCacheEntry, error) {
	o := d.opts
	// Credentials go only to the configured upstream, never to a host a
	// document points at.
	o.noAuth = o.noAuth || !sameOriginStr(repo.UpstreamURL, d.url)

	return s.fetchURL(ctx, repo, key, d.url, o)
}

// sameOriginStr reports whether the URL strings a and b share scheme, host and
// port; an unparsable URL is never the same origin. Upstream credentials are
// only ever sent to the upstream's own origin: an index may link files on a CDN.
func sameOriginStr(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)

	return errA == nil && errB == nil && strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

// readBlobCapped reads the blob blobKey in full; a blob larger than limit
// bytes is errInternal.
func (s *Service) readBlobCapped(ctx context.Context, blobKey, what string, limit int) ([]byte, error) {
	rc, err := s.blobStore.Get(ctx, blobKey)
	if err != nil {
		return nil, fmt.Errorf("open cached %s: %w", what, err)
	}
	defer func() { base.CloseLog(rc, s.log) }()

	body, err := io.ReadAll(io.LimitReader(rc, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read cached %s: %w", what, err)
	}

	if len(body) > limit {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", errInternal, what, limit)
	}

	return body, nil
}

// loadCachedBody reads the body of the resolved document at key (what names it
// in logs; limit bounds it), holding one of the bounded in-memory document
// slots; the caller must call release once it is done with the body (release
// is nil when err != nil). When the cache row outlived its blob the row is
// dropped and the document refetched once through refetch, with the slot given
// up for the duration of the network fetch.
func (s *Service) loadCachedBody(
	ctx context.Context,
	repo *models.Repository,
	key string,
	res *resolution,
	limit int,
	what string,
	refetch func() (*resolution, error),
) (*resolution, []byte, func(), error) {
	if err := s.packuments.Acquire(ctx); err != nil {
		return nil, nil, nil, err //nolint:wrapcheck // caller checks for cancellation
	}

	body, err := s.readBlobCapped(ctx, res.entry.BlobKey, what, limit)
	if err == nil {
		return res, body, s.packuments.Release, nil
	}

	s.packuments.Release()

	if !res.fromCache {
		s.log.Errorw("failed to read fetched "+what, "repo", repo.Name, "path", key, "error", err)
		return nil, nil, nil, errInternal
	}

	s.log.Warnw("proxy cache blob missing, refetching", "repo", repo.Name, "path", key, "error", err)

	if derr := s.db.DeleteProxyCacheEntry(ctx, repo.ID, key); derr != nil {
		s.log.Errorw("failed to drop stale proxy cache entry", "repo", repo.Name, "path", key, "error", derr)
	}

	res, err = refetch()
	if err != nil {
		return nil, nil, nil, err
	}

	if aerr := s.packuments.Acquire(ctx); aerr != nil {
		return nil, nil, nil, aerr //nolint:wrapcheck // caller checks for cancellation
	}

	body, err = s.readBlobCapped(ctx, res.entry.BlobKey, what, limit)
	if err != nil {
		s.packuments.Release()
		s.log.Errorw("failed to read fetched "+what, "repo", repo.Name, "path", key, "error", err)

		return nil, nil, nil, errInternal
	}

	return res, body, s.packuments.Release, nil
}
