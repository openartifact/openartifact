-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- A generated bootstrap admin password is only valid for a limited time. NULL
-- (every existing row) means the password never expires. Metadata-only ADD
-- COLUMN; nothing reads it before this release writes it.

SET LOCAL lock_timeout = '10s';

ALTER TABLE auth_users ADD COLUMN IF NOT EXISTS password_expires_at timestamp with time zone;

COMMENT ON COLUMN auth_users.password_expires_at IS 'After this instant the password is rejected at login (generated bootstrap password); NULL = never. Cleared whenever the password changes.';
