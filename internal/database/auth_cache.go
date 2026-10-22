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

// cachedLookup returns the cached value for key in *m, or runs load and caches
// the result unless isNil reports a not-found (nil) value. A result is stored only if no invalidation happened while
// load ran.
func cachedLookup[V any](ctx context.Context, c *authCache, m *map[string]cacheEntry[V], key string, clone func(V) V, isNil func(V) bool, load func(context.Context) (V, error)) (V, error) {
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

func cloneToken(t *models.Token) *models.Token {
	cp := *t
	cp.Scopes = append([]string(nil), t.Scopes...)

	return &cp
}

func cloneUser(u *models.User) *models.User {
	cp := *u

	return &cp
}

func cloneRepo(r *models.Repository) *models.Repository {
	cp := *r

	return &cp
}

func cloneGroups(gs []*models.Group) []*models.Group {
	out := make([]*models.Group, len(gs))

	for i, g := range gs {
		cp := *g
		out[i] = &cp
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

func cloneProjectGrants(gs []*models.ProjectGrant) []*models.ProjectGrant {
	out := make([]*models.ProjectGrant, len(gs))

	for i, g := range gs {
		cp := *g
		out[i] = &cp
	}

	return out
}

// GetTokenByHash returns the token with the given hash, from the auth cache
// when fresh.
func (p *PostgresDB) GetTokenByHash(ctx context.Context, hash string) (*models.Token, error) {
	return cachedLookup(ctx, &p.authCache, &p.authCache.tokens, hash, cloneToken,
		func(t *models.Token) bool { return t == nil },
		func(ctx context.Context) (*models.Token, error) { return p.GetTokenByHashUncached(ctx, hash) })
}

// GetUserByID returns the user with the given ID, from the auth cache when fresh.
func (p *PostgresDB) GetUserByID(ctx context.Context, id string) (*models.User, error) {
	return cachedLookup(ctx, &p.authCache, &p.authCache.users, id, cloneUser,
		func(u *models.User) bool { return u == nil },
		func(ctx context.Context) (*models.User, error) { return p.GetUserByIDUncached(ctx, id) })
}

// ListGroupsByUser returns the groups of a user, from the auth cache when fresh.
func (p *PostgresDB) ListGroupsByUser(ctx context.Context, userID string) ([]*models.Group, error) {
	return cachedLookup(ctx, &p.authCache, &p.authCache.groups, userID, cloneGroups,
		func([]*models.Group) bool { return false },
		func(ctx context.Context) ([]*models.Group, error) { return p.ListGroupsByUserUncached(ctx, userID) })
}

// GetPermissionTargetsForPrincipal returns the permission targets held by a
// user and its groups, from the auth cache when fresh.
func (p *PostgresDB) GetPermissionTargetsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]*models.PermissionTarget, error) {
	key := userID + "\x00" + strings.Join(groupIDs, "\x00")

	return cachedLookup(ctx, &p.authCache, &p.authCache.perms, key, clonePermTargets,
		func([]*models.PermissionTarget) bool { return false },
		func(ctx context.Context) ([]*models.PermissionTarget, error) {
			return p.GetPermissionTargetsForPrincipalUncached(ctx, userID, groupIDs)
		})
}

// GetRepositoryByName returns the repository with the given name, from the
// auth cache when fresh.
func (p *PostgresDB) GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error) {
	return cachedLookup(ctx, &p.authCache, &p.authCache.repos, name, cloneRepo,
		func(r *models.Repository) bool { return r == nil },
		func(ctx context.Context) (*models.Repository, error) { return p.GetRepositoryByNameUncached(ctx, name) })
}

// ListProjectGrantsByUser returns the project grants of a user (see
// models.ProjectGrant), from the auth cache when fresh.
func (p *PostgresDB) ListProjectGrantsByUser(ctx context.Context, userID string) ([]*models.ProjectGrant, error) {
	return cachedLookup(ctx, &p.authCache, &p.authCache.projects, userID, cloneProjectGrants,
		func([]*models.ProjectGrant) bool { return false },
		func(ctx context.Context) ([]*models.ProjectGrant, error) {
			return p.listProjectGrantsByUserUncached(ctx, userID)
		})
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
