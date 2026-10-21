-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Serve-stale for proxy repositories: when revalidating an expired cache entry
-- fails (network error, timeout, upstream 5xx) the cached copy may still be
-- served for up to max_stale_seconds past its expiry. 0 disables serve-stale.
-- The eviction worker keeps entries for that long before deleting them.
ALTER TABLE repositories
    ADD COLUMN IF NOT EXISTS max_stale_seconds integer DEFAULT 604800 NOT NULL;
