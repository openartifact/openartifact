-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- A chunked blob upload keeps one object per PATCH in the blob store instead of
-- one file in a replica-local directory, so any replica can take the next
-- request of a session. This table is the session's ordered list of those
-- objects; docker_uploads.offset stays the sum of their sizes.
SET LOCAL lock_timeout = '5s';

CREATE TABLE public.docker_upload_chunks (
    chunk_id uuid NOT NULL,
    upload_id uuid NOT NULL,
    seq integer NOT NULL,
    size_bytes bigint NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT docker_upload_chunks_pkey PRIMARY KEY (chunk_id),
    CONSTRAINT docker_upload_chunks_upload_seq_key UNIQUE (upload_id, seq),
    CONSTRAINT docker_upload_chunks_size_check CHECK (size_bytes > 0),
    CONSTRAINT docker_upload_chunks_upload_id_fkey FOREIGN KEY (upload_id) REFERENCES public.docker_uploads(id) ON DELETE CASCADE
);

COMMENT ON TABLE public.docker_upload_chunks IS 'Chunks (blob store objects) of an in-progress blob upload session, in upload order';
