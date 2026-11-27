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
	// when non-empty the token's rights are the intersection of those targets
	// (assigned to the owner or not) and the owner's own rights.
	TokenKind   string
	TokenScopes []string
	// TokenID is the id of that token (audit events record it).
	TokenID string
	// ServiceProjectID is set for a project token's service account: the only
	// project the principal belongs to. Such a principal ignores permission
	// targets and holds only the role of its own project.
	ServiceProjectID string
}

// IsToken reports whether the request was authenticated with an API token.
func (p Principal) IsToken() bool {
	return p.TokenKind != ""
}

// EvaluatorStore is the part of the database Evaluator needs.
type EvaluatorStore interface {
	GetPermissionTargetByName(ctx context.Context, name string) (*models.PermissionTarget, error)
	GetPermissionTargetsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]*models.PermissionTarget, error)
	GetPermissionTargetsByIDs(ctx context.Context, ids []string) ([]*models.PermissionTarget, error)
	GetProjectByName(ctx context.Context, name string) (*models.Project, error)
	GetRepositoryByName(ctx context.Context, name string) (*models.Repository, error)
	ListProjectGrantsByUser(ctx context.Context, userID string) ([]*models.ProjectGrant, error)
	ListPublicRepositoryNames(ctx context.Context) ([]string, error)
}

type Evaluator struct {
	db                 EvaluatorStore
	regexCache         sync.Map
	allowAnonymousRead bool
	log                *logger.Logger
}

func NewEvaluator(db EvaluatorStore, log *logger.Logger, allowAnonymousRead bool) *Evaluator {
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

	held, err := e.principalGrants(ctx, principal)
	if err != nil {
		return false, err
	}

	ok, err := e.grantsAllow(held, repoName, artifactPath, action)
	if err != nil {
		return false, err
	}

	if ok {
		return true, nil
	}

	return e.projectAllows(ctx, principal, repoName, action)
}

// grants are the permission targets that decide a (non-anonymous, non-legacy)
// principal's requests.
type grants struct {
	// owner are the targets the principal holds: assigned to the user or to
	// one of its groups.
	owner []*models.PermissionTarget
	// scopes are the targets a token is limited to, loaded by ID whether or
	// not they are assigned to the owner. Only meaningful when scoped.
	scopes []*models.PermissionTarget
	// scoped is set for a token carrying scopes: a request must then match a
	// scope target and an owner target.
	scoped bool
}

// principalGrants loads the permission targets that apply to a
// (non-anonymous, non-legacy) principal. A token carrying scopes gets the
// intersection of its scope targets and the owner's rights, so a scope can
// only ever narrow what the owner may do, and a scope target that is not
// assigned to the owner still narrows it (it grants nothing by itself).
func (e *Evaluator) principalGrants(ctx context.Context, principal Principal) (grants, error) {
	if principal.UserID == nil || principal.ServiceProjectID != "" {
		return grants{}, nil
	}

	g := grants{scoped: len(principal.TokenScopes) > 0}

	if g.scoped {
		var err error

		g.scopes, err = e.db.GetPermissionTargetsByIDs(ctx, principal.TokenScopes)
		if err != nil {
			return grants{}, err
		}

		// Every scope target is gone: the token grants nothing.
		if len(g.scopes) == 0 {
			return g, nil
		}
	}

	var err error

	g.owner, err = e.db.GetPermissionTargetsForPrincipal(ctx, *principal.UserID, principal.GroupIDs)
	if err != nil {
		return grants{}, err
	}

	return g, nil
}

// grantsAllow reports whether the grants allow the action on the path of the
// repository: an owner target must match and, for a scoped token, a scope
// target as well.
func (e *Evaluator) grantsAllow(g grants, repoName, artifactPath, action string) (bool, error) {
	if g.scoped {
		ok, err := e.anyTargetMatches(g.scopes, repoName, artifactPath, action)
		if err != nil || !ok {
			return false, err
		}
	}

	return e.anyTargetMatches(g.owner, repoName, artifactPath, action)
}

func (e *Evaluator) anyTargetMatches(targets []*models.PermissionTarget, repoName, artifactPath, action string) (bool, error) {
	for _, target := range targets {
		ok, err := e.matchTarget(target, repoName, artifactPath, action)
		if err != nil {
			return false, err
		}

		if ok {
			return true, nil
		}
	}

	return false, nil
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
