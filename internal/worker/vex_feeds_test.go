// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type fakeVEXFeeds struct {
	feeds   []*models.VEXFeed
	synced  []string
	applies int
}

func (f *fakeVEXFeeds) ListVEXFeeds(context.Context) ([]*models.VEXFeed, error) { return f.feeds, nil }

func (f *fakeVEXFeeds) SyncFeed(_ context.Context, id string) (models.VEXFeedSyncResult, error) {
	f.synced = append(f.synced, id)

	return models.VEXFeedSyncResult{Success: true}, nil
}

func (f *fakeVEXFeeds) ApplyFeeds(context.Context) error {
	f.applies++

	return nil
}

func TestVEXFeedsWorkerSyncsDueFeedsAndAppliesHourly(t *testing.T) {
	now := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time {
		ts := now.Add(-d)

		return &ts
	}

	fake := &fakeVEXFeeds{feeds: []*models.VEXFeed{
		{ID: "never", Enabled: true, IntervalHours: 24},
		{ID: "recent", Enabled: true, IntervalHours: 24, LastAttemptAt: at(time.Hour)},
		{ID: "disabled", Enabled: false, IntervalHours: 24},
		{ID: "stale", Enabled: true, IntervalHours: 24, LastAttemptAt: at(25 * time.Hour)},
	}}

	log, err := logger.New("error", "console", "stderr")
	if err != nil {
		t.Fatal(err)
	}

	w := NewVEXFeedsWorker(fake, fake, log)
	w.now = func() time.Time { return now }

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if want := []string{"never", "stale"}; !slices.Equal(fake.synced, want) {
		t.Errorf("synced = %v, want %v", fake.synced, want)
	}

	if fake.applies != 1 {
		t.Errorf("applies = %d, want 1", fake.applies)
	}

	// Within the hour the feeds are not rematerialized again.
	now = now.Add(30 * time.Minute)

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if fake.applies != 1 {
		t.Errorf("applies after 30 minutes = %d, want 1", fake.applies)
	}

	now = now.Add(31 * time.Minute)

	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if fake.applies != 2 {
		t.Errorf("applies after an hour = %d, want 2", fake.applies)
	}
}
