-- name: UpsertOCIAttestation :one
-- One row per (image, subject, envelope): a re-push keeps the row and its
-- created_at (the first time the envelope was seen, the verification time of a
-- short-lived certificate) and moves it to the manifest that carries it now.
INSERT INTO oci_attestations (repository_id, image, subject_digest, attestation_digest, manifest_id, predicate_type,
                              kind, signer_identity, issuer, verified, verify_error, payload_blob, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, sqlc.arg(created_at)::timestamptz)
ON CONFLICT (repository_id, image, subject_digest, attestation_digest) DO UPDATE SET
    manifest_id = EXCLUDED.manifest_id,
    predicate_type = EXCLUDED.predicate_type,
    kind = EXCLUDED.kind,
    signer_identity = EXCLUDED.signer_identity,
    issuer = EXCLUDED.issuer,
    verified = EXCLUDED.verified,
    verify_error = EXCLUDED.verify_error,
    payload_blob = EXCLUDED.payload_blob,
    updated_at = now()
RETURNING id, created_at;

-- name: GetOCIAttestationFirstSeen :one
SELECT created_at FROM oci_attestations
WHERE repository_id = $1 AND image = $2 AND subject_digest = $3 AND attestation_digest = $4;

-- name: ListOCIAttestations :many
-- The attestations of one subject digest (of one image when image is set),
-- carried by manifests that are not in the trash.
SELECT a.id, a.repository_id, a.image, a.subject_digest, a.attestation_digest, a.manifest_id, a.predicate_type, a.kind,
       a.signer_identity, a.issuer, a.verified, a.verify_error, a.payload_blob, a.created_at, a.updated_at
FROM oci_attestations a
JOIN docker_manifests m ON m.id = a.manifest_id
WHERE a.repository_id = sqlc.arg(repository_id)::uuid AND a.subject_digest = sqlc.arg(subject_digest)::text
  AND (sqlc.arg(image)::text = '' OR a.image = sqlc.arg(image)::text)
  AND m.deleted_at IS NULL
ORDER BY a.created_at, a.attestation_digest;

-- name: ListOCIAttestationsByDigest :many
SELECT a.id, a.repository_id, a.image, a.subject_digest, a.attestation_digest, a.manifest_id, a.predicate_type, a.kind,
       a.signer_identity, a.issuer, a.verified, a.verify_error, a.payload_blob, a.created_at, a.updated_at
FROM oci_attestations a
JOIN docker_manifests m ON m.id = a.manifest_id
WHERE a.repository_id = $1 AND a.attestation_digest = $2 AND m.deleted_at IS NULL
ORDER BY a.created_at, a.id;

-- name: ListOCIAttestationsPage :many
-- Keyset page over the live attestations of a repository, for re-verification.
SELECT a.id, a.repository_id, a.image, a.subject_digest, a.attestation_digest, a.manifest_id, a.predicate_type, a.kind,
       a.signer_identity, a.issuer, a.verified, a.verify_error, a.payload_blob, a.created_at, a.updated_at
FROM oci_attestations a
JOIN docker_manifests m ON m.id = a.manifest_id
WHERE a.repository_id = sqlc.arg(repository_id)::uuid AND a.id > sqlc.arg(after_id)::uuid AND m.deleted_at IS NULL
ORDER BY a.id
LIMIT sqlc.arg(page_size)::int;

-- name: UpdateOCIAttestationVerification :exec
UPDATE oci_attestations
SET signer_identity = $2, issuer = $3, verified = $4, verify_error = $5, updated_at = now()
WHERE id = $1;

-- name: HasVerifiedOCIAttestation :one
-- Whether a verified attestation of one of the predicate types exists for the
-- manifest digest in the repository (digests are content addresses, so the
-- image does not matter).
SELECT EXISTS (
    SELECT 1 FROM oci_attestations a
    JOIN docker_manifests m ON m.id = a.manifest_id
    WHERE a.repository_id = sqlc.arg(repository_id)::uuid
      AND a.subject_digest = sqlc.arg(subject_digest)::text AND a.kind = 'attestation' AND a.verified
      AND a.predicate_type = ANY(sqlc.arg(predicate_types)::text[]) AND m.deleted_at IS NULL
)::boolean AS present;

-- name: GetRepositoryAttestationTrust :one
SELECT config FROM repository_attestation_trust WHERE repository_id = $1;

-- name: UpsertRepositoryAttestationTrust :exec
INSERT INTO repository_attestation_trust (repository_id, config) VALUES ($1, $2)
ON CONFLICT (repository_id) DO UPDATE SET config = EXCLUDED.config, updated_at = now();

-- name: DeleteRepositoryAttestationTrust :execrows
DELETE FROM repository_attestation_trust WHERE repository_id = $1;
