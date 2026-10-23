// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"encoding/json"
	"time"
)

type BundleState string

const (
	BundleStateDraft    BundleState = "draft"
	BundleStateSealed   BundleState = "sealed"
	BundleStateArchived BundleState = "archived"
)

type PromotionStatus string

const (
	PromotionStatusInProgress PromotionStatus = "in_progress"
	PromotionStatusSuccess    PromotionStatus = "success"
	PromotionStatusFailed     PromotionStatus = "failed"
)

// PipelineStage is one element in a PromotionPipeline.Stages slice.
type PipelineStage struct {
	Name         string `json:"name"`
	RepositoryID string `json:"repo_id"`
}

type PromotionPipeline struct {
	ID          string          `db:"id"`
	Name        string          `db:"name"`
	Description string          `db:"description"`
	Stages      []PipelineStage `db:"-"`
	StagesRaw   json.RawMessage `db:"stages"`
	CreatedBy   string          `db:"created_by"`
	CreatedAt   time.Time       `db:"created_at"`
	UpdatedAt   time.Time       `db:"updated_at"`
}

type ReleaseBundle struct {
	ID           string      `db:"id"`
	Name         string      `db:"name"`
	Version      string      `db:"version"`
	Description  string      `db:"description"`
	State        BundleState `db:"state"`
	PipelineID   *string     `db:"pipeline_id"`
	CurrentStage *string     `db:"current_stage"`
	CreatedBy    string      `db:"created_by"`
	SealedAt     *time.Time  `db:"sealed_at"`
	SealedBy     *string     `db:"sealed_by"`
	ArchivedAt   *time.Time  `db:"archived_at"`
	ArchivedBy   *string     `db:"archived_by"`
	GPGKeyID     *string     `db:"gpg_key_id"`
	GPGSignature *string     `db:"gpg_signature"`
	CreatedAt    time.Time   `db:"created_at"`
	UpdatedAt    time.Time   `db:"updated_at"`
}

// IsMutable returns true only for draft bundles.
func (b *ReleaseBundle) IsMutable() bool { return b.State == BundleStateDraft }

type ReleaseBundleItem struct {
	ID           string    `db:"id"`
	BundleID     string    `db:"bundle_id"`
	SourceRepoID string    `db:"source_repo_id"`
	ComponentID  *string   `db:"component_id"`
	SHA256       string    `db:"sha256"`
	ArtifactPath string    `db:"artifact_path"`
	Format       string    `db:"format"`
	SizeBytes    int64     `db:"size_bytes"`
	AddedBy      string    `db:"added_by"`
	AddedAt      time.Time `db:"added_at"`
}

type BundlePromotion struct {
	ID           string          `db:"id"`
	BundleID     string          `db:"bundle_id"`
	FromStage    *string         `db:"from_stage"`
	ToStage      string          `db:"to_stage"`
	TargetRepoID string          `db:"target_repo_id"`
	Status       PromotionStatus `db:"status"`
	PromotedBy   string          `db:"promoted_by"`
	Notes        string          `db:"notes"`
	ErrorMessage *string         `db:"error_message"`
	ItemCount    int             `db:"item_count"`
	StartedAt    time.Time       `db:"started_at"`
	CompletedAt  *time.Time      `db:"completed_at"`
}

// BundleSignature is the GPG signature a bundle is sealed with. ItemIDs (the
// ids of the items the signed manifest was built from, sorted) pin the item
// set: sealing fails when it changed meanwhile.
type BundleSignature struct {
	KeyFingerprint string
	Signature      string
	ItemIDs        []string
}
