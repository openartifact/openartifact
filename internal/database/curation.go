// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/openartifact/openartifact/internal/curation"
	"github.com/openartifact/openartifact/internal/database/dbsqlc"
)

// ErrCurationRuleLimit is returned by CreateCurationRule when the repository
// already holds curation.MaxRules rules.
var ErrCurationRuleLimit = errors.New("repository has the maximum number of curation rules")

// ErrCurationRuleNotFound is returned when a rule does not exist in the
// repository.
var ErrCurationRuleNotFound = errors.New("curation rule not found")

// CurationStore owns the curation rules of proxy repositories.
//
// Rules are read on the request path of every proxy repository that has
// curation on, so ListCurationRules goes through the auth cache: a write made
// through this PostgresDB invalidates it (never stale for a request that starts
// after the write on this instance); a write by another replica is seen after at
// most the cache TTL (DB_AUTH_CACHE_TTL_MS, default 2s).
type CurationStore struct{ *core }

func curationRuleFromRow(row dbsqlc.ListCurationRulesRow) curation.Rule {
	return curation.Rule{
		ID:           row.ID,
		RepositoryID: row.RepositoryID,
		Action:       row.Action,
		Package:      row.PackagePattern,
		Version:      row.VersionPattern,
		Reason:       row.Reason,
		CreatedBy:    row.CreatedBy,
		CreatedAt:    row.CreatedAt,
	}
}

// ListCurationRules returns the rules of a repository in creation order. The
// returned slice is the caller's.
func (p *CurationStore) ListCurationRules(ctx context.Context, repoID string) ([]curation.Rule, error) {
	ptrs, err := cachedList(ctx, &p.authCache, &p.authCache.curation, repoID, cloneShallowSlice, p.listCurationRulesUncached)
	if err != nil {
		return nil, err
	}

	out := make([]curation.Rule, len(ptrs))
	for i, r := range ptrs {
		out[i] = *r
	}

	return out, nil
}

func (p *CurationStore) listCurationRulesUncached(ctx context.Context, repoID string) ([]*curation.Rule, error) {
	rows, err := p.q.ListCurationRules(ctx, repoID)
	if err != nil {
		return nil, fmt.Errorf("failed to list curation rules: %w", err)
	}

	out := make([]*curation.Rule, len(rows))

	for i, row := range rows {
		r := curationRuleFromRow(row)
		out[i] = &r
	}

	return out, nil
}

// GetCurationRule returns one rule of a repository, ErrCurationRuleNotFound when
// the repository has no such rule.
func (p *CurationStore) GetCurationRule(ctx context.Context, repoID, id string) (*curation.Rule, error) {
	row, err := p.q.GetCurationRule(ctx, dbsqlc.GetCurationRuleParams{RepositoryID: repoID, ID: id})
	if err != nil {
		return nil, mapCurationRuleErr("get", err)
	}

	r := curationRuleFromRow(dbsqlc.ListCurationRulesRow(row))

	return &r, nil
}

// CreateCurationRule stores rule (Action, Package, Version, Reason, CreatedBy;
// the repository is rule.RepositoryID) and returns it with its id and time. It
// fails with ErrCurationRuleLimit past curation.MaxRules rules.
func (p *CurationStore) CreateCurationRule(ctx context.Context, rule *curation.Rule) (*curation.Rule, error) {
	defer p.authCache.invalidate()

	n, err := p.q.CountCurationRules(ctx, rule.RepositoryID)
	if err != nil {
		return nil, fmt.Errorf("failed to count curation rules: %w", err)
	}

	if n >= curation.MaxRules {
		return nil, ErrCurationRuleLimit
	}

	row, err := p.q.CreateCurationRule(ctx, dbsqlc.CreateCurationRuleParams{
		RepositoryID:   rule.RepositoryID,
		Action:         rule.Action,
		PackagePattern: rule.Package,
		VersionPattern: rule.Version,
		Reason:         rule.Reason,
		CreatedBy:      rule.CreatedBy,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create curation rule: %w", err)
	}

	out := curationRuleFromRow(dbsqlc.ListCurationRulesRow(row))

	return &out, nil
}

// UpdateCurationRule replaces the action, patterns and reason of the rule
// rule.ID of rule.RepositoryID; ErrCurationRuleNotFound when it does not exist.
func (p *CurationStore) UpdateCurationRule(ctx context.Context, rule *curation.Rule) (*curation.Rule, error) {
	defer p.authCache.invalidate()

	row, err := p.q.UpdateCurationRule(ctx, dbsqlc.UpdateCurationRuleParams{
		RepositoryID:   rule.RepositoryID,
		ID:             rule.ID,
		Action:         rule.Action,
		PackagePattern: rule.Package,
		VersionPattern: rule.Version,
		Reason:         rule.Reason,
	})
	if err != nil {
		return nil, mapCurationRuleErr("update", err)
	}

	out := curationRuleFromRow(dbsqlc.ListCurationRulesRow(row))

	return &out, nil
}

// DeleteCurationRule removes a rule; ErrCurationRuleNotFound when it does not
// exist in the repository.
func (p *CurationStore) DeleteCurationRule(ctx context.Context, repoID, id string) error {
	defer p.authCache.invalidate()

	n, err := p.q.DeleteCurationRule(ctx, dbsqlc.DeleteCurationRuleParams{RepositoryID: repoID, ID: id})
	if err != nil {
		return fmt.Errorf("failed to delete curation rule: %w", err)
	}

	if n == 0 {
		return ErrCurationRuleNotFound
	}

	return nil
}

func mapCurationRuleErr(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCurationRuleNotFound
	}

	return fmt.Errorf("failed to %s curation rule: %w", op, err)
}
