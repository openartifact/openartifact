// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// AttestationStore owns the index of attestations attached to docker images
// (oci_attestations) and the per-repository trust configuration they are
// verified against.
type AttestationStore struct{ *core }

func attestationFromRow(r dbsqlc.OciAttestation) *models.OCIAttestation {
	return &models.OCIAttestation{
		ID:                r.ID,
		RepositoryID:      r.RepositoryID,
		Image:             r.Image,
		SubjectDigest:     r.SubjectDigest,
		AttestationDigest: r.AttestationDigest,
		ManifestID:        r.ManifestID,
		PredicateType:     r.PredicateType,
		Kind:              r.Kind,
		SignerIdentity:    r.SignerIdentity,
		Issuer:            r.Issuer,
		Verified:          r.Verified,
		VerifyError:       r.VerifyError,
		PayloadBlob:       r.PayloadBlob,
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
	}
}

// UpsertOCIAttestation records the attachment, or refreshes the row of the same
// (repository, image, subject, envelope): its verification outcome and the
// manifest carrying it. An existing row keeps its created_at, the time the
// envelope was first seen; a zero a.CreatedAt means now. The stored id and
// created_at are filled back into a.
func (p *AttestationStore) UpsertOCIAttestation(ctx context.Context, a *models.OCIAttestation) error {
	created := a.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}

	row, err := p.q.UpsertOCIAttestation(ctx, dbsqlc.UpsertOCIAttestationParams{
		RepositoryID:      a.RepositoryID,
		Image:             a.Image,
		SubjectDigest:     a.SubjectDigest,
		AttestationDigest: a.AttestationDigest,
		ManifestID:        a.ManifestID,
		PredicateType:     a.PredicateType,
		Kind:              a.Kind,
		SignerIdentity:    a.SignerIdentity,
		Issuer:            a.Issuer,
		Verified:          a.Verified,
		VerifyError:       a.VerifyError,
		PayloadBlob:       a.PayloadBlob,
		CreatedAt:         created,
	})
	if err != nil {
		return fmt.Errorf("failed to upsert attestation: %w", err)
	}

	a.ID, a.CreatedAt = row.ID, row.CreatedAt

	return nil
}

// GetOCIAttestationFirstSeen returns when the envelope was first indexed for the
// subject of the image; false when it never was.
func (p *AttestationStore) GetOCIAttestationFirstSeen(
	ctx context.Context, repoID, image, subject, attestationDigest string,
) (time.Time, bool, error) {
	t, err := p.q.GetOCIAttestationFirstSeen(ctx, dbsqlc.GetOCIAttestationFirstSeenParams{
		RepositoryID: repoID, Image: image, SubjectDigest: subject, AttestationDigest: attestationDigest,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}

	if err != nil {
		return time.Time{}, false, fmt.Errorf("failed to get attestation first seen time: %w", err)
	}

	return t, true, nil
}

// ListOCIAttestations lists the attachments of a subject digest carried by
// manifests that are not in the trash, of one image when image is not empty.
func (p *AttestationStore) ListOCIAttestations(ctx context.Context, repoID, subject, image string) ([]*models.OCIAttestation, error) {
	rows, err := p.q.ListOCIAttestations(ctx, dbsqlc.ListOCIAttestationsParams{
		RepositoryID: repoID, SubjectDigest: subject, Image: image,
	})

	return mapRows(rows, err, "list attestations", attestationFromRow)
}

// ListOCIAttestationsByDigest lists the rows of an envelope digest.
func (p *AttestationStore) ListOCIAttestationsByDigest(
	ctx context.Context, repoID, attestationDigest string,
) ([]*models.OCIAttestation, error) {
	rows, err := p.q.ListOCIAttestationsByDigest(ctx, dbsqlc.ListOCIAttestationsByDigestParams{
		RepositoryID: repoID, AttestationDigest: attestationDigest,
	})

	return mapRows(rows, err, "list attestations by digest", attestationFromRow)
}

// ListOCIAttestationsPage lists up to limit attachments of the repository whose
// id is greater than afterID, in id order.
func (p *AttestationStore) ListOCIAttestationsPage(
	ctx context.Context,
	repoID, afterID string,
	limit int,
) ([]*models.OCIAttestation, error) {
	rows, err := p.q.ListOCIAttestationsPage(ctx, dbsqlc.ListOCIAttestationsPageParams{
		RepositoryID: repoID, AfterID: afterID, PageSize: int32(limit), //nolint:gosec // a small page size
	})

	return mapRows(rows, err, "list attestations page", attestationFromRow)
}

// HasVerifiedOCIAttestation reports whether a verified attestation of one of the
// predicate types is indexed for the manifest digest.
func (p *AttestationStore) HasVerifiedOCIAttestation(ctx context.Context, repoID, subject string, predicateTypes []string) (bool, error) {
	ok, err := p.q.HasVerifiedOCIAttestation(ctx, dbsqlc.HasVerifiedOCIAttestationParams{
		RepositoryID: repoID, SubjectDigest: subject, PredicateTypes: predicateTypes,
	})
	if err != nil {
		return false, fmt.Errorf("failed to check verified attestation: %w", err)
	}

	return ok, nil
}

// GetRepositoryAttestationTrust returns the repository's trust configuration
// document, nil when none is set.
func (p *AttestationStore) GetRepositoryAttestationTrust(ctx context.Context, repoID string) (json.RawMessage, error) {
	cfg, err := p.q.GetRepositoryAttestationTrust(ctx, repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get attestation trust: %w", err)
	}

	return cfg, nil
}

// SetRepositoryAttestationTrust stores the repository's trust configuration document.
func (p *AttestationStore) SetRepositoryAttestationTrust(ctx context.Context, repoID string, config json.RawMessage) error {
	if err := p.q.UpsertRepositoryAttestationTrust(ctx, dbsqlc.UpsertRepositoryAttestationTrustParams{
		RepositoryID: repoID, Config: config,
	}); err != nil {
		return fmt.Errorf("failed to set attestation trust: %w", err)
	}

	return nil
}
