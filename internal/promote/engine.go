// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package promote

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/internal/licensepolicy"
	"github.com/openartifact/openartifact/internal/quarantine"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Regenerator enqueues the refresh of generated metadata for a promotion's
// destination. api.NewWorkerRegenerator satisfies it.
type Regenerator interface {
	NotifyDebStale(repoID string)
	NotifyRPMStale(repoID string)
	NotifyMavenStale(repoID, groupID, artifactID string)
	NotifyNPMStale(packageID string)
}

// Engine promotes a sealed bundle into a repository.
type Engine struct {
	db        database.Database
	log       *logger.Logger
	regen     Regenerator
	promoters map[string]Promoter
}

// NewEngine returns an Engine with the promoter of every hosted format. regen
// may be nil (generated metadata is then only refreshed by the periodic sweep).
func NewEngine(db database.Database, log *logger.Logger, regen Regenerator) *Engine {
	e := &Engine{db: db, log: log, regen: regen, promoters: map[string]Promoter{}}
	for _, p := range Promoters() {
		e.promoters[p.Format()] = p
	}

	return e
}

// Params is one promotion request. The caller has already authorized it and
// validated the stage transition.
type Params struct {
	Bundle *models.ReleaseBundle
	Items  []*models.ReleaseBundleItem
	Target *models.Repository
	// ToStage is the pipeline stage the bundle moves to.
	ToStage string
	Notes   string
	// Promoter is the username recorded on the promotion.
	Promoter string
	// Force replaces conflicting content in the target (to its trash) instead
	// of failing; the caller has checked the principal may override immutability.
	Force bool
}

// ItemReport is what happened to one bundle item.
type ItemReport struct {
	SHA256       string     `json:"sha256"`
	ArtifactPath string     `json:"artifact_path"`
	Format       string     `json:"format"`
	Copied       []Artifact `json:"copied"`
	Existing     []Artifact `json:"existing"`
	// Replaced lists the target artifacts a forced promotion moved to the trash.
	Replaced []Artifact `json:"replaced"`
}

// Report is the outcome of a successful promotion.
type Report struct {
	Promotion *models.BundlePromotion
	Items     []ItemReport
	// Copied, Existing and Replaced count artifacts written, already present
	// and (forced promotions) replaced.
	Copied, Existing, Replaced int
}

// Promote copies every item of the bundle into the target repository and
// records the promotion, all in one transaction: on any error nothing is
// changed. Promoting the same bundle to the same stage again is rejected by
// the stage check, but a promotion retried after a partial client-side failure
// is idempotent because artifacts already present with the same content are
// counted as existing rather than conflicting.
func (e *Engine) Promote(ctx context.Context, p Params) (*Report, error) {
	if len(p.Items) == 0 {
		return nil, ErrEmpty
	}

	srcs, err := e.sources(ctx, p)
	if err != nil {
		return nil, err
	}

	started := time.Now()

	tx, err := e.db.BeginTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin promotion: %w", err)
	}

	defer func() { _ = tx.Rollback() }() // no-op once committed

	if err := lockBundle(ctx, tx, p.Bundle); err != nil {
		return nil, err
	}

	report, total, regen, err := e.copyItems(ctx, tx, p, srcs)
	if err != nil {
		return nil, err
	}

	if err := e.charge(ctx, p.Target.ID, total); err != nil {
		return nil, err
	}

	if err := recordPromotion(ctx, tx, p, report, started); err != nil {
		e.refund(ctx, p.Target.ID, total)

		return nil, err
	}

	if err := tx.Commit(); err != nil {
		e.refund(ctx, p.Target.ID, total)

		return nil, fmt.Errorf("commit promotion: %w", err)
	}

	e.notify(regen)

	return report, nil
}

// sources resolves and validates the repositories of the items and the target.
func (e *Engine) sources(ctx context.Context, p Params) (map[string]*models.Repository, error) {
	if p.Target.RepoType != models.ProjectRepoTypeHosted || p.Target.DeletedAt != nil {
		return nil, fmt.Errorf("%w: target %s", ErrNotHosted, p.Target.Name)
	}

	srcs := map[string]*models.Repository{}

	for _, it := range p.Items {
		if _, done := srcs[it.SourceRepoID]; done {
			continue
		}

		repo, err := e.db.GetRepositoryByID(ctx, it.SourceRepoID)
		if err != nil {
			return nil, fmt.Errorf("load source repository: %w", err)
		}

		if repo == nil || repo.DeletedAt != nil {
			return nil, &ItemError{SHA256: it.SHA256, Path: it.ArtifactPath, Err: ErrItemNotFound}
		}

		if repo.ID == p.Target.ID {
			return nil, &ItemError{SHA256: it.SHA256, Path: it.ArtifactPath, Err: ErrSameRepository}
		}

		if repo.RepoType != models.ProjectRepoTypeHosted {
			return nil, &ItemError{SHA256: it.SHA256, Path: it.ArtifactPath, Err: fmt.Errorf("%w: source %s", ErrNotHosted, repo.Name)}
		}

		if repo.Format != p.Target.Format || repo.Format != it.Format {
			return nil, &ItemError{SHA256: it.SHA256, Path: it.ArtifactPath, Err: fmt.Errorf("%w: %s is %s, target is %s", ErrFormatMismatch, repo.Name, repo.Format, p.Target.Format)}
		}

		srcs[it.SourceRepoID] = repo
	}

	return srcs, nil
}

// lockBundle serializes promotions of one bundle and re-validates, under the
// row lock, that it is still sealed and at the stage the request was
// validated against.
func lockBundle(ctx context.Context, tx Tx, b *models.ReleaseBundle) error {
	var (
		state string
		stage sql.NullString
	)

	err := tx.QueryRowContext(ctx,
		`SELECT state, current_stage FROM release_bundles WHERE id = $1 FOR UPDATE`, b.ID).Scan(&state, &stage)
	if err != nil {
		return fmt.Errorf("lock bundle: %w", err)
	}

	if models.BundleState(state) != models.BundleStateSealed {
		return ErrBundleState
	}

	if stage.Valid != (b.CurrentStage != nil) || (b.CurrentStage != nil && stage.String != *b.CurrentStage) {
		return ErrStageChanged
	}

	return nil
}

// copyItems runs the promoter of every item and applies the source quarantine
// and the target license policy to what it copied.
func (e *Engine) copyItems(ctx context.Context, tx Tx, p Params, srcs map[string]*models.Repository) (*Report, Usage, []Regen, error) {
	report := &Report{}

	var (
		total Usage
		regen []Regen
	)

	for _, it := range p.Items {
		promoter, ok := e.promoters[it.Format]
		if !ok {
			return nil, total, nil, &ItemError{SHA256: it.SHA256, Path: it.ArtifactPath, Err: ErrUnsupportedFormat}
		}

		src := srcs[it.SourceRepoID]

		res, err := promoter.Promote(ctx, tx, Request{Src: src, Dst: p.Target, SHA256: it.SHA256, Path: it.ArtifactPath, Force: p.Force, Actor: p.Promoter})
		if err != nil {
			return nil, total, nil, &ItemError{SHA256: it.SHA256, Path: it.ArtifactPath, Err: err}
		}

		if err := e.checkPolicies(ctx, src, p.Target, res); err != nil {
			return nil, total, nil, &ItemError{SHA256: it.SHA256, Path: it.ArtifactPath, Err: err}
		}

		total.Bytes += res.Usage.Bytes
		total.Artifacts += res.Usage.Artifacts
		regen = append(regen, res.Regen...)

		report.Copied += len(res.Copied)
		report.Existing += len(res.Existing)
		report.Replaced += len(res.Replaced)
		report.Items = append(report.Items, ItemReport{
			SHA256: it.SHA256, ArtifactPath: it.ArtifactPath, Format: it.Format,
			Copied: nonNil(res.Copied), Existing: nonNil(res.Existing), Replaced: nonNil(res.Replaced),
		})
	}

	return report, total, regen, nil
}

func nonNil(a []Artifact) []Artifact {
	if a == nil {
		return []Artifact{}
	}

	return a
}

// checkPolicies refuses content quarantined in the source repository, and
// content whose licenses a blocking policy of the target refuses.
func (e *Engine) checkPolicies(ctx context.Context, src, dst *models.Repository, res *Result) error {
	quarantined, err := quarantine.IsQuarantined(ctx, e.db, src.ID, quarantine.PathKeys(res.QuarantineKeys...)...)
	if err != nil {
		return fmt.Errorf("check quarantine: %w", err)
	}

	if quarantined {
		return ErrQuarantined
	}

	violations, err := licensepolicy.Evaluate(ctx, e.db, e.log, dst.ID, res.Licenses)
	if err != nil {
		return fmt.Errorf("evaluate license policies: %w", err)
	}

	if blocked := licensepolicy.Blocked(violations); len(blocked) > 0 {
		return &LicenseBlockedError{Licenses: res.Licenses, Violations: blocked}
	}

	return nil
}

// charge reserves the promotion's usage in the target's quota.
func (e *Engine) charge(ctx context.Context, repoID string, u Usage) error {
	if u.Bytes == 0 && u.Artifacts == 0 {
		return nil
	}

	if err := quota.EnforceN(ctx, e.db, repoID, u.Bytes, u.Artifacts); err != nil {
		if errors.Is(err, quota.ErrExceeded) {
			return err
		}

		return fmt.Errorf("reserve quota: %w", err)
	}

	return nil
}

// refund gives a reservation back after the transaction failed.
func (e *Engine) refund(ctx context.Context, repoID string, u Usage) {
	if u.Bytes == 0 && u.Artifacts == 0 {
		return
	}

	if err := quota.ReleaseN(context.WithoutCancel(ctx), e.db, repoID, u.Bytes, u.Artifacts); err != nil {
		e.log.Errorw("promote: release quota reservation", "repo_id", repoID, "error", err)
	}
}

// recordPromotion inserts the success row and advances the bundle's stage.
func recordPromotion(ctx context.Context, tx Tx, p Params, report *Report, started time.Time) error {
	pr := &models.BundlePromotion{
		BundleID:     p.Bundle.ID,
		FromStage:    p.Bundle.CurrentStage,
		ToStage:      p.ToStage,
		TargetRepoID: p.Target.ID,
		Status:       models.PromotionStatusSuccess,
		PromotedBy:   p.Promoter,
		ItemCount:    len(p.Items),
	}

	var notes *string
	if p.Notes != "" {
		notes = &p.Notes
	}

	err := tx.QueryRowContext(ctx,
		`INSERT INTO bundle_promotions
		   (bundle_id, from_stage, to_stage, target_repo_id, status, promoter_id, notes, item_count, started_at, completed_at)
		 VALUES ($1, $2, $3, $4, $5, (SELECT id FROM auth_users WHERE username = $6), $7, $8, $9, now())
		 RETURNING id, started_at, completed_at`,
		pr.BundleID, pr.FromStage, pr.ToStage, pr.TargetRepoID, pr.Status, pr.PromotedBy, notes, pr.ItemCount, started,
	).Scan(&pr.ID, &pr.StartedAt, &pr.CompletedAt)
	if err != nil {
		return fmt.Errorf("record promotion: %w", err)
	}

	pr.Notes = p.Notes

	if _, err := tx.ExecContext(ctx,
		`UPDATE release_bundles SET current_stage = $1, updated_at = now() WHERE id = $2`,
		p.ToStage, p.Bundle.ID); err != nil {
		return fmt.Errorf("advance bundle stage: %w", err)
	}

	report.Promotion = pr

	return nil
}

// notify enqueues the metadata refreshes a committed promotion needs.
func (e *Engine) notify(regen []Regen) {
	if e.regen == nil {
		return
	}

	for _, r := range regen {
		switch r.Kind {
		case RegenDeb:
			e.regen.NotifyDebStale(r.RepoID)
		case RegenRPM:
			e.regen.NotifyRPMStale(r.RepoID)
		case RegenMaven:
			e.regen.NotifyMavenStale(r.RepoID, r.GroupID, r.ArtifactID)
		case RegenNPM:
			e.regen.NotifyNPMStale(r.PackageID)
		}
	}
}
