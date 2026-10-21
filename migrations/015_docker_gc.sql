-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Registry garbage collection (untagged manifests and the blobs only they used).
--
-- docker_manifest_refs records, per manifest, which blobs and child manifests
-- its body references, so GC can answer "is this blob still used by any
-- manifest of the repository?" in SQL, inside the transaction that deletes it,
-- instead of re-reading every manifest body from the blob store. Rows are
-- written together with the manifest on push; manifests that predate this
-- table (or were restored from a backup) are indexed lazily by the GC worker.
-- A kind='indexed' row with an empty digest marks a manifest whose references
-- are fully recorded, so a manifest without any blob reference is not mistaken
-- for an unindexed one.
CREATE TABLE docker_manifest_refs (
    manifest_id uuid NOT NULL,
    repository_id uuid NOT NULL,
    image text DEFAULT '' NOT NULL,
    kind text NOT NULL,
    digest text DEFAULT '' NOT NULL,
    CONSTRAINT docker_manifest_refs_pkey PRIMARY KEY (manifest_id, kind, digest),
    CONSTRAINT docker_manifest_refs_kind_check CHECK (kind IN ('blob', 'manifest', 'indexed')),
    CONSTRAINT docker_manifest_refs_manifest_id_fkey FOREIGN KEY (manifest_id) REFERENCES docker_manifests(id) ON DELETE CASCADE
);

CREATE INDEX idx_docker_manifest_refs_lookup ON docker_manifest_refs (repository_id, kind, digest);

COMMENT ON TABLE docker_manifest_refs IS 'References held by a manifest body: kind=blob (config, layers), kind=manifest (index children); kind=indexed marks the manifest as fully indexed.';
COMMENT ON COLUMN docker_manifest_refs.repository_id IS 'Denormalised from docker_manifests so blob references can be looked up per repository without a join.';
COMMENT ON COLUMN docker_manifest_refs.image IS 'Image of the referencing manifest; child manifests are resolved inside the same image.';

-- Last time a client relied on the blob (uploaded, mounted, re-pushed or probed
-- with HEAD). GC never removes a blob used more recently than its grace period,
-- which covers a push that has verified a layer exists but not yet pushed the
-- manifest referencing it. Existing rows start at migration time.
ALTER TABLE docker_blobs ADD COLUMN last_used_at timestamp with time zone DEFAULT now() NOT NULL;

COMMENT ON COLUMN docker_blobs.last_used_at IS 'Last upload, mount, re-push or HEAD of this blob in the repository; registry GC keeps blobs used within its grace period.';

-- Referrers lookup (subject -> manifests) and the GC subject checks.
CREATE INDEX idx_docker_manifests_subject ON docker_manifests (repository_id, image, subject_digest) WHERE subject_digest IS NOT NULL;
