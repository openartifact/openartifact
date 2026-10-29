// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"fmt"
)

var _ BlobDeletionStore = (*PostgresDB)(nil)

// PurgeExpiredBackupLeases removes the leases of backups that stopped renewing
// them (crashed). An expired lease already blocks nothing; this only tidies up.
func (p *BackupStore) PurgeExpiredBackupLeases(ctx context.Context) (int64, error) {
	n, err := p.q.DeleteExpiredBackupLeases(ctx)
	if err != nil {
		return 0, fmt.Errorf("purge expired backup leases: %w", err)
	}

	return n, nil
}
