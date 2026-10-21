// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package proxy serves proxy repositories: files are fetched from the
// repository's upstream on first request, cached content-addressed in the blob
// store, recorded in proxy_cache_entries (which also backs the repository's
// package listing) and served from the cache until their TTL expires.
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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/format/base"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/storage"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// DefaultMaxBody caps any single file fetched from an upstream.
	DefaultMaxBody int64 = 4 << 30 // 4 GiB

	// fetchTimeout bounds one upstream fetch, independent of any client.
	fetchTimeout = 15 * time.Minute

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
)

// Service fetches and caches upstream files for proxy repositories.
type Service struct {
	log       *logger.Logger
	db        database.Database
	blobStore storage.BlobStore
	client    *http.Client
	flights   flightGroup

	// maxBody caps a single upstream response; DefaultMaxBody unless a test
	// lowers it.
	maxBody int64
}

// NewService creates the proxy service. Upstream requests use an SSRF-safe
// client: blocked destination addresses are refused at dial time and on every
// redirect hop.
func NewService(log *logger.Logger, db database.Database, blobStore storage.BlobStore) *Service {
	return &Service{
		log:       log,
		db:        db,
		blobStore: blobStore,
		client:    netutil.SafeHTTPClient(fetchTimeout),
		maxBody:   DefaultMaxBody,
	}
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

	key, ok := CleanPath(relPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	entry, fromCache, err := s.resolve(r.Context(), repo, key)
	if err != nil {
		s.writeResolveError(w, r, err)
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

		entry, _, err = s.resolve(r.Context(), repo, key)
		if err != nil {
			s.writeResolveError(w, r, err)
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

	if quarantine.Enforce(w, r, s.db, repo.ID, quarantine.PathKeys(key, entry.ID)...) {
		return
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

// resolve returns a fresh cache entry for key, fetching it from upstream when
// it is absent or expired. fromCache reports whether no fetch was needed.
// errNotFound is returned for an upstream 404 (negatively cached).
func (s *Service) resolve(ctx context.Context, repo *models.Repository, key string) (*models.ProxyCacheEntry, bool, error) {
	entry, err := s.freshEntry(ctx, repo, key)
	if err != nil {
		return nil, false, err
	}

	if entry != nil {
		return entry, true, nil
	}

	entry, err = s.flights.do(ctx, repo.ID+"\x00"+key, func() (*models.ProxyCacheEntry, error) {
		// The flight outlives the request that started it: other requests
		// are waiting on it.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
		defer cancel()

		// A flight that finished just before this one started has already
		// filled the cache.
		if e, ferr := s.freshEntry(fctx, repo, key); ferr != nil || e != nil {
			return e, ferr
		}

		return s.fetch(fctx, repo, key)
	})
	if err != nil {
		return nil, false, err
	}

	if entry == nil {
		// The flight leader died (panic) before producing a result.
		return nil, false, errInternal
	}

	return entry, false, nil
}

// freshEntry returns the cached entry if it has not expired. A negative entry
// yields errNotFound.
func (s *Service) freshEntry(ctx context.Context, repo *models.Repository, key string) (*models.ProxyCacheEntry, error) {
	entry, err := s.db.GetProxyCacheEntry(ctx, repo.ID, key)
	if err != nil {
		s.log.Errorw("failed to read proxy cache entry", "repo", repo.Name, "path", key, "error", err)
		return nil, errInternal
	}

	if entry == nil || !entry.ExpiresAt.After(time.Now()) {
		return nil, nil //nolint:nilnil // (nil, nil) means "no usable entry": the caller fetches
	}

	if entry.StatusCode == http.StatusNotFound || entry.BlobKey == "" {
		return nil, errNotFound
	}

	return entry, nil
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

	if verr := netutil.ValidateOutboundURLContext(ctx, target); verr != nil {
		s.log.Warnw("proxy upstream rejected by outbound URL policy", "repo", repo.Name, "error", verr)
		return nil, errUpstream
	}

	req, err := s.newUpstreamRequest(ctx, repo, target)
	if err != nil {
		return nil, err
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warnw("proxy upstream request failed", "repo", repo.Name, "path", key, "error", err)
		return nil, errUpstream
	}
	defer func() { base.CloseLog(resp.Body, s.log) }()

	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		s.recordNegative(ctx, repo, key)
		return nil, errNotFound
	case resp.StatusCode != http.StatusOK:
		s.log.Warnw("proxy upstream returned an error", "repo", repo.Name, "path", key, "status", resp.StatusCode)
		return nil, errUpstream
	}

	return s.store(ctx, repo, key, resp)
}

func (s *Service) newUpstreamRequest(ctx context.Context, repo *models.Repository, target string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		s.log.Errorw("failed to build proxy upstream request", "repo", repo.Name, "error", err)
		return nil, errInternal
	}

	req.Header.Set("User-Agent", "OpenArtifact-Proxy")
	req.Header.Set("Accept", "*/*")
	// Mirror bytes exactly: never let the transport transparently decompress.
	req.Header.Set("Accept-Encoding", "identity")

	if repo.UpstreamUsername != "" {
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
func (s *Service) store(ctx context.Context, repo *models.Repository, key string, resp *http.Response) (*models.ProxyCacheEntry, error) {
	if resp.ContentLength > s.maxBody {
		s.log.Warnw("proxy upstream file exceeds size limit", "repo", repo.Name, "path", key, "size", resp.ContentLength)
		return nil, errUpstream
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
		return nil, errUpstream
	}

	if size > s.maxBody || (resp.ContentLength >= 0 && size != resp.ContentLength) {
		s.log.Warnw("proxy upstream body oversized or truncated", "repo", repo.Name, "path", key, "size", size)
		return nil, errUpstream
	}

	sum := hex.EncodeToString(hasher.Sum(nil))

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
		ExpiresAt:    now.Add(ttlFor(repo, key)),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	mediaType := contentType
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}

	err = storage.WithBlobLock(ctx, s.db, sum, func(ctx context.Context) error {
		blobID, perr := s.blobStore.Put(ctx, spool, map[string]string{"sha256": sum, "content-type": mediaType})
		if perr != nil {
			return fmt.Errorf("store blob: %w", perr)
		}

		if blobID != sum {
			return fmt.Errorf("%w: blob store returned id %q for sha256 %s", errInternal, blobID, sum)
		}

		if _, eerr := s.db.EnsureBlob(ctx, sum, mediaType, size); eerr != nil {
			return fmt.Errorf("record blob: %w", eerr)
		}

		if uerr := s.db.UpsertProxyCacheEntry(ctx, entry); uerr != nil {
			return fmt.Errorf("record cache entry: %w", uerr)
		}

		return nil
	})
	if err != nil {
		s.log.Errorw("failed to cache proxied file", "repo", repo.Name, "path", key, "sha256", sum, "error", err)
		return nil, errInternal
	}

	return entry, nil
}

func (s *Service) recordNegative(ctx context.Context, repo *models.Repository, key string) {
	ttl := time.Duration(repo.NegativeCacheTTLSeconds) * time.Second
	if ttl <= 0 {
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

func ttlFor(repo *models.Repository, key string) time.Duration {
	if Immutable(repo.Format, key) {
		return immutableTTL
	}

	return time.Duration(repo.CacheTTLSeconds) * time.Second
}

// Immutable reports whether the file at key never changes once published, so a
// cached copy never needs revalidation: released Maven artifacts (not
// maven-metadata.xml, not -SNAPSHOT files) and deb/rpm package files.
func Immutable(format, key string) bool {
	name := key[strings.LastIndexByte(key, '/')+1:]

	switch format {
	case "maven":
		return !strings.HasPrefix(name, "maven-metadata.xml") && !strings.Contains(key, "-SNAPSHOT")
	case "deb":
		return strings.HasSuffix(name, ".deb") || strings.HasSuffix(name, ".udeb")
	case "rpm":
		return strings.HasSuffix(name, ".rpm")
	}

	return false
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
	entry   *models.ProxyCacheEntry
	err     error
	waiters int
}

// do runs fn once per key at a time; concurrent callers wait for its result.
// A waiter whose ctx ends stops waiting without disturbing the flight.
func (g *flightGroup) do(ctx context.Context, key string, fn func() (*models.ProxyCacheEntry, error)) (*models.ProxyCacheEntry, error) {
	g.mu.Lock()

	if g.calls == nil {
		g.calls = make(map[string]*flight)
	}

	if f, ok := g.calls[key]; ok {
		f.waiters++
		g.mu.Unlock()

		select {
		case <-f.done:
			return f.entry, f.err
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

	f.entry, f.err = fn()

	return f.entry, f.err
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
