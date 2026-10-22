-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Group repositories: hosted members win a package name.
--
-- By default a group no longer merges a proxy member's versions or files of a
-- package into the view of a package that a hosted member also holds (the
-- dependency-confusion window: someone publishes internal-lib@99.0.0 on the
-- public registry a proxy member mirrors). Setting merge_proxy_versions turns
-- the old union behavior back on for that group. Existing groups get the safe
-- default (false). Metadata-only column add.

SET LOCAL lock_timeout = '10s';

ALTER TABLE repositories
    ADD COLUMN merge_proxy_versions boolean DEFAULT false NOT NULL;

COMMENT ON COLUMN repositories.merge_proxy_versions IS 'Groups only. false (default): when a hosted member holds a package name, proxy members'' versions and files of that name are hidden. true: versions of all members are merged.';
