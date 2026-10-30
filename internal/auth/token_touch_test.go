// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// size counts the entries of the table and checks the counter agrees.
func (t *touchThrottle) size(tb testing.TB) int {
	tb.Helper()

	n := 0

	t.entries.Range(func(_, _ any) bool {
		n++

		return true
	})

	if got := int(t.count.Load()); got != n {
		tb.Errorf("counter = %d, entries = %d", got, n)
	}

	return n
}

func TestTouchThrottle_PrunesExpiredEntries(t *testing.T) {
	th := &touchThrottle{max: 8}
	now := time.Now()

	for i := range 8 {
		th.record("old-"+strconv.Itoa(i), now.Add(-2*tokenTouchInterval))
	}

	if got := th.size(t); got != 8 {
		t.Fatalf("entries before the bound is passed = %d, want 8", got)
	}

	th.record("fresh", now)

	if got := th.size(t); got != 1 {
		t.Errorf("entries after the bound is passed = %d, want only the fresh one", got)
	}

	if _, ok := th.last("fresh"); !ok {
		t.Error("the fresh entry was pruned")
	}

	if _, ok := th.last("old-0"); ok {
		t.Error("an expired entry survived the prune")
	}
}

// A table of tokens that are all recent stays bounded too: arbitrary ones go.
func TestTouchThrottle_StaysBoundedWhenEveryEntryIsRecent(t *testing.T) {
	th := &touchThrottle{max: 8}
	now := time.Now()

	for i := range 200 {
		th.record("t-"+strconv.Itoa(i), now)

		if got := th.size(t); got > th.max {
			t.Fatalf("after %d tokens the table holds %d entries, bound %d", i+1, got, th.max)
		}
	}

	if got := th.size(t); got == 0 {
		t.Error("the table was emptied")
	}
}

func TestTouchThrottle_BoundHoldsUnderConcurrency(t *testing.T) {
	th := &touchThrottle{max: 64}
	now := time.Now()

	var wg sync.WaitGroup

	for w := range 8 {
		wg.Go(func() {
			for i := range 2000 {
				th.record("w"+strconv.Itoa(w)+"-"+strconv.Itoa(i), now)
			}
		})
	}

	wg.Wait()

	if got := th.size(t); got > th.max {
		t.Errorf("table holds %d entries, bound %d", got, th.max)
	}
}

type touchCountingDB struct{ writes []string }

func (d *touchCountingDB) UpdateTokenLastUsed(_ context.Context, id string) error {
	d.writes = append(d.writes, id)

	return nil
}

// The prune does not weaken the throttle: a token written within the interval is
// not written again, one written longer ago is, and a token the auth cache saw
// used a moment ago is not written at all.
func TestTouchThrottle_StillThrottlesPerToken(t *testing.T) {
	th := &touchThrottle{max: 4}
	now := time.Now()

	if !th.touch(&models.Token{ID: "a"}, now) {
		t.Fatal("a token never written must be written")
	}

	if th.touch(&models.Token{ID: "a"}, now.Add(tokenTouchInterval/2)) {
		t.Error("a token written half an interval ago must not be written again")
	}

	if !th.touch(&models.Token{ID: "a"}, now.Add(tokenTouchInterval)) {
		t.Error("a token written a full interval ago must be written again")
	}

	recent := now.Add(-time.Second)
	if th.touch(&models.Token{ID: "b", LastUsedAt: &recent}, now) {
		t.Error("a token used a second ago per the cache must not be written")
	}

	// Fill the table past its bound: "a" is evicted or kept, never written twice
	// within the interval by the entries that remain.
	for i := range 20 {
		th.touch(&models.Token{ID: "x" + strconv.Itoa(i)}, now.Add(tokenTouchInterval))
	}

	if got := th.size(t); got > th.max {
		t.Errorf("table holds %d entries, bound %d", got, th.max)
	}

	// TouchToken writes through the store once and then holds back.
	db := &touchCountingDB{}
	tok := &models.Token{ID: "touch-" + strconv.FormatInt(now.UnixNano(), 10)}

	TouchToken(t.Context(), db, tok)
	TouchToken(t.Context(), db, tok)

	if len(db.writes) != 1 || db.writes[0] != tok.ID {
		t.Errorf("writes = %v, want one for %s", db.writes, tok.ID)
	}
}
