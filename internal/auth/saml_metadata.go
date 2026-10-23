// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/telemetry"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// DefaultIDPMetadataRefreshSeconds is how long fetched IdP metadata is
	// reused when saml_config.idp_metadata_refresh_seconds is unset.
	DefaultIDPMetadataRefreshSeconds = 3600
	// MinIDPMetadataRefreshSeconds and MaxIDPMetadataRefreshSeconds bound the
	// per-provider refresh interval.
	MinIDPMetadataRefreshSeconds = 60
	MaxIDPMetadataRefreshSeconds = 86400

	maxIDPMetadataBytes = 1 << 20
	// maxStaleIDPMetadata bounds how long a copy that could not be refreshed is
	// still served, so a revoked IdP certificate is eventually dropped when the
	// URL stays unreachable.
	maxStaleIDPMetadata = 24 * time.Hour
	// idpMetadataFailureBackoff is how long a failed refresh suppresses further
	// attempts: logins keep using the stale copy (or fail fast) instead of each
	// one waiting out the fetch timeout against an IdP that is down.
	idpMetadataFailureBackoff = 30 * time.Second
	idpMetadataFetchTimeout   = 10 * time.Second
)

var (
	errIDPMetadataTooLarge = errors.New("idp metadata exceeds size limit")
	errIDPMetadataStatus   = errors.New("idp metadata url returned an error status")
	errIDPMetadataExpired  = errors.New("idp metadata is past its validUntil")
	errNoIDPMetadata       = errors.New("no idp metadata source configured")
)

// IDPMetadataRefresh returns the effective refresh interval of cfg: the
// configured value clamped to [60s, 24h], one hour when unset.
func IDPMetadataRefresh(cfg models.SAMLConfig) time.Duration {
	s := cfg.IDPMetadataRefreshSeconds
	if s <= 0 {
		s = DefaultIDPMetadataRefreshSeconds
	}

	s = min(max(s, MinIDPMetadataRefreshSeconds), MaxIDPMetadataRefreshSeconds)

	return time.Duration(s) * time.Second
}

// idpMetadataEntry is the per-URL state: the last good copy and the last
// failed refresh. All fields are guarded by mu; the fetch itself runs outside
// the lock, with refreshing/done making concurrent callers share it.
type idpMetadataEntry struct {
	mu         sync.Mutex
	descriptor *saml.EntityDescriptor
	fetchedAt  time.Time
	// validUntil and cacheDuration are the metadata's own validity hints.
	validUntil    time.Time
	cacheDuration time.Duration

	lastFailure time.Time
	lastErr     error

	refreshing bool
	done       chan struct{}
}

// fresh reports whether the copy may be used without a refresh at now. The
// metadata's cacheDuration shortens the configured refresh interval but never
// below the 60s floor; its validUntil is a hard upper bound.
func (e *idpMetadataEntry) fresh(now time.Time, refresh time.Duration) bool {
	if e.descriptor == nil {
		return false
	}

	ttl := refresh
	if e.cacheDuration > 0 && e.cacheDuration < ttl {
		ttl = max(e.cacheDuration, MinIDPMetadataRefreshSeconds*time.Second)
	}

	if !now.Before(e.fetchedAt.Add(ttl)) {
		return false
	}

	return e.validUntil.IsZero() || now.Before(e.validUntil)
}

// usableStale reports whether the copy may still be served when a refresh
// failed (stale-if-error): within the staleness bound and never past the
// metadata's own validUntil.
func (e *idpMetadataEntry) usableStale(now time.Time) bool {
	if e.descriptor == nil || !now.Before(e.fetchedAt.Add(maxStaleIDPMetadata)) {
		return false
	}

	return e.validUntil.IsZero() || now.Before(e.validUntil)
}

// idpMetadataFetchFunc downloads the raw metadata document.
type idpMetadataFetchFunc func(ctx context.Context, metadataURL string) ([]byte, error)

// idpMetadataCache caches IdP metadata per URL (refreshing, stale-if-error,
// negative-cached failures) and static XML per document. The clock and the
// fetcher are injectable.
type idpMetadataCache struct {
	now   func() time.Time
	fetch idpMetadataFetchFunc

	mu      sync.Mutex
	entries map[string]*idpMetadataEntry
	static  sync.Map // xml -> *saml.EntityDescriptor
}

func newIDPMetadataCache(now func() time.Time, fetch idpMetadataFetchFunc) *idpMetadataCache {
	return &idpMetadataCache{now: now, fetch: fetch, entries: map[string]*idpMetadataEntry{}}
}

// samlMetadata is the process-lifetime IdP metadata cache.
//
//nolint:gochecknoglobals // process-lifetime SAML IdP metadata cache; tests swap it for a fake clock/fetcher
var samlMetadata = newIDPMetadataCache(time.Now, downloadIDPMetadata)

func (c *idpMetadataCache) entry(metadataURL string) *idpMetadataEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[metadataURL]
	if !ok {
		e = &idpMetadataEntry{}
		c.entries[metadataURL] = e
	}

	return e
}

// metadata returns the IdP descriptor for cfg: static XML is parsed once; a
// URL is refreshed per IDPMetadataRefresh(cfg).
func (c *idpMetadataCache) metadata(ctx context.Context, cfg models.SAMLConfig, log *logger.Logger) (*saml.EntityDescriptor, error) {
	if cfg.IDPMetadataURL != "" {
		return c.fromURL(ctx, cfg.IDPMetadataURL, IDPMetadataRefresh(cfg), log)
	}

	if cfg.IDPMetadataXML != "" {
		if cached, ok := c.static.Load(cfg.IDPMetadataXML); ok {
			if md, ok := cached.(*saml.EntityDescriptor); ok {
				return md, nil
			}
		}

		md, err := samlsp.ParseMetadata([]byte(cfg.IDPMetadataXML))
		if err != nil {
			return nil, fmt.Errorf("parse idp metadata xml: %w", err)
		}

		// An admin-supplied document is trusted as is, validUntil included: it
		// never refreshes, so honoring an expiry would lock everybody out.
		c.static.Store(cfg.IDPMetadataXML, md)

		return md, nil
	}

	return nil, errNoIDPMetadata
}

func (c *idpMetadataCache) fromURL(ctx context.Context, metadataURL string, refresh time.Duration, log *logger.Logger) (*saml.EntityDescriptor, error) {
	e := c.entry(metadataURL)

	for {
		e.mu.Lock()
		now := c.now()

		if e.fresh(now, refresh) {
			md := e.descriptor
			e.mu.Unlock()

			return md, nil
		}

		// A refresh failed moments ago: do not hit the IdP again yet.
		if !e.lastFailure.IsZero() && now.Sub(e.lastFailure) < idpMetadataFailureBackoff {
			md, err := e.fallbackLocked(now, e.lastErr)
			e.mu.Unlock()

			return md, err
		}

		// Someone else is refreshing: serve the stale copy at once, or wait for
		// the result when there is nothing to serve.
		if e.refreshing {
			if e.usableStale(now) {
				md := e.descriptor
				e.mu.Unlock()

				return md, nil
			}

			done := e.done
			e.mu.Unlock()

			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("%w: %w", ErrIDPMetadataUnavailable, ctx.Err())
			}
		}

		e.refreshing = true
		e.done = make(chan struct{})
		e.mu.Unlock()

		return c.refresh(ctx, e, metadataURL, log)
	}
}

// refresh downloads and installs new metadata; the caller owns e.refreshing.
func (c *idpMetadataCache) refresh(ctx context.Context, e *idpMetadataEntry, metadataURL string, log *logger.Logger) (*saml.EntityDescriptor, error) {
	// One caller's cancelled request must not fail the refresh everyone shares.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), idpMetadataFetchTimeout)
	defer cancel()

	md, fetchErr := c.download(fetchCtx, metadataURL)

	e.mu.Lock()
	defer e.mu.Unlock()

	defer func() {
		e.refreshing = false
		close(e.done)
	}()

	now := c.now()

	if fetchErr != nil {
		e.lastFailure, e.lastErr = now, fetchErr

		if log != nil {
			log.Warnw("SAML IdP metadata refresh failed", "url", metadataURL, "error", fetchErr, "serving_stale", e.usableStale(now))
		}

		return e.fallbackLocked(now, fetchErr)
	}

	e.descriptor = md
	e.fetchedAt = now
	e.validUntil = md.ValidUntil
	e.cacheDuration = md.CacheDuration
	e.lastFailure, e.lastErr = time.Time{}, nil

	return md, nil
}

// download fetches and validates one copy. Metadata that is already past its
// validUntil is rejected: a live fetch never installs a stale document.
func (c *idpMetadataCache) download(ctx context.Context, metadataURL string) (*saml.EntityDescriptor, error) {
	body, err := c.fetch(ctx, metadataURL)
	if err != nil {
		return nil, err
	}

	md, err := samlsp.ParseMetadata(body)
	if err != nil {
		return nil, fmt.Errorf("parse idp metadata: %w", err)
	}

	if !md.ValidUntil.IsZero() && !c.now().Before(md.ValidUntil) {
		return nil, fmt.Errorf("%w (%s)", errIDPMetadataExpired, md.ValidUntil.UTC().Format(time.RFC3339))
	}

	return md, nil
}

// fallbackLocked serves the stale copy when allowed, else the failure. e.mu
// must be held.
func (e *idpMetadataEntry) fallbackLocked(now time.Time, cause error) (*saml.EntityDescriptor, error) {
	if e.usableStale(now) {
		return e.descriptor, nil
	}

	return nil, fmt.Errorf("%w: %w", ErrIDPMetadataUnavailable, cause)
}

// downloadIDPMetadata is the production fetcher. The URL is admin-supplied:
// private/loopback targets are rejected (unless
// OPENARTIFACT_ALLOW_PRIVATE_UPSTREAMS=true) and re-checked at dial time.
func downloadIDPMetadata(ctx context.Context, metadataURL string) ([]byte, error) {
	if err := netutil.ValidateOutboundURLContext(ctx, metadataURL); err != nil {
		return nil, fmt.Errorf("idp metadata url rejected: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build metadata request: %w", err)
	}

	resp, err := telemetry.WrapClient(netutil.SafeHTTPClient(idpMetadataFetchTimeout)).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%w: %d", errIDPMetadataStatus, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIDPMetadataBytes+1))
	if err != nil {
		return nil, err
	}

	if len(body) > maxIDPMetadataBytes {
		return nil, errIDPMetadataTooLarge
	}

	return body, nil
}
