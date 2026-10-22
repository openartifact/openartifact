-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Project tokens: service accounts.
--
-- A project token is a deploy token owned by a service user (kind 'service')
-- that belongs to exactly one project and is a member of it with the token's
-- role (viewer or developer). Service users have no password, cannot log in
-- and are hidden from user and member lists. Deleting the project deletes its
-- service users and, through them, the tokens.
SET LOCAL lock_timeout = '10s';

ALTER TABLE auth_users
    ADD COLUMN kind text NOT NULL DEFAULT 'human',
    ADD COLUMN project_id uuid REFERENCES projects(id) ON DELETE CASCADE,
    ADD CONSTRAINT chk_auth_user_kind CHECK (kind IN ('human', 'service')),
    ADD CONSTRAINT chk_service_user_project CHECK ((kind = 'service') = (project_id IS NOT NULL));

ALTER TABLE auth_tokens
    ADD COLUMN project_id uuid REFERENCES projects(id) ON DELETE CASCADE,
    ADD COLUMN created_by uuid REFERENCES auth_users(id) ON DELETE SET NULL,
    ADD COLUMN project_role text,
    ADD CONSTRAINT chk_token_project_role CHECK (project_role IS NULL OR project_role IN ('viewer', 'developer')),
    ADD CONSTRAINT chk_token_project CHECK ((project_id IS NULL) = (project_role IS NULL));

COMMENT ON COLUMN auth_users.kind IS 'human: a person; service: the account behind one project token.';
COMMENT ON COLUMN auth_users.project_id IS 'The only project a service user belongs to.';
COMMENT ON COLUMN auth_tokens.project_id IS 'Set for project tokens: the project the token belongs to.';
COMMENT ON COLUMN auth_tokens.created_by IS 'Project tokens: the user who minted the token; the token dies with that user''s membership.';
COMMENT ON COLUMN auth_tokens.project_role IS 'Project tokens: viewer or developer.';

-- A creator that is deleted loses its project tokens in the same transaction
-- (auth_tokens.created_by would otherwise just become NULL).
CREATE FUNCTION revoke_project_tokens_of_deleted_user() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE auth_users SET disabled = true
    WHERE id IN (SELECT user_id FROM auth_tokens
                 WHERE created_by = OLD.id AND project_id IS NOT NULL AND revoked_at IS NULL);
    UPDATE auth_tokens SET revoked_at = now()
    WHERE created_by = OLD.id AND project_id IS NOT NULL AND revoked_at IS NULL;
    RETURN OLD;
END;
$$;

CREATE TRIGGER trg_auth_users_revoke_project_tokens
    BEFORE DELETE ON auth_users
    FOR EACH ROW EXECUTE FUNCTION revoke_project_tokens_of_deleted_user();
