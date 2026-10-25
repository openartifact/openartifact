// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	TokenPrefixAccess = "oa_pat_"
	TokenPrefixDeploy = "oa_dep_"
	TokenKindAccess   = "access"
	TokenKindDeploy   = "deploy"
)

// lookupDigest returns the hex SHA-256 used as the database lookup key for an
// API token body. The body is 32 bytes (256 bits) from crypto/rand, not a
// human-chosen password, so brute force is infeasible and a slow salted KDF
// (bcrypt/argon2) would only add per-request latency without adding security.
// The digest must stay deterministic and unsalted: it is the index
// GetTokenByHash queries by, and changing it would invalidate stored tokens.
func lookupDigest(body string) string {
	sum := sha256.Sum256([]byte(body))

	return hex.EncodeToString(sum[:])
}

// NewTokenSecret generates a token of the given kind. It returns the secret to
// hand to the user once and the lookup digest to store.
func NewTokenSecret(kind string) (string, string, error) {
	if kind != TokenKindAccess && kind != TokenKindDeploy {
		return "", "", fmt.Errorf("invalid token kind: %s", kind) //nolint:err113 // dynamic error with runtime context
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate token bytes: %w", err)
	}

	body := hex.EncodeToString(raw)

	prefix := TokenPrefixDeploy
	if kind == TokenKindAccess {
		prefix = TokenPrefixAccess
	}

	return prefix + body, lookupDigest(body), nil
}

func CreateToken(ctx context.Context, db database.Database, kind, name, userID string, scopes []string, expiresAt *time.Time) (string, *models.Token, error) {
	tokenString, tokenHash, err := NewTokenSecret(kind)
	if err != nil {
		return "", nil, err
	}

	if scopes == nil {
		scopes = []string{}
	}

	token := &models.Token{
		UserID:    userID,
		Name:      name,
		Kind:      kind,
		TokenHash: tokenHash,
		Scopes:    scopes,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now(),
	}

	saved, err := db.CreateToken(ctx, token)
	if err != nil {
		return "", nil, fmt.Errorf("create token: %w", err)
	}

	return tokenString, saved, nil
}

// CreateProjectToken mints a deploy token for a new service account of the
// project (see database.ProjectTokenSpec); spec.TokenHash is filled here.
func CreateProjectToken(ctx context.Context, db database.Database, spec database.ProjectTokenSpec) (string, *models.Token, error) {
	tokenString, tokenHash, err := NewTokenSecret(TokenKindDeploy)
	if err != nil {
		return "", nil, err
	}

	spec.TokenHash = tokenHash

	saved, err := db.CreateProjectToken(ctx, spec)
	if err != nil {
		return "", nil, err //nolint:wrapcheck // sentinel errors are matched by callers
	}

	return tokenString, saved, nil
}

func ValidateToken(ctx context.Context, db database.Database, tokenString string) (*models.Token, *models.User, error) {
	var body string

	switch {
	case strings.HasPrefix(tokenString, TokenPrefixAccess):
		body = strings.TrimPrefix(tokenString, TokenPrefixAccess)
	case strings.HasPrefix(tokenString, TokenPrefixDeploy):
		body = strings.TrimPrefix(tokenString, TokenPrefixDeploy)
	default:
		return nil, nil, nil
	}

	tokenHash := lookupDigest(body)

	token, err := db.GetTokenByHash(ctx, tokenHash)
	if err != nil {
		return nil, nil, err
	}

	if token == nil {
		return nil, nil, nil
	}

	if token.RevokedAt != nil {
		return nil, nil, nil
	}

	if token.ExpiresAt != nil && time.Now().After(*token.ExpiresAt) {
		return nil, nil, nil
	}

	user, err := db.GetUserByID(ctx, token.UserID)
	if err != nil {
		return nil, nil, err
	}

	if user == nil || user.Disabled {
		return nil, nil, nil
	}

	return token, user, nil
}

// tokenTouchInterval throttles last_used_at writes to once per interval per token.
const tokenTouchInterval = time.Minute

// UpdateLastUsed records token usage synchronously with a short bounded
// timeout (no per-request goroutines). Failures are ignored: usage bookkeeping
// must never fail a request.
func UpdateLastUsed(ctx context.Context, db database.Database, tokenID string) {
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()

	_ = db.UpdateTokenLastUsed(bgCtx, tokenID)
}

// tokenLastTouch maps token ID to the last time this process wrote last_used_at.
//
//nolint:gochecknoglobals // process-wide throttle state
var tokenLastTouch sync.Map

// TouchToken records usage of an authenticated token, at most once per
// tokenTouchInterval so a busy CI client does not hammer one hot row.
//
// The token comes from the auth cache, whose LastUsedAt is stale for the whole
// cache TTL, so the throttle is tracked per token ID in process memory instead.
func TouchToken(ctx context.Context, db database.Database, token *models.Token) {
	now := time.Now()
	if last, ok := tokenLastTouch.Load(token.ID); ok {
		if t, ok := last.(time.Time); ok && now.Sub(t) < tokenTouchInterval {
			return
		}
	} else if token.LastUsedAt != nil && now.Sub(*token.LastUsedAt) < tokenTouchInterval {
		return
	}

	tokenLastTouch.Store(token.ID, now)
	UpdateLastUsed(ctx, db, token.ID)
}
