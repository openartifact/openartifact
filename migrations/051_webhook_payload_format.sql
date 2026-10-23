-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Per-webhook payload format (docs/operations/webhooks.md): the native
-- OpenArtifact JSON (default, unchanged), CloudEvents 1.0 structured mode, or a
-- chat service's incoming-webhook shape.
--
-- webhooks is a small admin-managed table, so the CHECK constraint is added and
-- validated in this transaction. artifact.downloaded was subscribable but never
-- emitted, and is no longer an accepted event: drop it from existing
-- subscriptions so the stored values stay inside the accepted set.

SET LOCAL lock_timeout = '10s';

ALTER TABLE webhooks
    ADD COLUMN payload_format text NOT NULL DEFAULT 'openartifact',
    ADD CONSTRAINT webhooks_payload_format_check
        CHECK (payload_format IN ('openartifact', 'cloudevents', 'slack', 'teams', 'discord'));

COMMENT ON COLUMN webhooks.payload_format IS 'Delivery body format: openartifact, cloudevents, slack, teams or discord.';

UPDATE webhooks SET events = array_remove(events, 'artifact.downloaded') WHERE 'artifact.downloaded' = ANY(events);
