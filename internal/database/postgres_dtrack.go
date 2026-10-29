// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// GetRepositoryAttestationAttach reports whether the repository attaches signed
// attestations to its docker images as OCI referrers; false when never set.
func (p *RepositoryStore) GetRepositoryAttestationAttach(ctx context.Context, repoID string) (bool, error) {
	enabled, err := p.q.GetRepositoryAttestationAttach(ctx, repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("get attestation attach setting: %w", err)
	}

	return enabled, nil
}

// SetRepositoryAttestationAttach stores the repository's attach switch.
func (p *RepositoryStore) SetRepositoryAttestationAttach(ctx context.Context, repoID string, enabled bool) error {
	if err := p.q.SetRepositoryAttestationAttach(
		ctx,
		dbsqlc.SetRepositoryAttestationAttachParams{RepositoryID: repoID, Enabled: enabled},
	); err != nil {
		return fmt.Errorf("set attestation attach setting: %w", err)
	}

	return nil
}
