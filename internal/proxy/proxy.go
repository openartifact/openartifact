// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package proxy serves proxy repositories: files are fetched from the
// repository's upstream on first request, cached content-addressed in the blob
// store, recorded in proxy_cache_entries (which also backs the repository's
// package listing) and served from the cache until their TTL expires. When an
// expired entry cannot be revalidated because the upstream is down, it is
// served stale for up to the repository's max_stale_seconds.
package proxy

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// DefaultMaxBody caps any single file fetched from an upstream.
	DefaultMaxBody int64 = 4 << 30 // 4 GiB

	// fetchTimeout bounds one upstream fetch, independent of any client.
	fetchTimeout = 15 * time.Minute

	// responseHeaderTimeout bounds the wait for an upstream's response
	// headers (the dial and TLS handshake are bounded to 10s each by the
	// SSRF-safe client). A hung upstream fails fast, as a network error, so
	// a stale copy can be served; the body transfer is only bound by
	// fetchTimeout.
	responseHeaderTimeout = 30 * time.Second

	// maxConcurrentFetches bounds the upstream downloads in flight at once
	// across every repository. Each holds an upstream connection and a spool
	// file of up to maxBody bytes; singleflight only collapses requests for
	// the same key, so a burst of distinct keys (a cold CI cache, a crawler)
	// would otherwise open one connection and one temp file per key.
	maxConcurrentFetches = 16

	// immutableTTL is how long immutable files (released Maven artifacts,
	// deb/rpm package files) stay cached: effectively until evicted by an
	// admin or repository delete.
	immutableTTL = 100 * 365 * 24 * time.Hour

	maxPathLen = 2048
)

var (
	errNotFound = errors.New("not found upstream")
	errUpstream = errors.New("upstream error")
	errInternal = errors.New("internal error")

	// errUpstreamDown is the errUpstream subset that justifies serving a stale
	// cached copy: the upstream was unreachable, timed out, answered 5xx or
	// broke the transfer. Refusals that signal misconfiguration (401/403, an
	// oversized file, a blocked address) are plain errUpstream.
	errUpstreamDown = fmt.Errorf("%w: unavailable", errUpstream)

	// errVerify is an upstream body that failed its integrity check (checksum
	// or format): the upstream answered, wrongly, so no stale copy is served
	// in its place.
	errVerify = fmt.Errorf("%w: integrity check failed", errUpstream)
)

// staleWarning marks a response served from an expired cache entry
// (RFC 7234 section 5.5.1, "Response is Stale").
const staleWarning = `110 - "Response is Stale"`

// Service fetches and caches upstream files for proxy repositories.
type Service struct {
	log       *logger.Logger
	db        database.Database
	blobStore storage.BlobStore
	client    *http.Client
	flights   flightGroup
	tokens    tokenCache // docker upstream bearer tokens

	// baseURL is the server URL rewritten metadata points at (npm tarballs).
	baseURL string

	// dockerDigestTTL is how long a docker manifest or blob addressed by digest
	// stays cached after it was last used; 0 keeps them until an admin purge or
	// the repository's removal (see WithDockerDigestTTL).
	dockerDigestTTL time.Duration

	// sumdb verifies Go module files against the checksum database; nil
	// (GOSUMDB=off) verifies their structure only.
	sumdb *sumDB

	nuget nugetIndexCache // parsed NuGet upstream service indexes

	// packuments bounds every upstream document held in memory at once (npm
	// packuments, PyPI pages, Helm indexes, NuGet/OCI documents, cargo
	// indexes): each is read whole and parsed or re-encoded per request.
	packuments base.Gate

	// fetches bounds the upstream downloads (request, body spool, blob
	// store) running at once. It is held only around that network section,
	// never across a nested resolve, so a fetch that needs another cached
	// file cannot starve itself of a slot.
	fetches base.Gate

	// maxBody caps a single upstream response; DefaultMaxBody unless a test
	// lowers it.
	maxBody int64
}

// NewService creates the proxy service. Upstream requests use an SSRF-safe
// client: blocked destination addresses are refused at dial time and on every
// redirect hop.
func NewService(log *logger.Logger, db database.Database, blobStore storage.BlobStore) *Service {
	client := telemetry.WrapClient(netutil.SafeHTTPClientWithHeaderTimeout(fetchTimeout, responseHeaderTimeout))

	return &Service{
		log:        log,
		db:         db,
		blobStore:  blobStore,
		client:     client,
		maxBody:    DefaultMaxBody,
		packuments: base.NewGate(maxConcurrentPackuments),
		fetches:    base.NewGate(maxConcurrentFetches),
		sumdb:      newSumDB(os.Getenv("GOSUMDB"), client, log),
	}
}

// WithDockerDigestTTL sets how long a docker manifest or blob addressed by
// digest stays cached without being used. Such an entry never changes, so
// until now it never expired; with a positive ttl the cache worker evicts it
// (and its blob, once no other entry references it) after ttl without a pull,
// and every pull that finds it past half its lifetime renews it. 0 keeps the
// old behavior. It must be called before the service serves requests.
func (s *Service) WithDockerDigestTTL(ttl time.Duration) *Service {
	s.dockerDigestTTL = max(ttl, 0)
	return s
}

// Serve answers a GET/HEAD for relPath (the path below the repository root)
// of proxy repo.
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, repo *models.Repository, relPath string) {
	if repo.Blocked {
		http.Error(w, "repository is blocked", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "proxy repositories are read-only", http.StatusMethodNotAllowed)

		return
	}

	if repo.Format == "pypi" {
		s.servePyPI(w, r, repo, relPath)
		return
	}

	if repo.Format == "npm" {
		s.serveNPM(w, r, repo, relPath)
		return
	}

	if repo.Format == "helm" {
		s.serveHelm(w, r, repo, relPath)
		return
	}

	if repo.Format == "cargo" {
		s.serveCargo(w, r, repo, relPath)
		return
	}

	if repo.Format == "go" {
		s.serveGo(w, r, repo, relPath)
		return
	}

	if repo.Format == "nuget" {
		s.serveNuGet(w, r, repo, relPath)
		return
	}

	key, ok := CleanPath(relPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	res, err := s.resolve(r.Context(), repo, key)
	if err != nil {
		s.writeResolveError(w, r, err)
		return
	}

	s.serveResolved(w, r, repo, key, res, func() (*resolution, error) {
		return s.resolve(r.Context(), repo, key)
	})
}

// serveResolved streams the cached file of res to the client after the
// quarantine check. refetch resolves key again; it is used once when the cache
// row outlived its blob.
func (s *Service) serveResolved(w http.ResponseWriter, r *http.Request, repo *models.Repository, key string, res *resolution, refetch func() (*resolution, error)) {
	entry, fromCache := res.entry, res.fromCache

	if quarantine.Enforce(w, r, s.db, repo.ID, quarantine.PathKeys(key, entry.ID)...) {
		return
	}

	// A binary download of a cached (or just fetched) file is answered with a
	// redirect to the object store when presigned downloads are enabled.
	// Metadata files and stale copies (which carry a Warning header) stream.
	if !res.stale && presign.IsProxyBlobPath(repo.Format, key) &&
		presign.Try(s.blobStore, w, r, presign.Blob{
			SHA256:      entry.BlobKey,
			Size:        entry.SizeBytes,
			Filename:    path.Base(key),
			ContentType: entry.ContentType,
			Repository:  repo,
			CheckExists: true,
		}) {
		return
	}

	rc, err := s.blobStore.Get(r.Context(), entry.BlobKey)
	if err != nil && fromCache {
		// The cache row outlived its blob (storage wiped or restored from an
		// older backup): drop the row and refetch once.
		s.log.Warnw("proxy cache blob missing, refetching", "repo", repo.Name, "path", key, "error", err)

		if derr := s.db.DeleteProxyCacheEntry(r.Context(), repo.ID, key); derr != nil {
			s.log.Errorw("failed to drop stale proxy cache entry", "repo", repo.Name, "path", key, "error", derr)
		}

		res, err = refetch()
		if err != nil {
			s.writeResolveError(w, r, err)
			return
		}

		entry = res.entry

		if quarantine.Enforce(w, r, s.db, repo.ID, quarantine.PathKeys(key, entry.ID)...) {
			return
		}

		rc, err = s.blobStore.Get(r.Context(), entry.BlobKey)
	}

	if err != nil {
		s.log.Errorw("failed to open cached blob", "repo", repo.Name, "path", key, "blob_key", entry.BlobKey, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)

		return
	}
	defer func() { base.CloseLog(rc, s.log) }()

	if res.stale {
		w.Header().Set("Warning", staleWarning)
	}

	writeEntry(w, r, entry, rc)
}

func (s *Service) writeResolveError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNotFound):
		http.NotFound(w, r)
	case errors.Is(err, errUpstream):
		http.Error(w, "upstream repository error", http.StatusBadGateway)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The client went away; nothing useful to write.
		s.log.Debugw("proxy request abandoned", "error", err)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func writeEntry(w http.ResponseWriter, r *http.Request, entry *models.ProxyCacheEntry, body io.Reader) {
	etag := `"` + entry.BlobKey + `"`
	h := w.Header()

	ct := entry.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}

	h.Set("Content-Type", ct)
	h.Set("ETag", etag)

	if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	h.Set("Content-Length", strconv.FormatInt(entry.SizeBytes, 10))
	w.WriteHeader(http.StatusOK)

	if r.Method == http.MethodHead {
		return
	}

	// The status line is already out: a failed copy can only abort the body.
	_, _ = io.Copy(w, body)
}

// resolution is the outcome of resolve.
type resolution struct {
	entry *models.ProxyCacheEntry
	// fromCache reports that no upstream response was stored for this request:
	// the entry was fresh, or an expired one served stale.
	fromCache bool
	// stale reports that entry is past its TTL and was served because the
	// upstream could not be reached.
	stale bool
}

// resolve returns a cache entry for key, fetching it from upstream when it is
// absent or expired. When revalidating an expired entry fails because the
// upstream is down, the stale entry is returned (resolution.stale) as long as
// it is within the repository's max-stale window. errNotFound is returned for
// an upstream 404 (negatively cached).
func (s *Service) resolve(ctx context.Context, repo *models.Repository, key string) (*resolution, error) {
	return s.resolveWith(ctx, repo, key, func(fctx context.Context) (*models.ProxyCacheEntry, error) {
		return s.fetch(fctx, repo, key)
	})
}

// fetchFunc downloads and caches one key; ctx is the flight's context.
type fetchFunc func(ctx context.Context) (*models.ProxyCacheEntry, error)

// resolveWith is resolve with a custom upstream fetch (formats whose upstream
// URL is not simply the repository URL plus key).
func (s *Service) resolveWith(ctx context.Context, repo *models.Repository, key string, fetch fetchFunc) (*resolution, error) {
	fresh, _, err := s.lookup(ctx, repo, key)
	if err != nil {
		return nil, err
	}

	if fresh != nil {
		return &resolution{entry: fresh, fromCache: true}, nil
	}

	res, err := s.flights.do(ctx, repo.ID+"\x00"+key, func() (*resolution, error) {
		// The flight outlives the request that started it: other requests
		// are waiting on it.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()

		// A flight that finished just before this one started has already
		// filled the cache.
		fresh, stale, lerr := s.lookup(fctx, repo, key)
		if lerr != nil {
			return nil, lerr
		}

		if fresh != nil {
			return &resolution{entry: fresh, fromCache: true}, nil
		}

		entry, ferr := fetch(fctx)
		if ferr == nil {
			return &resolution{entry: entry}, nil
		}

		if errors.Is(ferr, errUpstreamDown) && s.staleUsable(repo, stale) {
			s.log.Warnw("proxy upstream unavailable, serving stale cached copy",
				"repo", repo.Name, "path", key, "expired_for", time.Since(stale.ExpiresAt).Round(time.Second).String(), "error", ferr)

			return &resolution{entry: stale, fromCache: true, stale: true}, nil
		}

		return nil, ferr
	})
	if err != nil {
		return nil, err
	}

	if res == nil {
		// The flight leader died (panic) before producing a result.
		return nil, errInternal
	}

	return res, nil
}

// lookup reads the cache row for key. A fresh positive entry is returned as
// fresh; an expired positive entry (a candidate for serving stale) as stale;
// a fresh negative entry yields errNotFound. Both are nil when the caller has
// to fetch with nothing to fall back on.
func (s *Service) lookup(ctx context.Context, repo *models.Repository, key string) (*models.ProxyCacheEntry, *models.ProxyCacheEntry, error) {
	entry, err := s.db.GetProxyCacheEntry(ctx, repo.ID, key)
	if err != nil {
		s.log.Errorw("failed to read proxy cache entry", "repo", repo.Name, "path", key, "error", err)
		return nil, nil, errInternal
	}

	if entry == nil {
		return nil, nil, nil
	}

	positive := entry.StatusCode != http.StatusNotFound && entry.BlobKey != ""

	if !entry.ExpiresAt.After(time.Now()) {
		if positive {
			return nil, entry, nil
		}

		return nil, nil, nil
	}

	if !positive {
		return nil, nil, errNotFound
	}

	return entry, nil, nil
}

// staleUsable reports whether an expired entry may still be served: it exists
// and expired less than the repository's max_stale_seconds ago.
func (s *Service) staleUsable(repo *models.Repository, entry *models.ProxyCacheEntry) bool {
	if entry == nil || repo.MaxStaleSeconds <= 0 {
		return false
	}

	return time.Since(entry.ExpiresAt) < time.Duration(repo.MaxStaleSeconds)*time.Second
}

// fetch downloads key from the upstream, stores the blob and records the cache
// entry. It returns errNotFound for an upstream 404/410 and errUpstream for
// anything else the upstream answers or fails with.
func (s *Service) fetch(ctx context.Context, repo *models.Repository, key string) (*models.ProxyCacheEntry, error) {
	target, err := upstreamURL(repo.UpstreamURL, key)
	if err != nil {
		s.log.Errorw("invalid proxy upstream url", "repo", repo.Name, "error", err)
		return nil, errInternal
	}

	return s.fetchURL(ctx, repo, key, target, fetchOpts{})
}

// fetchOpts tunes one upstream fetch.
type fetchOpts struct {
	// accept overrides the Accept request header (default "*/*").
	accept string
	// noAuth withholds the repository's upstream credentials, for targets
	// outside the configured upstream host.
	noAuth bool
	// wantSHA256, when set, is the lowercase hex digest the body must have;
	// a different body is refused and not cached.
	wantSHA256 string
	// unverified marks a body that no published digest vouches for (the
	// upstream lists none): it is cached for the repository TTL, never as
	// immutable, so a wrong or tampered copy is replaced on revalidation.
	unverified bool
	// vouched, when set, receives whether verify checked the body against a
	// published digest. A body it leaves false is cached for the repository
	// TTL, like unverified. Only set it on files whose digest can be unknown.
	vouched *bool
	// verify, when set, inspects the spooled body before it is stored; an
	// error refuses the body. The spool may be read freely: it is rewound
	// afterwards.
	verify func(spool io.ReadSeeker) error
}

// outboundError classifies a failed outbound URL check. A host that does not
// resolve is an unreachable upstream (a resolver or network outage), so a
// stale cached copy may stand in; every other rejection (blocked address,
// scheme, malformed URL) is a refusal that must not be papered over.
func outboundError(err error) error {
	if errors.Is(err, netutil.ErrCannotResolve) {
		return errUpstreamDown
	}

	return errUpstream
}

// fetchURL is fetch for an explicit target URL.
func (s *Service) fetchURL(ctx context.Context, repo *models.Repository, key, target string, o fetchOpts) (*models.ProxyCacheEntry, error) {
	if verr := netutil.ValidateOutboundURLContext(ctx, target); verr != nil {
		s.log.Warnw("proxy upstream rejected by outbound URL policy", "repo", repo.Name, "error", verr)
		return nil, outboundError(verr)
	}

	req, err := s.newUpstreamRequest(ctx, repo, target, o)
	if err != nil {
		return nil, err
	}

	if aerr := s.acquireFetch(ctx, repo, key); aerr != nil {
		return nil, aerr
	}
	defer s.fetches.Release()

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warnw("proxy upstream request failed", "repo", repo.Name, "path", key, "error", err)
		return nil, errUpstreamDown
	}
	defer func() { base.CloseLog(resp.Body, s.log) }()

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		s.recordNegative(ctx, repo, key)
		return nil, errNotFound
	case resp.StatusCode >= http.StatusInternalServerError:
		s.log.Warnw("proxy upstream returned an error", "repo", repo.Name, "path", key, "status", resp.StatusCode)
		return nil, errUpstreamDown
	case resp.StatusCode != http.StatusOK:
		s.log.Warnw("proxy upstream returned an error", "repo", repo.Name, "path", key, "status", resp.StatusCode)
		return nil, errUpstream
	}

	return s.store(ctx, repo, key, resp, o)
}

// acquireFetch takes a slot of the upstream-download gate; the caller releases
// it with s.fetches.Release when its body has been spooled and stored. Only the
// flight leader of a key gets here (waiters of the same key wait on the flight,
// holding nothing), and callers invoke it right before the network request and
// after any nested resolve, so no holder ever waits for another slot.
//
// A wait that ends with the context (the flight's fetchTimeout) is reported
// like any other transport failure of the upstream, errUpstreamDown: the
// upstream could not be served in time, a stale copy may stand in, and the
// client gets the 502 it gets for an unreachable upstream rather than a
// silent empty answer.
func (s *Service) acquireFetch(ctx context.Context, repo *models.Repository, key string) error {
	if err := s.fetches.Acquire(ctx); err != nil {
		s.log.Warnw("proxy upstream fetch queue wait ended", "repo", repo.Name, "path", key, "error", err)
		return errUpstreamDown
	}

	return nil
}

func (s *Service) newUpstreamRequest(ctx context.Context, repo *models.Repository, target string, o fetchOpts) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		s.log.Errorw("failed to build proxy upstream request", "repo", repo.Name, "error", err)
		return nil, errInternal
	}

	req.Header.Set("User-Agent", "OpenArtifact-Proxy")
	req.Header.Set("Accept", cmp.Or(o.accept, "*/*"))
	// Mirror bytes exactly: never let the transport transparently decompress.
	req.Header.Set("Accept-Encoding", "identity")

	if repo.UpstreamUsername != "" && !o.noAuth {
		password := ""

		if repo.UpstreamPasswordEnc != "" {
			password, err = crypto.Decrypt(repo.UpstreamPasswordEnc)
			if err != nil {
				s.log.Errorw("failed to decrypt proxy upstream password", "repo", repo.Name, "error", err)
				return nil, errInternal
			}
		}

		req.SetBasicAuth(repo.UpstreamUsername, password)
	}

	return req, nil
}

// store spools the response, enforcing the size limit, then writes the blob
// and cache entry under the blob lock.
func (s *Service) store(ctx context.Context, repo *models.Repository, key string, resp *http.Response, o fetchOpts) (*models.ProxyCacheEntry, error) {
	if resp.ContentLength > s.maxBody {
		s.log.Warnw("proxy upstream file exceeds size limit", "repo", repo.Name, "path", key, "size", resp.ContentLength)
		return nil, errUpstream
	}

	if isHTMLForPackage(repo.Format, key, resp) {
		s.log.Warnw("proxy upstream answered a package path with an HTML page, refusing",
			"repo", repo.Name, "path", key, "content_type", resp.Header.Get("Content-Type"))

		return nil, errVerify
	}

	spool, err := os.CreateTemp("", "openartifact-proxy-*")
	if err != nil {
		s.log.Errorw("failed to create proxy spool file", "error", err)
		return nil, errInternal
	}

	defer func() {
		base.CloseLog(spool, s.log)

		if rerr := os.Remove(spool.Name()); rerr != nil {
			s.log.Warnw("failed to remove proxy spool file", "error", rerr)
		}
	}()

	hasher := sha256.New()

	size, err := io.Copy(io.MultiWriter(spool, hasher), io.LimitReader(resp.Body, s.maxBody+1))
	if err != nil {
		s.log.Warnw("proxy upstream body read failed", "repo", repo.Name, "path", key, "error", err)
		return nil, errUpstreamDown
	}

	if size > s.maxBody {
		s.log.Warnw("proxy upstream body oversized", "repo", repo.Name, "path", key, "size", size)
		return nil, errUpstream
	}

	if resp.ContentLength >= 0 && size != resp.ContentLength {
		s.log.Warnw("proxy upstream body truncated", "repo", repo.Name, "path", key, "size", size)
		return nil, errUpstreamDown
	}

	sum := hex.EncodeToString(hasher.Sum(nil))

	if o.wantSHA256 != "" && !strings.EqualFold(o.wantSHA256, sum) {
		s.log.Warnw("proxy upstream file does not match its published checksum, refusing",
			"repo", repo.Name, "path", key, "want_sha256", o.wantSHA256, "got_sha256", sum)

		return nil, errVerify
	}

	if o.verify != nil {
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			s.log.Errorw("failed to rewind proxy spool file", "error", err)
			return nil, errInternal
		}

		if verr := o.verify(spool); verr != nil {
			s.log.Warnw("proxy upstream file failed verification, refusing", "repo", repo.Name, "path", key, "error", verr)
			return nil, errVerify
		}
	}

	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		s.log.Errorw("failed to rewind proxy spool file", "error", err)
		return nil, errInternal
	}

	contentType := resp.Header.Get("Content-Type")
	now := time.Now()

	entry := &models.ProxyCacheEntry{
		RepositoryID: repo.ID,
		CacheKey:     key,
		BlobKey:      sum,
		ContentType:  contentType,
		StatusCode:   http.StatusOK,
		SizeBytes:    size,
		ExpiresAt:    now.Add(ttlFor(repo, key, o.pinnable(resp))),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	mediaType := contentType
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}

	if perr := s.persist(ctx, spool, sum, mediaType, entry); perr != nil {
		s.log.Errorw("failed to cache proxied file", "repo", repo.Name, "path", key, "sha256", sum, "error", perr)
		return nil, errInternal
	}

	return entry, nil
}

// persist stores the spooled body (rewound, hashing to sum) in the blob store
// and records the blobs row and cache entry.
//
// The object-store upload runs OUTSIDE the per-blob lock. The lock is a
// database transaction holding a pooled connection and one of the bounded lock
// slots (postgres lockSlots) until it ends; held across an upload of up to
// maxBody bytes it would let a handful of slow transfers exhaust the slots and
// stall every upload, delete and cache fill. The blob is content-addressed and
// Put is idempotent (the same bytes under the same id), so storing it first is
// safe by itself.
//
// What the lock must still guarantee is that the blob is in the store when the
// referencing row is committed, because the only remover racing with us is
// storage.SafeDeleteBlob / DeleteBlobIfUnreferenced (also used by the orphan
// collector), which delete the object under this same lock when no row
// references the sha. The collector never sees an object without a blobs row,
// and a fresh row is protected by storage.OrphanMinAge, but a blob whose
// previous references were just removed (a deleted hosted package with
// identical bytes, an orphan older than the grace) can be deleted between our
// Put and the lock. So the existence of the object is re-checked under the
// lock, after EnsureBlob and before the cache entry (a reference) is written;
// if it vanished it is stored again, now under the lock, which is rare and
// keeps the delete from ever leaving a cache row without its blob.
func (s *Service) persist(ctx context.Context, spool *os.File, sum, mediaType string, entry *models.ProxyCacheEntry) error {
	meta := map[string]string{"sha256": sum, "content-type": mediaType}

	if perr := s.putBlob(ctx, spool, sum, meta); perr != nil {
		return perr
	}

	return storage.WithBlobLock(ctx, s.db, sum, func(ctx context.Context) error {
		if _, eerr := s.db.EnsureBlob(ctx, sum, mediaType, entry.SizeBytes); eerr != nil {
			return fmt.Errorf("record blob: %w", eerr)
		}

		present, xerr := s.blobStore.Exists(ctx, sum)
		if xerr != nil {
			return fmt.Errorf("check blob: %w", xerr)
		}

		if !present {
			if perr := s.putBlob(ctx, spool, sum, meta); perr != nil {
				return perr
			}
		}

		if uerr := s.db.UpsertProxyCacheEntry(ctx, entry); uerr != nil {
			return fmt.Errorf("record cache entry: %w", uerr)
		}

		return nil
	})
}

// putBlob uploads the spool (from its start) as the blob sum.
func (s *Service) putBlob(ctx context.Context, spool io.ReadSeeker, sum string, meta map[string]string) error {
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind spool: %w", err)
	}

	blobID, err := s.blobStore.Put(ctx, spool, meta)
	if err != nil {
		return fmt.Errorf("store blob: %w", err)
	}

	if blobID != sum {
		return fmt.Errorf("%w: blob store returned id %q for sha256 %s", errInternal, blobID, sum)
	}

	return nil
}

func (s *Service) recordNegative(ctx context.Context, repo *models.Repository, key string) {
	ttl := time.Duration(repo.NegativeCacheTTLSeconds) * time.Second
	if ttl <= 0 {
		// Nothing to negative-cache, but an expired positive entry must not
		// outlive the upstream's 404/410 as a stale fallback.
		if err := s.db.DeleteProxyCacheEntry(ctx, repo.ID, key); err != nil {
			s.log.Errorw("failed to drop proxy cache entry for removed upstream file", "repo", repo.Name, "path", key, "error", err)
		}

		return
	}

	now := time.Now()

	err := s.db.UpsertProxyCacheEntry(ctx, &models.ProxyCacheEntry{
		RepositoryID: repo.ID,
		CacheKey:     key,
		StatusCode:   http.StatusNotFound,
		ExpiresAt:    now.Add(ttl),
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		s.log.Errorw("failed to record negative proxy cache entry", "repo", repo.Name, "path", key, "error", err)
	}
}

// completeBody reports whether the response framing proves the body arrived
// whole: a declared Content-Length (checked against the bytes read) or chunked
// encoding (a missing terminating chunk is a read error). A close-delimited
// body cut by a clean FIN looks complete, so it must not be cached for good.
func completeBody(resp *http.Response) bool {
	return resp.ContentLength >= 0 || slices.Contains(resp.TransferEncoding, "chunked")
}

// pinnable reports whether the response may be cached as immutable: its body is
// known to be complete and the file was not accepted without a digest to check
// it against (unverified) or with a digest that is not on record (vouched).
func (o fetchOpts) pinnable(resp *http.Response) bool {
	return completeBody(resp) && !o.unverified && (o.vouched == nil || *o.vouched)
}

// ttlFor picks the cache lifetime. Immutable files are cached (almost) forever
// only when their body is known to be complete and, where the upstream
// publishes a digest, was checked against it; otherwise the repository TTL
// applies so a truncated or unverified copy is revalidated.
func ttlFor(repo *models.Repository, key string, pinnable bool) time.Duration {
	if pinnable && Immutable(repo.Format, key) {
		return immutableTTL
	}

	return time.Duration(repo.CacheTTLSeconds) * time.Second
}

// Immutable reports whether the file at key never changes once published, so a
// cached copy never needs revalidation. Formats whose bodies are verified
// against a published digest (see fetchOpts) pin by name alone; the three that
// have no such check here (Maven, deb, rpm) pin only under layouts whose file
// names are never reused, and everything else is cached for the repository TTL
// so a wrong copy is replaced on revalidation:
//   - maven: released artifacts, not maven-metadata*.xml, archetype-catalog.xml
//     (both are regenerated in place) or -SNAPSHOT files; a checksum or
//     signature file (.md5 .sha1 .sha256 .sha512 .asc) is as immutable as the
//     file it belongs to.
//   - deb: .deb/.udeb below a pool/ directory (the Debian archive layout, where
//     a file name is never reused); the same names in a flat repository, where
//     a publisher may overwrite a package, use the repository TTL.
//   - rpm: .rpm below a Packages/ directory (the layout of Fedora, CentOS, RHEL
//     and most vendor repositories, whose NEVRA-named files are not rebuilt in
//     place); flat or other layouts use the repository TTL. No cheap integrity
//     signal (the repodata checksums are not checked by this proxy) exists to
//     pin the rest.
func Immutable(format, key string) bool {
	name := key[strings.LastIndexByte(key, '/')+1:]

	switch format {
	case "maven":
		return mavenImmutable(key)
	case "deb":
		return inDir(key, "pool") && (strings.HasSuffix(name, ".deb") || strings.HasSuffix(name, ".udeb"))
	case "rpm":
		return inDir(key, "Packages") && strings.HasSuffix(name, ".rpm")
	case "pypi":
		// Distribution files (and their PEP 658 metadata), not the index pages.
		return strings.HasPrefix(key, "files/")
	case "npm":
		// Tarballs (<pkg>/-/<file>), not packuments.
		return strings.HasSuffix(name, ".tgz") && strings.Contains(key, "/-/")
	case "helm":
		// Chart archives (charts/<file>), not index.yaml or provenance files.
		return strings.HasPrefix(key, helmChartsDir) && !strings.HasSuffix(name, helmProvExt)
	case "cargo":
		// Crate files, not index files or config.json.
		return strings.HasPrefix(key, "api/v1/crates/") && name == "download"
	case "go":
		// .info/.mod/.zip of a canonical version, not list/@latest/queries.
		return goImmutable(key)
	case "nuget":
		// Flat container package files, not version lists or registrations.
		return strings.HasPrefix(key, "v3/package/") && (strings.HasSuffix(name, ".nupkg") || strings.HasSuffix(name, ".nuspec"))
	case "docker":
		// Blobs and manifests addressed by digest; tags move.
		return digestRE.MatchString(name)
	}

	return false
}

// mavenSidecarExts are the checksum and signature files published next to a
// Maven file; each is immutable exactly when the file it belongs to is.
var mavenSidecarExts = []string{".md5", ".sha1", ".sha256", ".sha512", ".asc"} //nolint:gochecknoglobals // read-only table

// mavenImmutable reports whether key is a released, never-rewritten Maven file.
func mavenImmutable(key string) bool {
	for _, ext := range mavenSidecarExts {
		if owner, ok := strings.CutSuffix(key, ext); ok {
			return mavenImmutable(owner)
		}
	}

	name := key[strings.LastIndexByte(key, '/')+1:]

	return name != "" &&
		!strings.HasPrefix(name, "maven-metadata") &&
		name != "archetype-catalog.xml" &&
		!strings.Contains(key, "-SNAPSHOT")
}

// inDir reports whether key lies below a directory named dir, at the root of
// the upstream or deeper (an upstream URL may already include a path prefix).
func inDir(key, dir string) bool {
	return strings.HasPrefix(key, dir+"/") || strings.Contains(key, "/"+dir+"/")
}

// packageExts lists, for the formats whose files are not checked against a
// digest, the file extensions that name a binary package.
var packageExts = map[string][]string{ //nolint:gochecknoglobals // read-only table
	"maven": {".jar", ".pom", ".zip", ".war", ".ear", ".aar", ".module", ".gz"},
	"deb":   {".deb", ".udeb"},
	"rpm":   {".rpm"},
}

// isHTMLForPackage reports whether the upstream answered a package file's path
// with an HTML page: a captive portal, WAF challenge or proxy error page
// returned as 200. For the formats in packageExts nothing else would catch it,
// and caching it as the release would pin the page forever (and serve it to
// every client), so the body is refused instead.
func isHTMLForPackage(format, key string, resp *http.Response) bool {
	ct := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if !strings.HasPrefix(ct, "text/html") && !strings.HasPrefix(ct, "application/xhtml+xml") {
		return false
	}

	return slices.ContainsFunc(packageExts[format], func(ext string) bool { return strings.HasSuffix(key, ext) })
}

// CleanPath validates a client-supplied repository-relative path and returns
// it without the leading slash. Empty paths, directory paths (trailing slash),
// empty/dot/dot-dot segments, backslashes and control characters are refused:
// the path is appended to the upstream URL and must not be able to escape it.
func CleanPath(p string) (string, bool) {
	p = strings.TrimPrefix(p, "/")
	if p == "" || len(p) > maxPathLen || strings.HasSuffix(p, "/") {
		return "", false
	}

	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return "", false
		}
	}

	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}

	return p, true
}

// upstreamURL appends the cleaned path to the repository's upstream base URL.
func upstreamURL(baseURL, key string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse upstream url: %w", err)
	}

	u.Path = strings.TrimRight(u.Path, "/") + "/" + key
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""

	return u.String(), nil
}

// flightGroup collapses concurrent fetches of the same key into one.
type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flight
}

type flight struct {
	done    chan struct{}
	res     *resolution
	err     error
	waiters int
}

// do runs fn once per key at a time; concurrent callers wait for its result.
// A waiter whose ctx ends stops waiting without disturbing the flight.
func (g *flightGroup) do(ctx context.Context, key string, fn func() (*resolution, error)) (*resolution, error) {
	g.mu.Lock()

	if g.calls == nil {
		g.calls = make(map[string]*flight)
	}

	if f, ok := g.calls[key]; ok {
		f.waiters++
		g.mu.Unlock()

		select {
		case <-f.done:
			return f.res, f.err
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for upstream fetch: %w", ctx.Err())
		}
	}

	f := &flight{done: make(chan struct{})}
	g.calls[key] = f
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		close(f.done)
	}()

	f.res, f.err = fn()

	return f.res, f.err
}

// waiting reports how many callers are blocked on the in-flight fetch of key.
func (g *flightGroup) waiting(key string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	if f, ok := g.calls[key]; ok {
		return f.waiters
	}

	return 0
}
