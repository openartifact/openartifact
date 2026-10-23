// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package search

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/pkg/logger"
)

type fakeAuthCache struct {
	ttl time.Duration
	gen atomic.Uint64
}

func (f *fakeAuthCache) AuthCacheTTL() time.Duration { return f.ttl }
func (f *fakeAuthCache) AuthCacheGeneration() uint64 { return f.gen.Load() }

func principal(username string) *auth.Principal {
	id := "id-" + username

	return &auth.Principal{UserID: &id, Username: username, IsLegacy: true}
}

// cacheHarness serves searches for legacy principals (who read every
// repository) and counts how often the repository listing behind the readable
// set was queried.
type cacheHarness struct {
	h    *Handler
	mock sqlmock.Sqlmock
	src  *fakeAuthCache
	now  time.Time
}

func newCacheHarness(t *testing.T, ttl time.Duration) *cacheHarness {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	log, _ := logger.New("error", "json", "stderr")
	src := &fakeAuthCache{ttl: ttl}
	ch := &cacheHarness{mock: mock, src: src, now: time.Unix(1_000_000, 0)}
	ch.h = NewHandler(db, auth.NewEvaluator(nil, log, true), log, WithAuthCache(src))
	ch.h.searcher = &mockSearcher{}
	ch.h.readable.now = func() time.Time { return ch.now }

	return ch
}

// expectListing registers one repository listing returning the given repos.
func (c *cacheHarness) expectListing(ids ...uuid.UUID) {
	rows := sqlmock.NewRows([]string{"id", "name", "project_id"})
	for _, id := range ids {
		rows.AddRow(id.String(), "repo-"+id.String()[:4], nil)
	}

	c.mock.ExpectQuery(`SELECT id, name, project_id FROM repositories`).WillReturnRows(rows)
}

func (c *cacheHarness) search(t *testing.T, who *auth.Principal) {
	t.Helper()

	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, requestWithPrincipal(http.MethodGet, "/api/v1/search?q=x", who))

	if w.Code != http.StatusOK {
		t.Fatalf("search as %s: status %d body %s", who.Username, w.Code, w.Body.String())
	}
}

func (c *cacheHarness) met(t *testing.T) {
	t.Helper()

	if err := c.mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("repository listings: %v", err)
	}
}

func TestReadableReposAreCachedPerPrincipal(t *testing.T) {
	c := newCacheHarness(t, 2*time.Second)
	alice, bob := principal("alice"), principal("bob")

	// One listing per principal, however many searches each makes. A second
	// unexpected listing would fail the request with a 500.
	c.expectListing(uuid.New())
	c.expectListing(uuid.New())

	c.search(t, alice)
	c.search(t, alice)
	c.search(t, bob)
	c.search(t, bob)
	c.search(t, alice)
	c.met(t)
}

func TestReadableReposCacheNeverCrossesPrincipalKinds(t *testing.T) {
	repoA, repoB := uuid.New(), uuid.New()
	id := "same-user"
	session := &auth.Principal{UserID: &id, Username: "u", IsLegacy: true}
	scoped := &auth.Principal{UserID: &id, Username: "u", IsLegacy: true, TokenKind: "access", TokenScopes: []string{"t1"}}
	anon := &auth.Principal{Username: "u", IsAnonymous: true}

	c := newCacheHarness(t, time.Minute)

	// Same user, different credentials: the scoped token and the session each
	// resolve for themselves.
	c.expectListing(repoA)
	c.expectListing(repoB)

	c.search(t, session)
	c.search(t, scoped)
	c.search(t, session)
	c.search(t, scoped)
	c.met(t)

	// An anonymous principal is never confused with either (it would need the
	// evaluator's public-repository lookup, so only the keys are compared).
	if principalCacheKey(*session) == principalCacheKey(*scoped) ||
		principalCacheKey(*session) == principalCacheKey(*anon) ||
		principalCacheKey(*scoped) == principalCacheKey(*anon) {
		t.Fatal("principals with different credentials share a cache key")
	}
}

func TestReadableReposCacheDropsOnAuthInvalidation(t *testing.T) {
	c := newCacheHarness(t, time.Minute)
	alice := principal("alice")

	c.expectListing(uuid.New())
	c.expectListing(uuid.New())

	c.search(t, alice)
	c.search(t, alice)

	// A permission, group, project or repository write bumps the generation.
	c.src.gen.Add(1)

	c.search(t, alice)
	c.search(t, alice)
	c.met(t)
}

func TestReadableReposCacheExpiresAfterTTL(t *testing.T) {
	c := newCacheHarness(t, 2*time.Second)
	alice := principal("alice")

	c.expectListing(uuid.New())
	c.expectListing(uuid.New())

	c.search(t, alice)

	c.now = c.now.Add(time.Second)
	c.search(t, alice)

	c.now = c.now.Add(time.Second)
	c.search(t, alice)
	c.met(t)
}

func TestReadableReposCacheDisabledWithoutTTL(t *testing.T) {
	c := newCacheHarness(t, 0)
	alice := principal("alice")

	c.expectListing(uuid.New())
	c.expectListing(uuid.New())

	c.search(t, alice)
	c.search(t, alice)
	c.met(t)
}

func TestReadableReposLoadedAcrossInvalidationIsNotServed(t *testing.T) {
	c := newCacheHarness(t, time.Minute)

	gen, ok := c.h.readable.begin()
	if !ok {
		t.Fatal("cache should be on")
	}

	// The set was computed under gen; a write then invalidated the auth cache.
	c.h.readable.store("k", gen, map[uuid.UUID]struct{}{uuid.New(): {}})
	c.src.gen.Add(1)

	now, _ := c.h.readable.begin()
	if _, hit := c.h.readable.get("k", now); hit {
		t.Fatal("entry computed before the invalidation was served after it")
	}
}

func TestHandlerGroupParameter(t *testing.T) {
	for name, tc := range map[string]struct {
		query    string
		wantCode int
		want     bool
	}{
		"absent":  {"q=a", http.StatusOK, false},
		"package": {"q=a&group=package", http.StatusOK, true},
		"unknown": {"q=a&group=version", http.StatusBadRequest, false},
	} {
		t.Run(name, func(t *testing.T) {
			ms := &mockSearcher{}
			h, cleanup := newTestHandler(t, ms)
			defer cleanup()

			w := httptest.NewRecorder()
			h.ServeHTTP(w, requestWithPrincipal(http.MethodGet, "/api/v1/search?"+tc.query, adminPrincipal()))

			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.wantCode, w.Body.String())
			}

			if ms.gotOpts.GroupByPackage != tc.want {
				t.Errorf("GroupByPackage = %v, want %v", ms.gotOpts.GroupByPackage, tc.want)
			}
		})
	}
}
