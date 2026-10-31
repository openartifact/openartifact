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
	"bytes"
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
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/format/terraform"
	"github.com/openartifact/openartifact/internal/httpcache"
	"github.com/openartifact/openartifact/internal/iocopy"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/pathseg"
	"github.com/openartifact/openartifact/internal/presign"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// DefaultMaxBody caps any single file fetched from an upstream.
	DefaultMaxBody int64 = 4 << 30 // 4 GiB

	// DefaultMaxMetadata caps an index or metadata file fetched from an
	// upstream (Packages, repodata, packuments, simple pages, maven-metadata,
	// cargo and helm indexes, Go version lists). The largest real ones are
	// tens of MiB; a hostile upstream must not make the proxy spool and store
	// gigabytes under a metadata name.
	DefaultMaxMetadata int64 = 256 << 20

	// fetchTimeout bounds one upstream fetch, independent of any client.
	fetchTimeout = 15 * time.Minute

	// responseHeaderTimeout bounds the wait for an upstream's response
	// headers (the dial and TLS handshake are bounded to 10s each by the
	// SSRF-safe client). A hung upstream fails fast, as a network error, so
	// a stale copy can be served; the body transfer is only bound by
	// fetchTimeout.
	responseHeaderTimeout = 30 * time.Second

	// DefaultUpstreamStallTimeout is how long an upstream response body may
	// deliver no data before the fetch fails, so a dead connection releases
	// its flight (and the requests waiting on it) in a minute rather than
	// holding it until fetchTimeout.
	DefaultUpstreamStallTimeout = 60 * time.Second

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

	// jsonMediaType is the Content-Type of the JSON documents the proxy generates.
	jsonMediaType = "application/json"
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

	// errUnauthorized is an upstream that answered 401: the credentials (or
	// the token obtained with them) were refused. It is a plain errUpstream
	// for every caller; formats that log in to the upstream renew their token.
	errUnauthorized = fmt.Errorf("%w: unauthorized", errUpstream)
)

// staleWarning marks a response served from an expired cache entry
// (RFC 7234 section 5.5.1, "Response is Stale").
const staleWarning = `110 - "Response is Stale"`

// ServiceStore is the part of the database Service needs.
type ServiceStore interface {
	quarantine.Checker
	DeleteProxyCacheEntry(ctx context.Context, repoID string, cacheKey string) error
	EnsureBlob(ctx context.Context, sha256 string, mediaType string, size int64) (string, error)
	GetProxyCacheEntry(ctx context.Context, repoID string, cacheKey string) (*models.ProxyCacheEntry, error)
	UpsertProxyCacheEntry(ctx context.Context, entry *models.ProxyCacheEntry) error
}

// Service fetches and caches upstream files for proxy repositories.
type Service struct {
	log       *logger.Logger
	db        ServiceStore
	blobStore storage.BlobStore
	client    *http.Client
	flights   flightGroup
	tokens    tokenCache // docker upstream bearer tokens

	// baseURL is BASE_URL when the operator set one, "" otherwise; read it
	// through publicBase (the origin rewritten metadata points at).
	baseURL string

	// dockerDigestTTL is how long a docker manifest or blob addressed by digest
	// stays cached after it was last used; 0 keeps them until an admin purge or
	// the repository's removal (see WithDockerDigestTTL).
	dockerDigestTTL time.Duration

	// sumdb verifies Go module files against the checksum database; nil
	// (GOSUMDB=off) verifies their structure only.
	sumdb *sumDB

	// curation supplies the curation rules of proxy repositories (WithCuration).
	curation CurationSource

	nuget nugetIndexCache // parsed NuGet upstream service indexes

	gemMD5 md5Memo // MD5 ETags of cached RubyGems compact index files

	tfSecret    []byte            // server HMAC secret the Terraform download tickets derive from
	tfTickets   *terraform.Minter // mints the download URLs of Terraform proxy documents
	tfEndpoints tfEndpointMemo    // registry endpoints resolved from cached discovery documents
	tfCodeload  string            // base URL GitHub module sources are fetched from; "" = tfCodeloadBase (tests override it)

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

	// maxMeta caps an index or metadata response; DefaultMaxMetadata unless a
	// test lowers it.
	maxMeta int64

	// maxCondaIndex caps a conda repodata file, plain or decompressed;
	// DefaultMaxCondaIndex unless a test lowers it.
	maxCondaIndex int64
}

// NewService creates the proxy service. Upstream requests use an SSRF-safe
// client: blocked destination addresses are refused at dial time and on every
// redirect hop.
func NewService(log *logger.Logger, db ServiceStore, blobStore storage.BlobStore) *Service {
	client := upstreamClient(DefaultUpstreamStallTimeout)

	return &Service{
		log:       log,
		db:        db,
		blobStore: blobStore,
		client:    client,
		maxBody:   DefaultMaxBody,
		maxMeta:   DefaultMaxMetadata,

		maxCondaIndex: DefaultMaxCondaIndex,

		packuments: base.NewGate(maxConcurrentPackuments),
		fetches:    base.NewGate(maxConcurrentFetches),
		sumdb:      newSumDB(os.Getenv("GOSUMDB"), client, log),
	}
}

func upstreamClient(stall time.Duration) *http.Client {
	return telemetry.WrapClient(netutil.SafeHTTPClientWithStallTimeout(fetchTimeout, responseHeaderTimeout, stall))
}

// WithUpstreamStallTimeout sets how long an upstream response body may deliver
// no data before the fetch fails (DefaultUpstreamStallTimeout by default); 0
// disables the check. It must be called before the service serves requests.
func (s *Service) WithUpstreamStallTimeout(d time.Duration) *Service {
	s.client = upstreamClient(max(d, 0))
	if s.sumdb != nil {
		s.sumdb.client = s.client
	}

	return s
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

	h, hasHooks := hooks.Lookup(repo.Format)
	if hasHooks && s.curationRefusal(w, r, repo, h, relPath) {
		return
	}

	if hasHooks && h.Serve != nil {
		h.Serve(s, w, r, repo, relPath)
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
func (s *Service) serveResolved(
	w http.ResponseWriter,
	r *http.Request,
	repo *models.Repository,
	key string,
	res *resolution,
	refetch func() (*resolution, error),
) {
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

	if h, ok := hooks.Lookup(repo.Format); ok && h.WriteEntry != nil && h.WriteEntry(s, w, r, repo, entry, key, rc) {
		return
	}

	writeEntry(w, r, entry, rc)
}

func (s *Service) writeResolveError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNotFound):
		http.NotFound(w, r)
	case writeGateError(w, err):
	case errors.Is(err, errTooNew):
		http.Error(w, errTooNew.Error(), http.StatusForbidden)
	case errors.Is(err, errUpstream):
		http.Error(w, "upstream repository error", http.StatusBadGateway)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The client went away; nothing useful to write.
		s.log.Debugw("proxy request abandoned", "error", err)
	default:
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// failWithoutValidators answers an error after validators were set: they must
// not describe the error response.
func (s *Service) failWithoutValidators(w http.ResponseWriter, r *http.Request, err error) {
	h := w.Header()
	h.Del("ETag")
	h.Set("Cache-Control", "no-store")
	h.Del("Warning")

	s.writeResolveError(w, r, err)
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

	if httpcache.ETagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	h.Set("Content-Length", strconv.FormatInt(entry.SizeBytes, 10))
	w.WriteHeader(http.StatusOK)

	if r.Method == http.MethodHead {
		return
	}

	// The status line is already out: a failed copy can only abort the body.
	_, _ = iocopy.Copy(w, body)
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
func (s *Service) lookup(
	ctx context.Context,
	repo *models.Repository,
	key string,
) (*models.ProxyCacheEntry, *models.ProxyCacheEntry, error) {
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

	if h, ok := hooks.Lookup(repo.Format); ok && h.Fetch != nil {
		return h.Fetch(s, ctx, repo, key, target)
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
	// authorization, when set, is sent as the Authorization header instead of
	// the repository's Basic credentials (formats that log in to the upstream).
	authorization string
	// contentType, when set, is recorded and served instead of the upstream's
	// Content-Type, for documents whose clients insist on an exact type.
	contentType string
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
	// transform, when set, turns the upstream answer into the document that is
	// cached: it replaces resp.Body and resp.ContentLength (replaceBody). It
	// also accepts a 204, whose meaning lives in the headers. The original
	// body is still closed by the caller, which also closes the substituted
	// one after the entry is stored (a file-backed body removes its file then).
	transform func(resp *http.Response) error
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
	// The body a transform substitutes is memory; the upstream's is closed here.
	upstreamBody := resp.Body
	defer func() { base.CloseLog(upstreamBody, s.log) }()

	if o.transform == nil || resp.StatusCode != http.StatusNoContent {
		if err := s.upstreamStatusError(repo, key, resp.StatusCode); err != nil {
			if errors.Is(err, errNotFound) {
				s.recordNegative(ctx, repo, key)
			}

			return nil, err
		}
	}

	if o.transform != nil {
		terr := o.transform(resp)
		// A transform may hand over a body that holds a spool file; closing
		// it twice (when it left the upstream body in place) is harmless.
		defer func() { base.CloseLog(resp.Body, s.log) }()

		if terr != nil {
			return nil, terr
		}
	}

	return s.store(ctx, repo, key, resp, o)
}

// replaceBody makes body the response a transform hands to store.
func replaceBody(resp *http.Response, body []byte) {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.TransferEncoding = nil
}

// upstreamStatusError classifies an upstream answer that did not deliver the
// file: errNotFound for 404/410, errUpstreamDown for a 5xx, errUnauthorized for
// 401 and errUpstream for the rest; nil for 200.
func (s *Service) upstreamStatusError(repo *models.Repository, key string, status int) error {
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusNotFound || status == http.StatusGone:
		return errNotFound
	case status >= http.StatusInternalServerError:
		s.log.Warnw("proxy upstream returned an error", "repo", repo.Name, "path", key, "status", status)
		return errUpstreamDown
	case status == http.StatusUnauthorized:
		s.log.Warnw("proxy upstream refused the credentials", "repo", repo.Name, "path", key, "status", status)
		return errUnauthorized
	}

	s.log.Warnw("proxy upstream returned an error", "repo", repo.Name, "path", key, "status", status)

	return errUpstream
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

	if o.authorization != "" {
		req.Header.Set("Authorization", o.authorization)
	} else if repo.UpstreamUsername != "" && !o.noAuth {
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

// bodyLimit is the largest body the proxy accepts for key: the file limit,
// lowered to the index limit for the metadata files of format.
func (s *Service) bodyLimit(format, key string) int64 {
	h, _ := hooks.Lookup(format)

	switch {
	case h.IsLargeMetadata != nil && h.IsLargeMetadata(key):
		return min(s.maxBody, s.maxCondaIndex)
	case h.IsMetadata != nil && h.IsMetadata(key):
		return min(s.maxBody, s.maxMeta)
	}

	return s.maxBody
}

// store spools the response, enforcing the size limit, then writes the blob
// and cache entry under the blob lock.
func (s *Service) store(
	ctx context.Context,
	repo *models.Repository,
	key string,
	resp *http.Response,
	o fetchOpts,
) (*models.ProxyCacheEntry, error) {
	limit := s.bodyLimit(repo.Format, key)

	if resp.ContentLength > limit {
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

	size, err := iocopy.Copy(io.MultiWriter(spool, hasher), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		s.log.Warnw("proxy upstream body read failed", "repo", repo.Name, "path", key, "error", err)
		return nil, errUpstreamDown
	}

	if size > limit {
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

	contentType := cmp.Or(o.contentType, resp.Header.Get("Content-Type"))
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

// persist stores the spooled body (hashing to sum) in the blob store and
// records the blobs row and cache entry. The upload runs outside the per-blob
// lock; see storage.CommitBlobs.
func (s *Service) persist(ctx context.Context, spool io.ReadSeeker, sum, mediaType string, entry *models.ProxyCacheEntry) error {
	blob := storage.SeekerBlob(sum, spool, map[string]string{"sha256": sum, "content-type": mediaType})

	return storage.CommitBlob(ctx, s.db, s.blobStore, blob, func(ctx context.Context, key string) error {
		if key != sum {
			return fmt.Errorf("%w: blob store returned id %q for sha256 %s", errInternal, key, sum)
		}

		if _, eerr := s.db.EnsureBlob(ctx, sum, mediaType, entry.SizeBytes); eerr != nil {
			return fmt.Errorf("record blob: %w", eerr)
		}

		if uerr := s.db.UpsertProxyCacheEntry(ctx, entry); uerr != nil {
			return fmt.Errorf("record cache entry: %w", uerr)
		}

		return nil
	})
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
	h, _ := hooks.Lookup(repo.Format)

	if pinnable && h.Immutable != nil && h.Immutable(key) {
		return immutableTTL
	}

	ttl := time.Duration(repo.CacheTTLSeconds) * time.Second
	if h.CapTTL != nil {
		return h.CapTTL(key, ttl)
	}

	return ttl
}

// CleanPath validates a client-supplied repository-relative path and returns
// it without the leading slash. Empty paths, directory paths (trailing slash),
// empty/dot/dot-dot segments, backslashes and control characters are refused:
// the path is appended to the upstream URL and must not be able to escape it.
func CleanPath(p string) (string, bool) {
	p = strings.TrimPrefix(p, "/")
	if len(p) > maxPathLen || !pathseg.Path(p) {
		return "", false
	}

	for _, r := range p {
		if r < 0x20 || r == 0x7f {
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
