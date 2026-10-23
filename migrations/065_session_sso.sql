-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Remembers which OIDC provider created a session and the ID token it issued
-- (encrypted at rest), so logout can run RP-initiated logout with an
-- id_token_hint. Rows disappear with their session.
SET LOCAL lock_timeout = '5s';

CREATE TABLE auth_session_sso (
    session_id    uuid PRIMARY KEY REFERENCES auth_sessions(id) ON DELETE CASCADE,
    provider_slug text NOT NULL,
    id_token      text NOT NULL DEFAULT '',
    created_at    timestamp with time zone NOT NULL DEFAULT now()
);
