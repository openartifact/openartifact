// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

// Credential names describe how a request was authenticated.
const (
	CredentialSession     = "session"
	CredentialBearerToken = "bearer_token"
	CredentialBasicToken  = "basic_token"
	CredentialLegacy      = "legacy"
	CredentialNone        = "none"
)

// ErrEmptyPattern is returned for an empty repository filter or path pattern.
// An empty regular expression matches everything, so it must never be stored.
var ErrEmptyPattern = errors.New("pattern must not be empty")

type Principal struct {
	UserID             *string
	Username           string
	GroupIDs           []string
	IsAnonymous        bool
	IsLegacy           bool
	MustChangePassword bool
	// Credential is how the request authenticated (Credential* constants).
	// Ambient credentials (session cookie) require CSRF protection; explicit
	// ones (token headers, legacy key) do not.
	Credential string
	// TokenKind and TokenScopes describe the credential when the request was
	// authenticated with a personal/deploy token. They are empty for session
	// and legacy-key authentication. TokenScopes holds permission-target IDs:
	// when non-empty the token may only use those targets of its owner.
	TokenKind   string
	TokenScopes []string
	// ServiceProjectID is set for a project token's service account: the only
	// project the principal belongs to. Such a principal ignores permission
	// targets and holds only the role of its own project.
	ServiceProjectID string
}

// IsToken reports whether the request was authenticated with an API token.
func (p Principal) IsToken() bool {
	return p.TokenKind != ""
}

type Evaluator struct {
	db                 database.Database
	regexCache         sync.Map
	allowAnonymousRead bool
	log                *logger.Logger
}

func NewEvaluator(db database.Database, log *logger.Logger, allowAnonymousRead bool) *Evaluator {
	return &Evaluator{
		db:                 db,
		allowAnonymousRead: allowAnonymousRead,
		log:                log,
	}
}

// CompilePattern compiles a repository filter or path pattern exactly the way
// the evaluator matches it: "*" is shorthand for ".*", an empty pattern is
// rejected, and the expression is anchored so it must match the whole value
// ("dev" no longer matches "devops"). The API validates with this function so
// what is accepted at write time is what is enforced at request time.
func CompilePattern(pattern string) (*regexp.Regexp, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, ErrEmptyPattern
	}

	if pattern == "*" {
		pattern = ".*"
	}

	return regexp.Compile(`^(?:` + pattern + `)$`)
}

func (e *Evaluator) Evaluate(ctx context.Context, principal Principal, repoName, artifactPath, action string) (bool, error) {
	if principal.IsLegacy {
		return true, nil
	}

	// Deploy tokens are repository credentials: they must never reach the
	// management API (/api/v1/*), which maps to the "manage" action.
	if principal.TokenKind == TokenKindDeploy && action == "manage" {
		return false, nil
	}

	if principal.IsAnonymous {
		if action != "read" {
			return false, nil
		}

		// Public repositories are readable anonymously regardless of the
		// global flag. "blocked" is enforced at serve time (proxy), not here.
		if repoName != "" {
			repo, err := e.db.GetRepositoryByName(ctx, repoName)
			if err != nil {
				return false, err
			}

			if repo != nil && repo.Public {
				return true, nil
			}
		}

		if !e.allowAnonymousRead {
			return false, nil
		}

		target, err := e.db.GetPermissionTargetByName(ctx, "anonymous-read")
		if err != nil {
			return false, err
		}

		if target == nil {
			return false, nil
		}

		return e.matchTarget(target, repoName, artifactPath, action)
	}

	targets, err := e.principalTargets(ctx, principal)
	if err != nil {
		return false, err
	}

	for _, target := range targets {
		ok, err := e.matchTarget(target, repoName, artifactPath, action)
		if err != nil {
			return false, err
		}

		if ok {
			return true, nil
		}
	}

	return e.projectAllows(ctx, principal, repoName, action)
}

// principalTargets loads the permission targets that apply to a
// (non-anonymous, non-legacy) principal. A token carrying scopes is limited to
// the scoped targets: its effective rights are the intersection of the
// owner's rights and the scopes.
func (e *Evaluator) principalTargets(ctx context.Context, principal Principal) ([]*models.PermissionTarget, error) {
	if principal.UserID == nil || principal.ServiceProjectID != "" {
		return nil, nil
	}

	targets, err := e.db.GetPermissionTargetsForPrincipal(ctx, *principal.UserID, principal.GroupIDs)
	if err != nil {
		return nil, err
	}

	if len(principal.TokenScopes) == 0 {
		return targets, nil
	}

	scoped := make([]*models.PermissionTarget, 0, len(targets))

	for _, t := range targets {
		if slices.Contains(principal.TokenScopes, t.ID) {
			scoped = append(scoped, t)
		}
	}

	return scoped, nil
}

func (e *Evaluator) matchTarget(target *models.PermissionTarget, repoName, artifactPath, action string) (bool, error) {
	if !slices.Contains(target.Actions, action) {
		return false, nil
	}

	// A stored target with an unusable pattern (for example an empty one saved
	// before validation existed) grants nothing; it must neither match
	// everything nor fail every request of the principals that hold it.
	repoRe, err := e.getRegexp(target.RepositoryFilter)
	if err != nil {
		e.warnBadTarget(target, "repository_filter", err)
		return false, nil
	}

	pathRe, err := e.getRegexp(target.PathPattern)
	if err != nil {
		e.warnBadTarget(target, "path_pattern", err)
		return false, nil
	}

	return repoRe.MatchString(repoName) && pathRe.MatchString(artifactPath), nil
}

func (e *Evaluator) warnBadTarget(target *models.PermissionTarget, field string, err error) {
	if e.log == nil {
		return
	}

	e.log.Warnw("permission target has an invalid pattern and is ignored",
		"target_id", target.ID, "target", target.Name, "field", field, "error", err)
}

func (e *Evaluator) getRegexp(pattern string) (*regexp.Regexp, error) {
	if v, ok := e.regexCache.Load(pattern); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re, nil
		}
	}

	re, err := CompilePattern(pattern)
	if err != nil {
		return nil, err
	}

	e.regexCache.Store(pattern, re)

	return re, nil
}
