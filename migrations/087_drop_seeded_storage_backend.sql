-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- 001 seeded a storage_backends row ('local', filesystem, is_default, ./storage)
-- on every install. It never described the real primary store (that comes from
-- the server configuration, see docs/operations/storage.md), nothing read its
-- is_default flag, and the admin UI showed it as an unreachable backend next to
-- the real, invisible one.
--
-- The row is removed only while it is still exactly what 001 wrote and nothing
-- uses it: an operator who renamed it, re-pointed it, made it a tier or placed
-- blobs on it keeps it. Rolling-safe: pods of the previous release only list
-- and test rows, so they keep serving while this runs. The is_default column
-- and its index stay until a later contract migration (no pod of the previous
-- release writes them any more).
SET LOCAL lock_timeout = '10s';

DELETE FROM storage_backends sb
WHERE sb.name = 'local'
  AND sb.kind = 'filesystem'
  AND sb.config = '{"path": "./storage"}'::jsonb
  AND NOT EXISTS (SELECT 1 FROM storage_tiers t WHERE t.backend_id = sb.id)
  AND NOT EXISTS (SELECT 1 FROM storage_tier_moves m WHERE m.from_backend_id = sb.id OR m.to_backend_id = sb.id)
  AND NOT EXISTS (SELECT 1 FROM blobs b WHERE b.storage_backend_id = sb.id)
  AND NOT EXISTS (SELECT 1 FROM blob_deletions d WHERE d.scope = sb.id::text);
