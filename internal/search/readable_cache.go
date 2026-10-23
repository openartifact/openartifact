// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/auth"
)

// AuthCache is the invalidation signal of the auth cache (DB_AUTH_CACHE_TTL_MS)
// that the readable-repository cache follows; *database.PostgresDB implements it.
type AuthCache interface {
	// AuthCacheTTL is the entry lifetime; 0 disables caching.
	AuthCacheTTL() time.Duration
	// AuthCacheGeneration changes on every write that invalidates the auth cache.
	AuthCacheGeneration() uint64
}

// readableCacheMaxEntries bounds the cache like the auth cache: reaching it
// drops everything rather than evicting selectively.
const readableCacheMaxEntries = 4096

// readableCache memoizes the set of repositories a principal can read, which
// every search otherwise recomputes with a scan of all repositories plus a
// permission check per repository.
//
// Consistency matches the auth cache it derives from: an entry is served only
// for the TTL and only while the auth cache generation is the one read before
// the set was computed, so a write made through this process (permission,
// group, project or repository change) drops it at once, and a write by
// another replica is seen after at most the TTL. Entries are keyed by every
// field of the principal that decides readability, so principals never share
// one. The cached maps are shared and read-only.
type readableCache struct {
	src AuthCache
	now func() time.Time // test hook; nil means time.Now

	mu      sync.Mutex
	entries map[string]readableEntry
}

type readableEntry struct {
	repos   map[uuid.UUID]struct{}
	gen     uint64
	expires time.Time
}

func (c *readableCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}

	return time.Now()
}

// begin returns the generation to pass to store and whether caching is on.
func (c *readableCache) begin() (uint64, bool) {
	if c.src == nil || c.src.AuthCacheTTL() <= 0 {
		return 0, false
	}

	return c.src.AuthCacheGeneration(), true
}

func (c *readableCache) get(key string, gen uint64) (map[uuid.UUID]struct{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok || e.gen != gen || !c.clock().Before(e.expires) {
		return nil, false
	}

	return e.repos, true
}

// store keeps repos computed under generation gen. A stale gen is stored
// harmlessly: get only serves entries of the current generation.
func (c *readableCache) store(key string, gen uint64, repos map[uuid.UUID]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil || len(c.entries) >= readableCacheMaxEntries {
		c.entries = make(map[string]readableEntry)
	}

	c.entries[key] = readableEntry{repos: repos, gen: gen, expires: c.clock().Add(c.src.AuthCacheTTL())}
}

// principalCacheKey identifies everything about a principal that
// RepoReadFilter's outcome depends on. Fields are NUL-separated: none of them
// can contain NUL, so distinct principals cannot collide.
func principalCacheKey(p auth.Principal) string {
	var b strings.Builder

	field := func(s string) {
		b.WriteString(s)
		b.WriteByte(0)
	}

	userID := ""
	if p.UserID != nil {
		userID = *p.UserID
	}

	field(strconv.FormatBool(p.IsLegacy))
	field(strconv.FormatBool(p.IsAnonymous))
	field(userID)
	field(p.Username)
	field(p.TokenKind)
	field(p.ServiceProjectID)
	field(strconv.Itoa(len(p.TokenScopes)))

	for _, s := range p.TokenScopes {
		field(s)
	}

	field(strconv.Itoa(len(p.GroupIDs)))

	for _, g := range p.GroupIDs {
		field(g)
	}

	return b.String()
}
