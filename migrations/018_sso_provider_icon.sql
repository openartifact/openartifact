-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- The login page shows a vendor icon per SSO provider. The empty string keeps
-- the automatic choice (derived from the provider's configuration when it is
-- listed); a key such as 'okta' or 'keycloak' overrides it. Additive: existing
-- providers stay automatic.
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.auth_sso_providers
    ADD COLUMN icon text DEFAULT ''::text NOT NULL;

COMMENT ON COLUMN public.auth_sso_providers.icon IS 'Icon key chosen by an admin (see internal/auth ssoicon.go); empty means automatic';
