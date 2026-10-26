// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import "time"

// Token list kinds: a personal access token, a project service token, or a
// federation peer token of one repository.
const (
	TokenListPersonal   = "personal"
	TokenListProject    = "project"
	TokenListFederation = "federation"
)

// TokenListParams selects a page of the unified token list. Visibility is part
// of the query: it is never filtered after the fact, so Total stays exact.
type TokenListParams struct {
	// Types keeps only these kinds (empty: all three).
	Types []string
	// ExpiresBefore keeps only tokens that carry an expiry earlier than it
	// (already expired ones included; tokens without an expiry never match).
	ExpiresBefore *time.Time
	// Query is a case-insensitive substring of the token name or owner.
	Query string
	// Project keeps only the tokens of the project with this name: its project
	// tokens and the federation tokens of its repositories (empty: any).
	Project string
	// Admin lists every token. Otherwise the viewer's own personal tokens plus
	// the project and federation tokens of ProjectIDs are listed.
	Admin      bool
	ViewerID   string
	ProjectIDs []string
	Page       int
	Limit      int
}

// TokenListItem is one row of the unified token list. It never carries the
// token hash.
type TokenListItem struct {
	ID         string
	Type       string
	Name       string
	Owner      string
	Project    string
	Repository string
	Role       string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}
