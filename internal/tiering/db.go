// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tiering implements storage tiers and lifecycle policies: a registry
// of named tiers mapped to storage backends, a BlobStore that reads
// transparently from whichever tier holds a blob, and a mover that relocates
// blobs between tiers according to ordered policies without ever leaving a blob
// without exactly one authoritative placement. The protocol is documented in
// docs/operations/storage-tiering.md.
//
// Backend ids throughout this package use "" for the primary store (the one
// the server is configured with); every other tier maps to a storage_backends row.
package tiering

import (
	"context"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

// DB is the persistence the tiering package needs; *database.PostgresDB
// implements it.
type DB interface {
	ListStorageTiers(ctx context.Context) ([]*models.StorageTier, error)
	ListStorageBackends(ctx context.Context) ([]*models.StorageBackend, error)
	ListStoragePolicies(ctx context.Context) ([]*models.StoragePolicy, error)
	StorageTierUsage(ctx context.Context) ([]models.TierUsage, error)

	GetBlobPlacement(ctx context.Context, sha256 string) (backendID string, size int64, found bool, err error)
	// WithBlobLock runs fn under the per-blob advisory lock SafeDeleteBlob and
	// uploaders take.
	WithBlobLock(ctx context.Context, sha256 string, fn func(ctx context.Context) error) error
	// EnqueueBlobDeletion queues the copy on backendID ("" = primary store) of
	// sha256 for deferred deletion (see database.BlobDeletion). Callers hold the
	// per-blob lock.
	EnqueueBlobDeletion(ctx context.Context, sha256, backendID string) error
	FlipBlobPlacement(ctx context.Context, sha256, from, to string, verify func(ctx context.Context) error) (models.FlipOutcome, error)
	TouchBlobAccess(ctx context.Context, accessed map[string]time.Time, resolution time.Duration) (int64, error)

	BeginStorageTierMove(ctx context.Context, m *models.StorageTierMove) (*models.StorageTierMove, bool, error)
	ListRecoverableStorageTierMoves(ctx context.Context, maxAttempts int, retryAfter time.Duration, limit int) ([]*models.StorageTierMove, error)
	CountStorageTierMoves(ctx context.Context) (int64, error)
	FailStorageTierMove(ctx context.Context, sha256, message string) error
	FinishStorageTierMove(ctx context.Context, sha256 string) error

	ScanTierCandidates(ctx context.Context, after string, window int, clauses []models.TierScanClause) (found []models.BlobFacts, next string, scanned int, err error)
	ListPromotionCandidates(ctx context.Context, backendIDs []string, after string, limit int) ([]models.BlobFacts, error)
	ListBlobRefs(ctx context.Context, shas []string) (map[string][]models.BlobRef, error)
}
