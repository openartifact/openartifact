// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

// AuthPurgeWorker periodically deletes expired sessions, long-expired or
// revoked API tokens (and the service accounts of project tokens) and finished webhook deliveries so those tables do not
// grow without bound.
type AuthPurgeWorker struct {
	db       database.Database
	log      *logger.Logger
	interval time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
	started  bool

	// webhookRetentionDays: age after which finished webhook deliveries are
	// deleted; 0 keeps them.
	webhookRetentionDays int
	ctx                  context.Context //nolint:containedctx // worker lifetime context, cancelled by Stop
	cancel               context.CancelFunc
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

// SetWebhookDeliveryRetention sets how many days finished webhook deliveries
// are kept (0: forever). Call before Start.
func (w *AuthPurgeWorker) SetWebhookDeliveryRetention(days int) {
	w.webhookRetentionDays = days
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

	// Service accounts whose tokens are all gone (revoked or expired, then purged above).
	res, err = sqlDB.ExecContext(ctx, `
		DELETE FROM auth_users u
		WHERE u.kind = 'service'
		  AND NOT EXISTS (SELECT 1 FROM auth_tokens t WHERE t.user_id = u.id)`)
	if err != nil {
		w.log.Errorw("failed to purge project token service accounts", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		w.log.Infow("purged project token service accounts", "count", n)
	}

	w.purgeWebhookDeliveries(ctx, sqlDB)
}

// purgeWebhookDeliveries deletes finished deliveries older than the retention.
// Pending ones belong to the retry loop and are never touched.
func (w *AuthPurgeWorker) purgeWebhookDeliveries(ctx context.Context, sqlDB *sql.DB) {
	if w.webhookRetentionDays <= 0 {
		return
	}

	res, err := sqlDB.ExecContext(ctx,
		`DELETE FROM webhook_deliveries WHERE status IN ('success', 'failed') AND created_at < now() - make_interval(days => $1)`,
		w.webhookRetentionDays)
	if err != nil {
		w.log.Errorw("failed to purge webhook deliveries", "error", err)
	} else if n, _ := res.RowsAffected(); n > 0 {
		w.log.Infow("purged webhook deliveries", "count", n)
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
