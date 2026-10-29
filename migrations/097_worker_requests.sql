-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Requests a web process hands to a worker process (docs/operations/scaling.md):
-- the metadata regeneration of a package or repository, an AI analysis, a
-- vulnerability scan, a Dependency-Track push. A process started with
-- server.role=web starts no background worker, so what a request would have
-- scheduled in memory is written here and picked up by any process that runs
-- the workers (role all or worker).
--
-- Rolling-safe: a new table nothing else references. One row per (kind, key):
-- a repeated request for the same key refreshes requested_at instead of adding
-- a row, which is what coalesces a burst of uploads into one regeneration.

SET LOCAL lock_timeout = '10s';

CREATE TABLE worker_requests (
    kind character varying(64) NOT NULL,
    key text NOT NULL,
    payload jsonb,
    requested_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    claimed_until timestamp with time zone,
    CONSTRAINT worker_requests_pkey PRIMARY KEY (kind, key)
);

CREATE INDEX idx_worker_requests_requested_at ON worker_requests USING btree (requested_at);

COMMENT ON TABLE worker_requests IS 'Work a web process asks a worker process to do. One row per (kind, key); a repeated request refreshes requested_at. Rows are deleted once the work they asked for finished.';
COMMENT ON COLUMN worker_requests.kind IS 'What to do, e.g. regen:npm, regen:maven, regen:group, ai:analyze, vuln:scan, dtrack:push.';
COMMENT ON COLUMN worker_requests.key IS 'What to do it to: a repository or package id, or a hash of the payload when the subject has several parts.';
COMMENT ON COLUMN worker_requests.payload IS 'The subject of kinds whose key is only a hash; null otherwise.';
COMMENT ON COLUMN worker_requests.requested_at IS 'When the work was last requested; a worker deletes only the requests it has seen, so one made while it worked survives.';
COMMENT ON COLUMN worker_requests.claimed_until IS 'While in the future, a worker holds the request; a worker that died leaves it to expire and another takes it over. Null: unclaimed.';
