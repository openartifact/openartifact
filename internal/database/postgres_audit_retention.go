// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

var _ AuditRetentionStore = (*PostgresDB)(nil)

// AuditRetentionStore deletes audit entries past their retention window. It is
// implemented by *PostgresDB and found by the retention worker with a type
// assertion on Database.
type AuditRetentionStore interface {
	// PurgeAuditLogsBefore deletes up to limit audit entries created before
	// cutoff, oldest first, and returns how many it removed. A result below
	// limit means nothing older than cutoff is left.
	PurgeAuditLogsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error)
}

// PurgeAuditLogsBefore implements AuditRetentionStore.
func (p *PostgresDB) PurgeAuditLogsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	n, err := p.q.PurgeAuditLogsBefore(ctx, dbsqlc.PurgeAuditLogsBeforeParams{
		Cutoff: cutoff,
		Batch:  toInt32(limit),
	})
	if err != nil {
		return 0, fmt.Errorf("purge audit logs: %w", err)
	}

	return n, nil
}
