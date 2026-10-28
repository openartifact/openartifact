-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Contract step of 087: storage_backends.is_default has been neither read nor
-- written since 0.13.0 (the primary store comes from STORAGE_*), so the unique
-- partial index and the column go.
--
-- Rolling-safe: no 0.13.x pod references the column (its queries name their
-- columns explicitly); 0.12.x pods did, so upgrade through 0.13.x first.
-- Dropping a column is a catalog-only change, no table rewrite.

DROP INDEX IF EXISTS idx_storage_backends_default;
ALTER TABLE storage_backends DROP COLUMN IF EXISTS is_default;
