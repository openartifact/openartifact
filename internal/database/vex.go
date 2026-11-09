// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openartifact/openartifact/internal/database/dbsqlc"
	"github.com/openartifact/openartifact/internal/database/models"
)

// VEXStore owns VEX statements and the VEX suggestions of federated peers.
//
// vex_statements holds one decision per (repository, artifact, vulnerability)
// whatever its source: an operator decision (local) replaces anything, a vendor
// feed (csaf) or an automatically applied peer decision (peer) never replaces a
// statement of another source.
type VEXStore struct{ *core }

// ErrVEXStatementNotFound is returned when a VEX statement does not exist in the repository.
var ErrVEXStatementNotFound = errors.New("VEX statement not found")

// ErrVEXSuggestionNotFound is returned when a VEX suggestion does not exist in the repository.
var ErrVEXSuggestionNotFound = errors.New("VEX suggestion not found")

func vexFromRow(r dbsqlc.VexStatement) *models.VEXStatement {
	return &models.VEXStatement{
		ID:              r.ID,
		RepositoryID:    r.RepositoryID,
		ArtifactID:      r.ArtifactID,
		VulnID:          r.VulnID,
		Status:          r.Status,
		Justification:   r.Justification,
		ImpactStatement: r.ImpactStatement,
		ActionStatement: r.ActionStatement,
		CreatedBy:       r.CreatedBy,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
		SourceKind:      r.SourceKind,
		SourceName:      r.SourceName,
		SourceRef:       r.SourceRef,
		FeedID:          derefStr(r.FeedID),
		PeerID:          derefStr(r.PeerID),
	}
}

// UpsertVEXStatement records (or replaces, whatever its source) the decision
// for (RepositoryID, ArtifactID, VulnID) with the source of s (an empty
// SourceKind is local) and fills the stored row back into s.
func (p *VEXStore) UpsertVEXStatement(ctx context.Context, s *models.VEXStatement) error {
	return upsertVEXStatement(ctx, p.q, s)
}

func upsertVEXStatement(ctx context.Context, q *dbsqlc.Queries, s *models.VEXStatement) error {
	row, err := q.UpsertVEXStatement(ctx, vexStatementParams(s))

	return storeRow(s, row, err, "upsert VEX statement", vexFromRow)
}

func vexStatementParams(s *models.VEXStatement) dbsqlc.UpsertVEXStatementParams {
	kind := s.SourceKind
	if kind == "" {
		kind = models.VEXSourceLocal
	}

	return dbsqlc.UpsertVEXStatementParams{
		RepositoryID:    s.RepositoryID,
		ArtifactID:      s.ArtifactID,
		VulnID:          s.VulnID,
		Status:          s.Status,
		Justification:   s.Justification,
		ImpactStatement: s.ImpactStatement,
		ActionStatement: s.ActionStatement,
		CreatedBy:       s.CreatedBy,
		SourceKind:      kind,
		SourceName:      s.SourceName,
		SourceRef:       s.SourceRef,
		FeedID:          strPtr(s.FeedID),
		PeerID:          strPtr(s.PeerID),
	}
}

// ListVEXStatements lists the decisions of a repository, optionally only those
// of one artifact (artifactID != "").
func (p *VEXStore) ListVEXStatements(ctx context.Context, repoID, artifactID string) ([]*models.VEXStatement, error) {
	rows, err := p.q.ListVEXStatements(ctx, dbsqlc.ListVEXStatementsParams{RepositoryID: repoID, ArtifactID: artifactID})

	return mapRows(rows, err, "list VEX statements", vexFromRow)
}

// DeleteVEXStatement deletes a decision scoped to repoID; ErrVEXStatementNotFound when absent.
func (p *VEXStore) DeleteVEXStatement(ctx context.Context, repoID, id string) error {
	n, err := p.q.DeleteVEXStatement(ctx, dbsqlc.DeleteVEXStatementParams{RepositoryID: repoID, ID: id})
	if err != nil {
		return fmt.Errorf("failed to delete VEX statement: %w", err)
	}

	if n == 0 {
		return ErrVEXStatementNotFound
	}

	return nil
}

// ListVEXTargets returns the latest completed or partial vulnerability scan
// with findings of every artifact of repoID ("" = every repository), with the
// repository format.
func (p *VEXStore) ListVEXTargets(ctx context.Context, repoID string) ([]models.VEXTarget, error) {
	rows, err := p.q.ListVEXTargets(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list VEX targets: %w", err)
	}

	out := make([]models.VEXTarget, 0, len(rows))

	for _, r := range rows {
		t := models.VEXTarget{RepositoryID: r.RepositoryID, RepositoryFormat: r.RepositoryFormat, ArtifactID: r.ArtifactID}
		if err := json.Unmarshal(r.Vulnerabilities, &t.Vulns); err != nil {
			return nil, fmt.Errorf("decode vulnerabilities of %s: %w", r.ArtifactID, err)
		}

		out = append(out, t)
	}

	return out, nil
}

// ReplaceFeedVEXStatements replaces the statements materialized from a feed
// with stmts (stored as csaf statements of feedID) in one transaction. A target
// that has a statement of another source keeps it. It returns how many were
// inserted.
func (p *VEXStore) ReplaceFeedVEXStatements(ctx context.Context, feedID string, stmts []*models.VEXStatement) (int, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin feed VEX replace: %w", err)
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	q := p.q.WithTx(tx)

	if err := q.DeleteFeedVEXStatements(ctx, feedID); err != nil {
		return 0, fmt.Errorf("delete feed VEX statements: %w", err)
	}

	n, err := q.InsertFeedVEXStatements(ctx, feedVEXParams(feedID, stmts))
	if err != nil {
		return 0, fmt.Errorf("insert feed VEX statements: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit feed VEX replace: %w", err)
	}

	return int(n), nil
}

func feedVEXParams(feedID string, stmts []*models.VEXStatement) dbsqlc.InsertFeedVEXStatementsParams {
	n := len(stmts)
	a := dbsqlc.InsertFeedVEXStatementsParams{
		FeedID:           feedID,
		RepositoryIds:    make([]string, n),
		ArtifactIds:      make([]string, n),
		VulnIds:          make([]string, n),
		Statuses:         make([]string, n),
		Justifications:   make([]string, n),
		ImpactStatements: make([]string, n),
		ActionStatements: make([]string, n),
		SourceNames:      make([]string, n),
		SourceRefs:       make([]string, n),
	}

	for i, s := range stmts {
		a.RepositoryIds[i], a.ArtifactIds[i], a.VulnIds[i] = s.RepositoryID, s.ArtifactID, s.VulnID
		a.Statuses[i], a.Justifications[i] = s.Status, s.Justification
		a.ImpactStatements[i], a.ActionStatements[i] = s.ImpactStatement, s.ActionStatement
		a.SourceNames[i], a.SourceRefs[i] = s.SourceName, s.SourceRef
	}

	return a
}

// ApplyPeerVEXStatement stores the decision of the federated peer s.PeerID as
// a peer statement: inserted, or updated when the target's statement is
// already that peer's. It reports whether it wrote; a statement of any other
// source is left alone.
func (p *VEXStore) ApplyPeerVEXStatement(ctx context.Context, s *models.VEXStatement) (bool, error) {
	n, err := p.q.ApplyPeerVEXStatement(ctx, dbsqlc.ApplyPeerVEXStatementParams{
		RepositoryID:    s.RepositoryID,
		ArtifactID:      s.ArtifactID,
		VulnID:          s.VulnID,
		Status:          s.Status,
		Justification:   s.Justification,
		ImpactStatement: s.ImpactStatement,
		ActionStatement: s.ActionStatement,
		CreatedBy:       s.CreatedBy,
		SourceName:      s.SourceName,
		SourceRef:       s.SourceRef,
		PeerID:          s.PeerID,
	})
	if err != nil {
		return false, fmt.Errorf("failed to apply peer VEX statement: %w", err)
	}

	return n > 0, nil
}

// UpsertVEXSuggestion stores a peer's decision, keyed by (PeerID, ArtifactID,
// VulnID), and fills s.ID and s.State. It reports changed when the suggestion
// is new or its content (status, justification, statements) changed; a changed
// suggestion goes back to pending and loses its decision.
func (p *VEXStore) UpsertVEXSuggestion(ctx context.Context, s *models.VEXSuggestion) (bool, error) {
	row, err := p.q.UpsertVEXSuggestion(ctx, dbsqlc.UpsertVEXSuggestionParams{
		RepositoryID:    s.RepositoryID,
		PeerID:          s.PeerID,
		ArtifactID:      s.ArtifactID,
		VulnID:          s.VulnID,
		Status:          s.Status,
		Justification:   s.Justification,
		ImpactStatement: s.ImpactStatement,
		ActionStatement: s.ActionStatement,
		PeerUpdatedAt:   s.PeerUpdatedAt,
	})
	if err == nil {
		s.ID, s.State = row.ID, row.State

		return true, nil
	}

	if !isNoRows(err) {
		return false, fmt.Errorf("failed to upsert VEX suggestion: %w", err)
	}

	// Unchanged: the conflict update matched no row.
	key, err := p.q.GetVEXSuggestionKey(ctx, dbsqlc.GetVEXSuggestionKeyParams{
		PeerID: s.PeerID, ArtifactID: s.ArtifactID, VulnID: s.VulnID,
	})
	if err != nil {
		return false, fmt.Errorf("failed to read VEX suggestion: %w", err)
	}

	s.ID, s.State = key.ID, key.State

	return false, nil
}

// PruneVEXSuggestions deletes the suggestions of a peer but keepIDs and
// returns how many it deleted.
func (p *VEXStore) PruneVEXSuggestions(ctx context.Context, peerID string, keepIDs []string) (int64, error) {
	if keepIDs == nil {
		keepIDs = []string{}
	}

	n, err := p.q.PruneVEXSuggestions(ctx, dbsqlc.PruneVEXSuggestionsParams{PeerID: peerID, KeepIds: keepIDs})
	if err != nil {
		return 0, fmt.Errorf("failed to prune VEX suggestions: %w", err)
	}

	return n, nil
}

// DeleteVEXSuggestionsOfPeer deletes every suggestion of a peer.
func (p *VEXStore) DeleteVEXSuggestionsOfPeer(ctx context.Context, peerID string) (int64, error) {
	n, err := p.q.DeleteVEXSuggestionsOfPeer(ctx, peerID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete VEX suggestions: %w", err)
	}

	return n, nil
}

func vexSuggestionFromRow(s dbsqlc.VexSuggestion, peerName, peerBaseURL, localStatus, localSource string) *models.VEXSuggestion {
	return &models.VEXSuggestion{
		ID:              s.ID,
		RepositoryID:    s.RepositoryID,
		PeerID:          s.PeerID,
		PeerName:        peerName,
		PeerBaseURL:     peerBaseURL,
		ArtifactID:      s.ArtifactID,
		VulnID:          s.VulnID,
		Status:          s.Status,
		Justification:   s.Justification,
		ImpactStatement: s.ImpactStatement,
		ActionStatement: s.ActionStatement,
		PeerUpdatedAt:   s.PeerUpdatedAt,
		State:           s.State,
		DecidedBy:       s.DecidedBy,
		DecidedAt:       s.DecidedAt,
		CreatedAt:       s.CreatedAt,
		UpdatedAt:       s.UpdatedAt,
		LocalStatus:     localStatus,
		LocalSourceKind: localSource,
	}
}

// ListVEXSuggestions lists the suggestions of a repository in one state ("" =
// all), with the peer and the statement the target has now.
func (p *VEXStore) ListVEXSuggestions(ctx context.Context, repoID, state string) ([]*models.VEXSuggestion, error) {
	rows, err := p.q.ListVEXSuggestions(ctx, dbsqlc.ListVEXSuggestionsParams{RepositoryID: repoID, State: state})

	return mapRows(rows, err, "list VEX suggestions", func(r dbsqlc.ListVEXSuggestionsRow) *models.VEXSuggestion {
		return vexSuggestionFromRow(r.VexSuggestion, r.PeerName, r.PeerBaseUrl, r.LocalStatus, r.LocalSourceKind)
	})
}

// GetVEXSuggestion reads one suggestion of a repository; ErrVEXSuggestionNotFound when absent.
func (p *VEXStore) GetVEXSuggestion(ctx context.Context, repoID, id string) (*models.VEXSuggestion, error) {
	return getVEXSuggestion(ctx, p.q, repoID, id)
}

func getVEXSuggestion(ctx context.Context, q *dbsqlc.Queries, repoID, id string) (*models.VEXSuggestion, error) {
	if !isUUID(id) {
		return nil, ErrVEXSuggestionNotFound
	}

	r, err := q.GetVEXSuggestion(ctx, dbsqlc.GetVEXSuggestionParams{RepositoryID: repoID, ID: id})
	if isNoRows(err) {
		return nil, ErrVEXSuggestionNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get VEX suggestion: %w", err)
	}

	return vexSuggestionFromRow(r.VexSuggestion, r.PeerName, r.PeerBaseUrl, r.LocalStatus, r.LocalSourceKind), nil
}

// SetVEXSuggestionState moves a suggestion of a repository to state, decided
// by decidedBy now (pending clears the decision); ErrVEXSuggestionNotFound when absent.
func (p *VEXStore) SetVEXSuggestionState(ctx context.Context, repoID, id, state, decidedBy string) error {
	return setVEXSuggestionState(ctx, p.q, repoID, id, state, decidedBy)
}

func setVEXSuggestionState(ctx context.Context, q *dbsqlc.Queries, repoID, id, state, decidedBy string) error {
	if !isUUID(id) {
		return ErrVEXSuggestionNotFound
	}

	n, err := q.SetVEXSuggestionState(ctx, dbsqlc.SetVEXSuggestionStateParams{
		RepositoryID: repoID, ID: id, State: state, DecidedBy: decidedBy,
	})
	if err != nil {
		return fmt.Errorf("failed to set VEX suggestion state: %w", err)
	}

	if n == 0 {
		return ErrVEXSuggestionNotFound
	}

	return nil
}

// AcceptVEXSuggestion records a suggestion of a repository as the target's
// statement (source peer, created by decidedBy), replacing whatever statement
// was there, and marks the suggestion accepted, in one transaction. It returns
// the stored statement; ErrVEXSuggestionNotFound when the suggestion is absent.
func (p *VEXStore) AcceptVEXSuggestion(ctx context.Context, repoID, id, decidedBy string) (*models.VEXStatement, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin VEX suggestion accept: %w", err)
	}

	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	q := p.q.WithTx(tx)

	sug, err := getVEXSuggestion(ctx, q, repoID, id)
	if err != nil {
		return nil, err
	}

	stmt := &models.VEXStatement{
		RepositoryID:    sug.RepositoryID,
		ArtifactID:      sug.ArtifactID,
		VulnID:          sug.VulnID,
		Status:          sug.Status,
		Justification:   sug.Justification,
		ImpactStatement: sug.ImpactStatement,
		ActionStatement: sug.ActionStatement,
		CreatedBy:       decidedBy,
		SourceKind:      models.VEXSourcePeer,
		SourceName:      sug.PeerName,
		SourceRef:       sug.PeerBaseURL,
		PeerID:          sug.PeerID,
	}

	if err := upsertVEXStatement(ctx, q, stmt); err != nil {
		return nil, err
	}

	if err := setVEXSuggestionState(ctx, q, repoID, id, models.VEXSuggestionAccepted, decidedBy); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit VEX suggestion accept: %w", err)
	}

	return stmt, nil
}
