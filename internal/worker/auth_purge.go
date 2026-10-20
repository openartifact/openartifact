// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

// AuthPurgeWorker periodically deletes expired sessions and long-expired or
// revoked API tokens so the auth tables do not grow without bound.
type AuthPurgeWorker struct {
	db       database.Database
	log      *logger.Logger
	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
	started  bool
	ctx      context.Context //nolint:containedctx // worker lifetime context, cancelled by Stop
	cancel   context.CancelFunc
}

func NewAuthPurgeWorker(db database.Database, log *logger.Logger) *AuthPurgeWorker {
	ctx, cancel := context.WithCancel(context.Background())

	return &AuthPurgeWorker{
		db:       db,
		log:      log,
		interval: time.Hour,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
	}
}

func (w *AuthPurgeWorker) Start() {
	w.started = true

	go func() {
		defer close(w.doneCh)

		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		w.purgeSafely()

		for {
			select {
			case <-ticker.C:
				w.purgeSafely()
			case <-w.stopCh:
				return
			}
		}
	}()
}

func (w *AuthPurgeWorker) purgeSafely() {
	defer func() {
		if r := recover(); r != nil {
			w.log.Errorw("panic in auth purge", "panic", r)
		}
	}()

	ctx, cancel := context.WithTimeout(w.ctx, time.Minute)
	defer cancel()

	sqlDB := w.db.GetDB()
	if sqlDB == nil {
		return
	}

	res, err := sqlDB.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at < now()`)
	if err != nil {
		w.log.Errorw("failed to purge expired sessions", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		w.log.Infow("purged expired sessions", "count", n)
	}

	res, err = sqlDB.ExecContext(ctx, `
		DELETE FROM auth_tokens
		WHERE (expires_at IS NOT NULL AND expires_at < now() - interval '30 days')
		   OR (revoked_at IS NOT NULL AND revoked_at < now() - interval '30 days')`)
	if err != nil {
		w.log.Errorw("failed to purge expired tokens", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		w.log.Infow("purged expired tokens", "count", n)
	}
}

func (w *AuthPurgeWorker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })

	if !w.started {
		w.cancel()
		return
	}

	select {
	case <-w.doneCh:
	case <-time.After(10 * time.Second):
		w.cancel()
		<-w.doneCh
	}

	w.cancel()
}
