-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- A blob upload session belongs to one image of one repository. The session
-- URL carries the image (/v2/<repo>/<image>/blobs/uploads/<uuid>) and every
-- request on it must name the image it was started under; a different image
-- answers 404 BLOB_UPLOAD_UNKNOWN. The empty string is the repository's root
-- image, so NULL is reserved for the sessions that predate this column.
--
-- Those sessions have no recorded image and are not rebound: they answer
-- 404 BLOB_UPLOAD_UNKNOWN like an unknown session and the client pushes the
-- layer again (the idle-session sweep removes their rows and staged data).
-- Upload sessions are transient, so nothing is migrated.
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.docker_uploads
    ADD COLUMN image text;

COMMENT ON COLUMN public.docker_uploads.image IS 'Image the session was started for; NULL for sessions that predate the column, which no request can reach';
