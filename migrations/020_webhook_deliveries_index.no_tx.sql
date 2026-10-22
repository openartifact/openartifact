-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Serves "recent deliveries of one webhook" (ORDER BY created_at DESC) and the
-- retention purge of finished deliveries. Built without blocking writes, so
-- this file runs outside a transaction (*.no_tx.sql).

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_deliveries_webhook_created
    ON webhook_deliveries (webhook_id, created_at DESC);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_webhook_deliveries_finished_created
    ON webhook_deliveries (created_at) WHERE status IN ('success', 'failed');
