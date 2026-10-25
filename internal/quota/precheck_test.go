// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import (
	"context"
	"errors"
	"testing"

	"github.com/openartifact/openartifact/internal/database/databasetest"
	"github.com/openartifact/openartifact/internal/database/models"
)

type quotaRowDB struct {
	databasetest.BaseMockDB

	q *models.StorageQuota
}

func (d *quotaRowDB) GetOrCreateStorageQuota(context.Context, string) (*models.StorageQuota, error) {
	return d.q, nil
}

func TestPrecheck(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name string
		q    *models.StorageQuota
		size int64
		want error
	}{
		{"unlimited", &models.StorageQuota{}, 1 << 40, nil},
		{"fits", &models.StorageQuota{MaxSizeBytes: 100, CurrentSizeBytes: 40}, 60, nil},
		{"one byte over", &models.StorageQuota{MaxSizeBytes: 100, CurrentSizeBytes: 40}, 61, ErrExceeded},
		{"unknown length passes", &models.StorageQuota{MaxSizeBytes: 100, CurrentSizeBytes: 100}, -1, nil},
		{"artifact count exhausted", &models.StorageQuota{MaxArtifactCount: 2, CurrentArtifactCount: 2}, 1, ErrExceeded},
		{"no quota row", nil, 10, nil},
	}

	for _, tc := range tests {
		got := Precheck(ctx, &quotaRowDB{q: tc.q}, "r", tc.size)
		if !errors.Is(got, tc.want) {
			t.Errorf("%s: Precheck = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A quota lookup failure is advisory: the reservation reports it.
	if err := Precheck(ctx, &databasetest.BaseMockDB{}, "r", 10); err != nil {
		t.Errorf("lookup failure: %v", err)
	}
}

func TestRoom(t *testing.T) {
	ctx := context.Background()

	room, limited, err := Room(ctx, &quotaRowDB{q: &models.StorageQuota{MaxSizeBytes: 100, CurrentSizeBytes: 130}}, "r")
	if err != nil || !limited || room != 0 {
		t.Errorf("over-full: room=%d limited=%v err=%v, want 0 true nil", room, limited, err)
	}

	room, limited, _ = Room(ctx, &quotaRowDB{q: &models.StorageQuota{MaxSizeBytes: 100, CurrentSizeBytes: 30}}, "r")
	if !limited || room != 70 {
		t.Errorf("room=%d limited=%v, want 70 true", room, limited)
	}

	if _, limited, _ = Room(ctx, &quotaRowDB{q: &models.StorageQuota{}}, "r"); limited {
		t.Error("no max size must be unlimited")
	}
}
