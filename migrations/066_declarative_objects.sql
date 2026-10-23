-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Ownership markers of `oa apply`: which objects the declarative CLI created or
-- adopted, so --prune only ever deletes managed objects.
SET LOCAL lock_timeout = '5s';

CREATE TABLE declarative_objects (
    kind         text NOT NULL CHECK (char_length(kind) BETWEEN 1 AND 64),
    name         text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 512),
    object_id    text NOT NULL CHECK (char_length(object_id) BETWEEN 1 AND 256),
    applied_hash text NOT NULL DEFAULT '' CHECK (char_length(applied_hash) <= 128),
    applied_at   timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, name)
);

COMMENT ON TABLE declarative_objects IS
    'Objects created or adopted by oa apply; object_id is the server-side id at apply time, so a deleted-and-recreated object is not claimed by a stale marker.';
