// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package worker

import (
	"context"
	"database/sql"
	"time"

	"github.com/openartifact/openartifact/pkg/logger"
)

// AuthPurgeStore is the part of the database AuthPurgeWorker uses needs.
type AuthPurgeStore interface {
	GetDB() *sql.DB
}

// AuthPurgeWorker periodically deletes expired sessions, long-expired or
// revoked API tokens (and the service accounts of project tokens) and finished webhook deliveries so those tables do not
// grow without bound.
type AuthPurgeWorker struct {
	*lifecycle

	db       AuthPurgeStore
	log      *logger.Logger
	interval time.Duration
	started  bool

	// webhookRetentionDays: age after which finished webhook deliveries are
	// deleted; 0 keeps them.
	webhookRetentionDays int
}

func NewAuthPurgeWorker(db AuthPurgeStore, log *logger.Logger) *AuthPurgeWorker {
	return &AuthPurgeWorker{
		lifecycle: newLifecycle(),
		db:        db,
		log:       log,
		interval:  time.Hour,
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

	ctx, cancel := context.WithTimeout(w.runCtx, time.Minute)
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
	w.stopGracefully(w.started)
}
