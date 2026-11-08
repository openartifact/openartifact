-- SPDX-FileCopyrightText: The OpenArtifact Authors
--
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Index of the signatures, in-toto attestations and SBOMs attached to docker
-- images (cosign ".sig/.att/.sbom" tags, Sigstore bundle and DSSE referrers),
-- with the outcome of verifying them against the repository's trust
-- configuration, and that configuration (repository_attestation_trust).
-- An attestation row belongs to the manifest that carries it (manifest_id): it
-- goes when that manifest is deleted for good. Both tables are deliberately not
-- journaled (internal/recovery NotJournaled): the index is derived from the
-- manifests and rebuilt by the reverify endpoint, the trust configuration is
-- operator policy restored with database backups.
SET LOCAL lock_timeout = '10s';

CREATE TABLE public.oci_attestations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    repository_id uuid NOT NULL,
    image text DEFAULT '' NOT NULL,
    subject_digest text NOT NULL,
    attestation_digest text NOT NULL,
    manifest_id uuid NOT NULL,
    predicate_type text DEFAULT '' NOT NULL,
    kind text NOT NULL,
    signer_identity text DEFAULT '' NOT NULL,
    issuer text DEFAULT '' NOT NULL,
    verified boolean DEFAULT false NOT NULL,
    verify_error text DEFAULT '' NOT NULL,
    payload_blob text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT oci_attestations_pkey PRIMARY KEY (id),
    CONSTRAINT oci_attestations_key UNIQUE (repository_id, image, subject_digest, attestation_digest),
    CONSTRAINT oci_attestations_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE,
    CONSTRAINT oci_attestations_manifest_id_fkey FOREIGN KEY (manifest_id) REFERENCES public.docker_manifests(id) ON DELETE CASCADE,
    CONSTRAINT oci_attestations_kind_check CHECK (kind IN ('signature', 'attestation', 'sbom'))
);

COMMENT ON TABLE public.oci_attestations IS 'Signatures, in-toto attestations and SBOMs attached to docker images, with their verification result';
COMMENT ON COLUMN public.oci_attestations.attestation_digest IS 'sha256:<hex> of the signed envelope (the layer blob that carries it)';
COMMENT ON COLUMN public.oci_attestations.manifest_id IS 'Latest docker manifest that carries the envelope (the cosign tag manifest or the referrer)';
COMMENT ON COLUMN public.oci_attestations.payload_blob IS 'Blob store key (sha256 hex) of the envelope';
COMMENT ON COLUMN public.oci_attestations.created_at IS 'When the envelope was first seen; the verification time of a short-lived Fulcio certificate';

CREATE INDEX idx_oci_attestations_subject ON public.oci_attestations USING btree (repository_id, subject_digest);
CREATE INDEX idx_oci_attestations_manifest ON public.oci_attestations USING btree (manifest_id);

CREATE TABLE public.repository_attestation_trust (
    repository_id uuid NOT NULL,
    config jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT repository_attestation_trust_pkey PRIMARY KEY (repository_id),
    CONSTRAINT repository_attestation_trust_repository_id_fkey FOREIGN KEY (repository_id) REFERENCES public.repositories(id) ON DELETE CASCADE
);

COMMENT ON TABLE public.repository_attestation_trust IS 'Per-repository trust configuration for attestation verification (public keys, Fulcio roots and identities)';
