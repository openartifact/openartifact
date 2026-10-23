// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

var (
	// ErrGPGKeyNotFound is returned when a GPG key does not exist.
	ErrGPGKeyNotFound = errors.New("GPG key not found")
	// ErrGPGKeyExists is returned when a key with the same name or fingerprint is already stored.
	ErrGPGKeyExists = errors.New("GPG key already exists")
)

// gpgKeysAdvisoryLockKey serialises every change of the "which key is globally
// active / is there any key" state across replicas.
const gpgKeysAdvisoryLockKey = "openartifact:gpg-keys"

func gpgKeyFromRow(r dbsqlc.GpgKey) *models.GPGKey {
	return &models.GPGKey{
		ID:             r.ID,
		Name:           r.Name,
		Fingerprint:    r.Fingerprint,
		KeyID:          r.KeyID,
		Algorithm:      r.Algorithm,
		UserID:         r.UserID,
		PublicKey:      r.PublicKey,
		PrivateKeyEnc:  r.PrivateKeyEnc,
		IsGlobalActive: r.IsGlobalActive,
		Source:         r.Source,
		CreatedBy:      r.CreatedBy,
		CreatedAt:      r.CreatedAt,
		ExpiresAt:      r.ExpiresAt,
	}
}

func insertGPGKeyTx(ctx context.Context, tx *sql.Tx, k *models.GPGKey) error {
	err := tx.QueryRowContext(ctx, `
		INSERT INTO gpg_keys (name, fingerprint, key_id, algorithm, user_id, public_key, private_key_enc,
		                      is_global_active, source, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, false, $8, $9, $10)
		RETURNING id, created_at`,
		k.Name, k.Fingerprint, k.KeyID, k.Algorithm, k.UserID, k.PublicKey, k.PrivateKeyEnc,
		k.Source, k.CreatedBy, k.ExpiresAt,
	).Scan(&k.ID, &k.CreatedAt)
	if err != nil {
		if IsUniqueViolation(err) {
			return ErrGPGKeyExists
		}

		return fmt.Errorf("failed to insert GPG key: %w", err)
	}

	return nil
}

func lockGPGKeys(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, gpgKeysAdvisoryLockKey); err != nil {
		return fmt.Errorf("GPG keys advisory lock: %w", err)
	}

	return nil
}

// CreateGPGKey stores k (inactive) and fills ID and CreatedAt back.
// ErrGPGKeyExists when the name or fingerprint is taken.
func (p *PostgresDB) CreateGPGKey(ctx context.Context, k *models.GPGKey) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin GPG key tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := insertGPGKeyTx(ctx, tx, k); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit GPG key tx: %w", err)
	}

	return nil
}

// CreateGPGKeyIfNone stores k as the global active key only when no key exists
// yet, under a cluster-wide advisory lock so replicas starting together create
// exactly one. It reports whether k was stored.
func (p *PostgresDB) CreateGPGKeyIfNone(ctx context.Context, k *models.GPGKey) (bool, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin GPG auto-generate tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockGPGKeys(ctx, tx); err != nil {
		return false, err
	}

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM gpg_keys`).Scan(&n); err != nil {
		return false, fmt.Errorf("count GPG keys: %w", err)
	}

	if n > 0 {
		return false, nil
	}

	if err := insertGPGKeyTx(ctx, tx, k); err != nil {
		return false, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE gpg_keys SET is_global_active = true WHERE id = $1`, k.ID); err != nil {
		return false, fmt.Errorf("activate auto-generated GPG key: %w", err)
	}

	k.IsGlobalActive = true

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit GPG auto-generate tx: %w", err)
	}

	return true, nil
}

// ListGPGKeys lists every key without its private part.
func (p *PostgresDB) ListGPGKeys(ctx context.Context) ([]*models.GPGKey, error) {
	rows, err := p.q.ListGPGKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list GPG keys: %w", err)
	}

	out := make([]*models.GPGKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, gpgKeyFromRow(dbsqlc.GpgKey(r)))
	}

	return out, nil
}

// GetGPGKey loads one key including its sealed private part; ErrGPGKeyNotFound when absent.
func (p *PostgresDB) GetGPGKey(ctx context.Context, id string) (*models.GPGKey, error) {
	row, err := p.q.GetGPGKey(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrGPGKeyNotFound
		}

		return nil, fmt.Errorf("failed to get GPG key: %w", err)
	}

	return gpgKeyFromRow(row), nil
}

// GetGPGKeyByFingerprint loads a key by its (upper-case hex) fingerprint; ErrGPGKeyNotFound when absent.
func (p *PostgresDB) GetGPGKeyByFingerprint(ctx context.Context, fingerprint string) (*models.GPGKey, error) {
	row, err := p.q.GetGPGKeyByFingerprint(ctx, fingerprint)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrGPGKeyNotFound
		}

		return nil, fmt.Errorf("failed to get GPG key by fingerprint: %w", err)
	}

	return gpgKeyFromRow(row), nil
}

// DeleteGPGKey removes a key and its repository assignments; ErrGPGKeyNotFound when absent.
func (p *PostgresDB) DeleteGPGKey(ctx context.Context, id string) error {
	n, err := p.q.DeleteGPGKey(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to delete GPG key: %w", err)
	}

	if n == 0 {
		return ErrGPGKeyNotFound
	}

	return nil
}

// SetGlobalGPGKey makes id the one globally active key; ErrGPGKeyNotFound when absent.
func (p *PostgresDB) SetGlobalGPGKey(ctx context.Context, id string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin GPG activate tx: %w", err)
	}

	defer func() { _ = tx.Rollback() }()

	if err := lockGPGKeys(ctx, tx); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE gpg_keys SET is_global_active = false WHERE is_global_active AND id <> $1`, id); err != nil {
		return fmt.Errorf("deactivate previous GPG key: %w", err)
	}

	res, err := tx.ExecContext(ctx, `UPDATE gpg_keys SET is_global_active = true WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("activate GPG key: %w", err)
	}

	if n, _ := res.RowsAffected(); n == 0 {
		return ErrGPGKeyNotFound
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit GPG activate tx: %w", err)
	}

	return nil
}

// ClearGlobalGPGKey leaves no key globally active.
func (p *PostgresDB) ClearGlobalGPGKey(ctx context.Context) error {
	if err := p.q.ClearGlobalGPGKey(ctx); err != nil {
		return fmt.Errorf("failed to clear global GPG key: %w", err)
	}

	return nil
}

// SetRepositoryGPGKey makes keyID the signing key of one repository.
func (p *PostgresDB) SetRepositoryGPGKey(ctx context.Context, repoID, keyID, assignedBy string) error {
	err := p.q.SetRepositoryGPGKey(ctx, dbsqlc.SetRepositoryGPGKeyParams{RepositoryID: repoID, GpgKeyID: keyID, AssignedBy: assignedBy})
	if err != nil {
		return fmt.Errorf("failed to assign GPG key: %w", err)
	}

	return nil
}

// ClearRepositoryGPGKey removes a repository's own key (it then follows the
// global one) and reports whether there was one.
func (p *PostgresDB) ClearRepositoryGPGKey(ctx context.Context, repoID string) (bool, error) {
	n, err := p.q.ClearRepositoryGPGKey(ctx, repoID)
	if err != nil {
		return false, fmt.Errorf("failed to clear repository GPG key: %w", err)
	}

	return n > 0, nil
}

// ListGPGKeyAssignments lists every repository-to-key binding.
func (p *PostgresDB) ListGPGKeyAssignments(ctx context.Context) ([]models.GPGKeyAssignment, error) {
	rows, err := p.q.ListGPGKeyAssignments(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list GPG key assignments: %w", err)
	}

	out := make([]models.GPGKeyAssignment, 0, len(rows))
	for _, r := range rows {
		out = append(out, models.GPGKeyAssignment{
			KeyID: r.GpgKeyID, RepositoryID: r.RepositoryID, RepositoryName: r.RepositoryName, RepositoryFormat: r.RepositoryFormat,
		})
	}

	return out, nil
}

// ListRepositoriesForGPGKey returns the repositories bound to keyID.
func (p *PostgresDB) ListRepositoriesForGPGKey(ctx context.Context, keyID string) ([]models.GPGSigningRepo, error) {
	rows, err := p.q.ListRepositoriesForGPGKey(ctx, keyID)
	if err != nil {
		return nil, fmt.Errorf("failed to list repositories for GPG key: %w", err)
	}

	out := make([]models.GPGSigningRepo, 0, len(rows))
	for _, r := range rows {
		out = append(out, models.GPGSigningRepo{ID: r.ID, Format: r.Format})
	}

	return out, nil
}

// ListSigningRepositoriesWithoutKey returns the hosted deb/rpm repositories
// that have no key of their own, i.e. follow the global key.
func (p *PostgresDB) ListSigningRepositoriesWithoutKey(ctx context.Context) ([]models.GPGSigningRepo, error) {
	rows, err := p.q.ListSigningRepositoriesWithoutKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list repositories following the global GPG key: %w", err)
	}

	out := make([]models.GPGSigningRepo, 0, len(rows))
	for _, r := range rows {
		out = append(out, models.GPGSigningRepo{ID: r.ID, Format: r.Format})
	}

	return out, nil
}

// ResolveGPGKeyForRepository returns the key that signs a repository's
// metadata (its own, else the global active one) with its sealed private part,
// or nil when nothing signs it.
func (p *PostgresDB) ResolveGPGKeyForRepository(ctx context.Context, repoID string) (*models.GPGKey, error) {
	row, err := p.q.ResolveGPGKeyForRepository(ctx, repoID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil //nolint:nilnil // nil key means "unsigned"
		}

		return nil, fmt.Errorf("failed to resolve GPG key: %w", err)
	}

	return gpgKeyFromRow(row), nil
}
