// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package quota

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openartifact/openartifact/internal/database"
)

type reserveMockDB struct {
	database.BaseMockDB

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

func TestWriteExceeded(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteExceeded(rec)

	if rec.Code != http.StatusInsufficientStorage {
		t.Errorf("status = %d, want 507", rec.Code)
	}
}
