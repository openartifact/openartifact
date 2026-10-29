// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// authCacheMaxEntries bounds each cached map. Hitting the bound drops the map
// instead of evicting selectively: the working set (principals and
// repositories in active use) is far smaller, so this only guards memory
// against an unbounded stream of distinct credentials.
const authCacheMaxEntries = 4096

// authCache serves the per-request authentication lookups from memory for a
// short TTL. Every request otherwise pays five sequential round trips (token,
// user, groups, permission targets, repository) before any work starts.
//
// Consistency model:
//   - Writes made through this PostgresDB call invalidate, which drops every
//     entry and bumps a generation so a read that was in flight across the
//     write cannot repopulate the cache with the old value.
//   - Writes made by another process (a second replica, cmd/resetpw) are seen
//     after at most the TTL.
//   - Only positive results are cached, so creating a user, token or
//     repository is visible at once.
//
// The zero value (ttl 0) is a disabled cache.
type authCache struct {
	ttl time.Duration
	now func() time.Time // test hook; nil means time.Now

	mu       sync.Mutex
	gen      uint64
	tokens   map[string]cacheEntry[*models.Token]
	users    map[string]cacheEntry[*models.User]
	groups   map[string]cacheEntry[[]*models.Group]
	perms    map[string]cacheEntry[[]*models.PermissionTarget]
	repos    map[string]cacheEntry[*models.Repository]
	projects map[string]cacheEntry[[]*models.ProjectGrant]
}

type cacheEntry[V any] struct {
	val     V
	expires time.Time
}

func (c *authCache) enabled() bool { return c.ttl > 0 }

func (c *authCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}

	return time.Now()
}

// invalidate drops every cached entry. Safe on a disabled cache.
func (c *authCache) invalidate() {
	if !c.enabled() {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.gen++
	c.tokens, c.users, c.groups, c.perms, c.repos, c.projects = nil, nil, nil, nil, nil, nil
}

// AuthCacheTTL is the configured lifetime of auth-derived cache entries (0:
// caching disabled). Other caches that derive from the same permission data,
// such as the search handler's readable-repository sets, follow it.
func (p *core) AuthCacheTTL() time.Duration { return p.authCache.ttl }

// AuthCacheGeneration changes whenever a write made through this PostgresDB
// invalidated the auth cache. A value derived from permission data is current
// only while the generation read before deriving it is unchanged. It never
// changes while the cache is disabled (TTL 0).
func (p *core) AuthCacheGeneration() uint64 {
	p.authCache.mu.Lock()
	defer p.authCache.mu.Unlock()

	return p.authCache.gen
}

// cachedLookup returns the cached value for key in *m, or runs load and caches
// the result unless isNil reports a not-found (nil) value. A result is stored only if no invalidation happened while
// load ran.
func cachedLookup[V any](
	ctx context.Context,
	c *authCache,
	m *map[string]cacheEntry[V],
	key string,
	clone func(V) V,
	isNil func(V) bool,
	load func(context.Context) (V, error),
) (V, error) {
	if !c.enabled() {
		return load(ctx)
	}

	c.mu.Lock()
	gen := c.gen

	if e, ok := (*m)[key]; ok && c.clock().Before(e.expires) {
		// Cached values are never mutated (callers get clones), so the
		// copy can happen outside the lock.
		val := e.val
		c.mu.Unlock()

		return clone(val), nil
	}
	c.mu.Unlock()

	v, err := load(ctx)
	if err != nil || isNil(v) {
		return v, err
	}

	stored := clone(v)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.gen == gen {
		if *m == nil || len(*m) >= authCacheMaxEntries {
			*m = make(map[string]cacheEntry[V])
		}

		(*m)[key] = cacheEntry[V]{val: stored, expires: c.clock().Add(c.ttl)}
	}

	return v, nil
}

// cloneShallow copies a value that holds no slices or maps.
func cloneShallow[T any](v *T) *T {
	cp := *v

	return &cp
}

func cloneShallowSlice[T any](vs []*T) []*T {
	out := make([]*T, len(vs))

	for i, v := range vs {
		out[i] = cloneShallow(v)
	}

	return out
}

func clonePermTargets(ts []*models.PermissionTarget) []*models.PermissionTarget {
	out := make([]*models.PermissionTarget, len(ts))

	for i, t := range ts {
		cp := *t
		cp.Actions = append([]string(nil), t.Actions...)
		out[i] = &cp
	}

	return out
}

// cachedPtr is cachedLookup for a lookup of one pointer value, where nil means
// not found.
func cachedPtr[T any](
	ctx context.Context,
	c *authCache,
	m *map[string]cacheEntry[*T],
	key string,
	clone func(*T) *T,
	load func(context.Context, string) (*T, error),
) (*T, error) {
	return cachedLookup(ctx, c, m, key, clone,
		func(v *T) bool { return v == nil },
		func(ctx context.Context) (*T, error) { return load(ctx, key) })
}

// cachedList is cachedLookup for a lookup of a list; an empty list is cached.
func cachedList[T any](
	ctx context.Context,
	c *authCache,
	m *map[string]cacheEntry[[]*T],
	key string,
	clone func([]*T) []*T,
	load func(context.Context, string) ([]*T, error),
) ([]*T, error) {
	return cachedLookup(ctx, c, m, key, clone,
		func([]*T) bool { return false },
		func(ctx context.Context) ([]*T, error) { return load(ctx, key) })
}

// GetPermissionTargetsForPrincipal returns the permission targets held by a
// user and its groups, from the auth cache when fresh.
func (p *PostgresDB) GetPermissionTargetsForPrincipal(
	ctx context.Context,
	userID string,
	groupIDs []string,
) ([]*models.PermissionTarget, error) {
	key := userID + "\x00" + strings.Join(groupIDs, "\x00")

	return cachedList(ctx, &p.authCache, &p.authCache.perms, key, clonePermTargets,
		func(ctx context.Context, _ string) ([]*models.PermissionTarget, error) {
			return p.GetPermissionTargetsForPrincipalUncached(ctx, userID, groupIDs)
		})
}

// ListProjectGrantsByUser returns the project grants of a user (see
// models.ProjectGrant), from the auth cache when fresh.
func (p *PostgresDB) ListProjectGrantsByUser(ctx context.Context, userID string) ([]*models.ProjectGrant, error) {
	return cachedList(ctx, &p.authCache, &p.authCache.projects, userID, cloneShallowSlice, p.listProjectGrantsByUserUncached)
}

func (p *PostgresDB) listProjectGrantsByUserUncached(ctx context.Context, userID string) ([]*models.ProjectGrant, error) {
	rows, err := p.q.ListProjectGrantsByUser(ctx, &userID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project grants: %w", err)
	}

	grants := make([]*models.ProjectGrant, 0, len(rows))
	for _, row := range rows {
		grants = append(grants, &models.ProjectGrant{ProjectID: row.ProjectID, Role: row.Role})
	}

	return grants, nil
}
