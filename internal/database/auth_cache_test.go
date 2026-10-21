// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func tokenLookup(c *authCache, calls *int, hash string, load func() (*models.Token, error)) (*models.Token, error) {
	return cachedLookup(context.Background(), c, &c.tokens, hash, cloneToken,
		func(t *models.Token) bool { return t == nil },
		func(context.Context) (*models.Token, error) {
			*calls++

			return load()
		})
}

func TestAuthCache_HitsUntilInvalidated(t *testing.T) {
	c := &authCache{ttl: time.Minute}
	calls := 0
	load := func() (*models.Token, error) { return &models.Token{ID: "t1", Scopes: []string{"read"}}, nil }

	for range 3 {
		if tok, err := tokenLookup(c, &calls, "h", load); err != nil || tok.ID != "t1" {
			t.Fatalf("lookup = %+v, %v", tok, err)
		}
	}

	if calls != 1 {
		t.Fatalf("loader ran %d times for 3 lookups, want 1", calls)
	}

	c.invalidate()

	if _, err := tokenLookup(c, &calls, "h", load); err != nil {
		t.Fatal(err)
	}

	if calls != 2 {
		t.Fatalf("loader ran %d times after invalidate, want 2", calls)
	}
}

func TestAuthCache_ExpiresAfterTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	c := &authCache{ttl: 2 * time.Second, now: func() time.Time { return now }}
	calls := 0
	load := func() (*models.Token, error) { return &models.Token{ID: "t1"}, nil }

	for _, step := range []struct {
		advance time.Duration
		calls   int
	}{{0, 1}, {time.Second, 1}, {2 * time.Second, 2}} {
		now = now.Add(step.advance)

		if _, err := tokenLookup(c, &calls, "h", load); err != nil {
			t.Fatal(err)
		}

		if calls != step.calls {
			t.Fatalf("after +%v loader ran %d times, want %d", step.advance, calls, step.calls)
		}
	}
}

func TestAuthCache_DisabledNeverCaches(t *testing.T) {
	c := &authCache{}
	calls := 0
	load := func() (*models.Token, error) { return &models.Token{ID: "t1"}, nil }

	for range 3 {
		if _, err := tokenLookup(c, &calls, "h", load); err != nil {
			t.Fatal(err)
		}
	}

	if calls != 3 {
		t.Fatalf("loader ran %d times on a disabled cache, want 3", calls)
	}

	c.invalidate() // must not panic on a disabled cache
}

func TestAuthCache_DoesNotCacheNotFoundOrErrors(t *testing.T) {
	c := &authCache{ttl: time.Minute}
	calls := 0

	for range 2 {
		if tok, err := tokenLookup(c, &calls, "missing", func() (*models.Token, error) { return nil, nil }); err != nil || tok != nil {
			t.Fatalf("lookup = %+v, %v", tok, err)
		}
	}

	boom := errors.New("boom")

	for range 2 {
		if _, err := tokenLookup(c, &calls, "err", func() (*models.Token, error) { return nil, boom }); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	}

	if calls != 4 {
		t.Fatalf("loader ran %d times, want 4 (nothing cached)", calls)
	}
}

// A read that began before a write and finishes after the invalidation must
// not put its pre-write value back into the cache.
func TestAuthCache_InFlightReadDoesNotRepopulateAfterInvalidate(t *testing.T) {
	c := &authCache{ttl: time.Minute}
	calls := 0

	stale, err := tokenLookup(c, &calls, "h", func() (*models.Token, error) {
		c.invalidate() // a write lands while the read is in flight

		return &models.Token{ID: "old"}, nil
	})
	if err != nil || stale.ID != "old" {
		t.Fatalf("lookup = %+v, %v", stale, err)
	}

	fresh, err := tokenLookup(c, &calls, "h", func() (*models.Token, error) { return &models.Token{ID: "new"}, nil })
	if err != nil || fresh.ID != "new" {
		t.Fatalf("lookup after in-flight invalidate = %+v, %v; stale value was cached", fresh, err)
	}
}

// Callers receive copies, so mutating a result cannot corrupt later lookups.
func TestAuthCache_ReturnsIsolatedCopies(t *testing.T) {
	c := &authCache{ttl: time.Minute}
	calls := 0
	load := func() (*models.Token, error) { return &models.Token{ID: "t1", Scopes: []string{"read"}}, nil }

	first, _ := tokenLookup(c, &calls, "h", load)
	first.Scopes[0] = "admin"
	first.ID = "mutated"

	second, _ := tokenLookup(c, &calls, "h", load)
	if second.ID != "t1" || second.Scopes[0] != "read" {
		t.Fatalf("cache entry was mutated through a returned value: %+v", second)
	}
}

func TestAuthCache_BoundedSize(t *testing.T) {
	c := &authCache{ttl: time.Minute}
	calls := 0

	for i := range authCacheMaxEntries + 10 {
		key := string(rune('a'+i%26)) + time.Duration(i).String()
		if _, err := tokenLookup(c, &calls, key, func() (*models.Token, error) { return &models.Token{ID: key}, nil }); err != nil {
			t.Fatal(err)
		}
	}

	c.mu.Lock()
	n := len(c.tokens)
	c.mu.Unlock()

	if n > authCacheMaxEntries {
		t.Fatalf("cache holds %d entries, bound is %d", n, authCacheMaxEntries)
	}
}
