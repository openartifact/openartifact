-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- A chunked blob upload is now ONE growing object in the blob store instead of
-- one object per PATCH (docker_upload_chunks). The session row carries
-- everything a replica needs to take the next request without re-reading the
-- bytes already stored: the marshalled sha256 state of the first "offset"
-- bytes, an opaque storage token for the object (multipart parts, block list),
-- and a short lease so one request at a time works on the session.
--
-- Sessions started by the previous release lose their chunk rows with the
-- table: their next request answers 404 BLOB_UPLOAD_UNKNOWN and the client
-- pushes the layer again. Upload sessions are transient (idle sessions are
-- swept after hours), so nothing is migrated.
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.docker_uploads
    ADD COLUMN hash_state bytea,
    ADD COLUMN stage_state text DEFAULT '' NOT NULL,
    ADD COLUMN lease_token uuid,
    ADD COLUMN lease_until timestamp with time zone;

COMMENT ON COLUMN public.docker_uploads.hash_state IS 'Marshalled sha256 state of the first "offset" bytes; NULL while the offset is 0';
COMMENT ON COLUMN public.docker_uploads.stage_state IS 'Opaque blob store token of the upload object (multipart parts, committed blocks); empty for a store that needs none';
COMMENT ON COLUMN public.docker_uploads.lease_token IS 'Token of the request currently working on the session; NULL when idle';
COMMENT ON COLUMN public.docker_uploads.lease_until IS 'When the lease expires if its holder dies; NULL when idle';

DROP TABLE public.docker_upload_chunks;
