-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Helpers of the per-format import functions (migrations 100-120).

-- oa_derived_id is the deterministic id of a generated companion row of a
-- legacy row (a gomod .mod file next to its zip): the same legacy id and tag
-- always yield the same uuid, so importing twice does not duplicate it.
CREATE FUNCTION oa_derived_id(p_id uuid, p_tag text) RETURNS uuid
LANGUAGE sql IMMUTABLE AS $$
    SELECT md5(p_id::text || ':' || p_tag)::uuid
$$;

-- oa_epoch is a timestamptz as whole seconds since the epoch (0 for NULL),
-- the representation package_files.attrs keeps build times in.
CREATE FUNCTION oa_epoch(p_at timestamptz) RETURNS bigint
LANGUAGE sql IMMUTABLE AS $$
    SELECT COALESCE(EXTRACT(EPOCH FROM p_at)::bigint, 0)
$$;
