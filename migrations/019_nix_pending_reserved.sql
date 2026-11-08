-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- A staged NAR file now holds a storage-quota reservation from the moment it is
-- uploaded: reserved_bytes is what to hand back when the staging row is swept
-- (or move to the path when its narinfo arrives). Rows staged before this
-- migration reserved nothing, so they keep 0.
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.nix_pending_nars
    ADD COLUMN reserved_bytes bigint DEFAULT 0 NOT NULL;

COMMENT ON COLUMN public.nix_pending_nars.reserved_bytes IS 'Bytes reserved in the repository storage quota while the NAR waits for its narinfo';
