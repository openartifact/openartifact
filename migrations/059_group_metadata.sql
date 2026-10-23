-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Deb and RPM group repositories (docs/repositories/hosted-proxy-group.md):
-- the merged Release/Packages and repodata of a group are generated into
-- repository_metadata rows of the group itself, like a hosted repository's.
--
--   * group_metadata_state: one row per group. stale is set whenever a member
--     (or the group) changes and cleared by the regeneration that consumed the
--     change (updated_at is the compare-and-clear marker); refresh_after is when
--     proxy members' cached upstream metadata may have changed (the earliest of
--     their cache TTLs); dists are the Debian distributions the group serves
--     beyond those its hosted members hold (a proxy's distributions cannot be
--     listed, only asked for).
--   * group_package_owners: the member that owns every file the merged index
--     lists (a Debian pool path, an RPM location), so a download is served from
--     exactly the member whose checksum the index advertises.
--
-- Only new tables; the member index is built in the following .no_tx.sql.

SET LOCAL lock_timeout = '10s';

CREATE TABLE group_metadata_state (
    repository_id uuid NOT NULL,
    stale boolean DEFAULT true NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    generated_at timestamp with time zone,
    refresh_after timestamp with time zone,
    dists jsonb DEFAULT '[]'::jsonb NOT NULL,
    CONSTRAINT group_metadata_state_pkey PRIMARY KEY (repository_id),
    CONSTRAINT group_metadata_state_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES repositories(id) ON DELETE CASCADE
);

CREATE TABLE group_package_owners (
    group_repo_id uuid NOT NULL,
    path text NOT NULL,
    member_repo_id uuid NOT NULL,
    CONSTRAINT group_package_owners_pkey PRIMARY KEY (group_repo_id, path),
    CONSTRAINT group_package_owners_group_repo_id_fkey FOREIGN KEY (group_repo_id) REFERENCES repositories(id) ON DELETE CASCADE,
    CONSTRAINT group_package_owners_member_repo_id_fkey FOREIGN KEY (member_repo_id) REFERENCES repositories(id) ON DELETE CASCADE
);
