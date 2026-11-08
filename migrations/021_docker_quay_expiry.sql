-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Quay-style image expiry. A docker repository can opt in to honour the
-- "quay.expires-after" manifest annotation / image config label (Konflux sets
-- it on pull-request builds and Trusted Artifacts); the registry stores the
-- resulting deadline on the manifest row and a worker pass removes manifests
-- past it. Both columns are metadata-only additions: nothing changes on
-- upgrade (the setting defaults to off, existing manifests never expire).
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.repositories
    ADD COLUMN honour_quay_expiry boolean DEFAULT false NOT NULL;

COMMENT ON COLUMN public.repositories.honour_quay_expiry IS 'Hosted docker repositories only: delete manifests once their quay.expires-after deadline (docker_manifests.expires_at) has passed.';

ALTER TABLE public.docker_manifests
    ADD COLUMN expires_at timestamp with time zone;

COMMENT ON COLUMN public.docker_manifests.expires_at IS 'Deadline derived from the quay.expires-after annotation or image label at push time; NULL when the manifest declares none. Only enforced in repositories with honour_quay_expiry.';
