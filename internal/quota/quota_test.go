// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/database/databasetest"
)

type reserveMockDB struct {
	databasetest.BaseMockDB

	ok       bool
	err      error
	released int64
	relCount int
	relCtxOK bool
}

func (db *reserveMockDB) ReserveStorageUsage(_ context.Context, _ string, _ int64, _ int) (bool, error) {
	return db.ok, db.err
}

func (db *reserveMockDB) DecrementStorageUsage(ctx context.Context, _ string, size int64, count int) error {
	db.released += size
	db.relCount += count
	db.relCtxOK = ctx.Err() == nil

	return nil
}

func TestEnforce(t *testing.T) {
	ctx := context.Background()

	if err := Enforce(ctx, &reserveMockDB{ok: true}, "repo", 10); err != nil {
		t.Errorf("under quota: err = %v", err)
	}

	if err := Enforce(ctx, &reserveMockDB{ok: false}, "repo", 10); !errors.Is(err, ErrExceeded) {
		t.Errorf("over quota: err = %v, want ErrExceeded", err)
	}

	boom := errors.New("db down")
	if err := Enforce(ctx, &reserveMockDB{err: boom}, "repo", 10); errors.Is(err, ErrExceeded) || err == nil {
		t.Errorf("db failure must surface as a non-quota error, got %v", err)
	}
}

func TestReleaseSurvivesCancelledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	db := &reserveMockDB{}
	if err := Release(ctx, db, "repo", 42); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if db.released != 42 || db.relCount != 1 || !db.relCtxOK {
		t.Errorf("released=%d count=%d ctxOK=%v; want 42/1/true", db.released, db.relCount, db.relCtxOK)
	}
}

func TestReleaseN(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name      string
		size      int64
		count     int
		wantSize  int64
		wantCount int
		wantCalls bool
	}{
		{"batch", 300, 3, 300, 3, true},
		{"negative size is clamped", -5, 2, 0, 2, true},
		{"negative count is clamped", 7, -1, 7, 0, true},
		{"nothing to release makes no call", 0, 0, 0, 0, false},
		{"all negative makes no call", -1, -1, 0, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := &reserveMockDB{}
			if err := ReleaseN(cancelled, db, "repo", tc.size, tc.count); err != nil {
				t.Fatalf("ReleaseN: %v", err)
			}

			if db.released != tc.wantSize || db.relCount != tc.wantCount {
				t.Errorf("released %d bytes / %d artifacts, want %d / %d", db.released, db.relCount, tc.wantSize, tc.wantCount)
			}

			if tc.wantCalls && !db.relCtxOK {
				t.Error("release must be detached from a cancelled context")
			}
		})
	}
}

func TestWriteExceeded(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteExceeded(rec)

	if rec.Code != http.StatusInsufficientStorage {
		t.Errorf("status = %d, want 507", rec.Code)
	}

	if body := rec.Body.String(); !strings.Contains(body, "trashed items count until purged") || !strings.Contains(body, `"code":"quota_exceeded"`) {
		t.Errorf("body = %q, want the trash hint and quota_exceeded code", body)
	}

	if !strings.Contains(ErrExceeded.Error(), "see the repository trash") {
		t.Errorf("ErrExceeded = %q, want the trash hint", ErrExceeded)
	}
}
