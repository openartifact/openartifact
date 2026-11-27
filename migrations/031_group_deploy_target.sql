-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- A group repository may name one of its hosted members as its deploy target:
-- a write sent to the group is served as if it had been sent to that member.
-- The row of the target going away (or leaving the group, see
-- replaceGroupMembersTx) unsets it. The column is not journaled
-- (internal/recovery/tables.go): a rebuild inserts repositories in chunks, and
-- the target may not be restored yet when the group is.
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.repositories
    ADD COLUMN deploy_target_id uuid REFERENCES public.repositories (id) ON DELETE SET NULL,
    ADD CONSTRAINT repositories_deploy_target_not_self CHECK (deploy_target_id IS DISTINCT FROM id);
