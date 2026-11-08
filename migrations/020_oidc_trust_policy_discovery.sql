-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- How a CI OIDC trust policy finds the signing keys of its issuer.
--
-- discovery_mode: anonymous (the discovery document and the key set are read
-- without credentials, as before), static (the public key set stored in jwks is
-- the only key source; nothing is fetched) or serviceaccount (OpenArtifact
-- reads the issuer with its own Kubernetes service account token).
-- allow_private_issuer lets that policy's issuer and jwks_uri resolve to
-- private or loopback addresses, for a cluster-internal issuer.
-- Existing rows keep the behaviour they had: anonymous, private addresses
-- refused unless the process-wide switch allows them.
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.oidc_trust_policies
    ADD COLUMN discovery_mode text DEFAULT 'anonymous' NOT NULL,
    ADD COLUMN jwks jsonb,
    ADD COLUMN allow_private_issuer boolean DEFAULT false NOT NULL,
    ADD CONSTRAINT oidc_trust_policies_discovery_mode_check
        CHECK (discovery_mode = ANY (ARRAY['anonymous'::text, 'static'::text, 'serviceaccount'::text])),
    ADD CONSTRAINT oidc_trust_policies_jwks_check
        CHECK ((discovery_mode = 'static') = (jwks IS NOT NULL) AND (jwks IS NULL OR jsonb_typeof(jwks) = 'object'));

COMMENT ON COLUMN public.oidc_trust_policies.discovery_mode IS 'anonymous, static or serviceaccount: how the issuer keys are found';
COMMENT ON COLUMN public.oidc_trust_policies.jwks IS 'Public JWKS of the static discovery mode; NULL otherwise';
COMMENT ON COLUMN public.oidc_trust_policies.allow_private_issuer IS 'The issuer and its jwks_uri may resolve to private or loopback addresses';
