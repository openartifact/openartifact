// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
	"github.com/openartifact/openartifact/internal/database/models"
)

func setupTokenUser(t *testing.T, db *MockDatabase) *models.User {
	t.Helper()

	ctx := context.Background()
	user := &models.User{
		ID:       "token-test-user",
		Username: "tokenuser",
	}

	saved, err := db.CreateUser(ctx, user)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	return saved
}

func TestToken_AccessPrefix(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := setupTokenUser(t, db)

	tokenStr, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "my-token", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken access: %v", err)
	}

	if !strings.HasPrefix(tokenStr, auth.TokenPrefixAccess) {
		t.Errorf("expected prefix %q, got start of token: %q", auth.TokenPrefixAccess, tokenStr)
	}
}

func TestToken_DeployPrefix(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := setupTokenUser(t, db)

	tokenStr, _, err := auth.CreateToken(ctx, db, auth.TokenKindDeploy, "deploy-token", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken deploy: %v", err)
	}

	if !strings.HasPrefix(tokenStr, auth.TokenPrefixDeploy) {
		t.Errorf("expected prefix %q, got start of token: %q", auth.TokenPrefixDeploy, tokenStr)
	}
}

func TestToken_ValidateCorrect(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := setupTokenUser(t, db)

	tokenStr, record, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "valid-token", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	tok, gotUser, err := auth.ValidateToken(ctx, db, tokenStr)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}

	if tok == nil {
		t.Fatal("expected non-nil token")
	}

	if gotUser == nil {
		t.Fatal("expected non-nil user")
	}

	if tok.ID != record.ID {
		t.Errorf("token ID mismatch: %q != %q", tok.ID, record.ID)
	}

	if gotUser.ID != user.ID {
		t.Errorf("user ID mismatch: %q != %q", gotUser.ID, user.ID)
	}
}

func TestToken_ValidateRevoked(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := setupTokenUser(t, db)

	tokenStr, record, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "revoke-token", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if err := db.RevokeToken(ctx, record.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	tok, gotUser, err := auth.ValidateToken(ctx, db, tokenStr)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}

	if tok != nil {
		t.Error("expected nil token after revocation")
	}

	if gotUser != nil {
		t.Error("expected nil user after revocation")
	}
}

func TestToken_ValidateExpired(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := setupTokenUser(t, db)

	past := time.Now().Add(-1 * time.Hour)

	tokenStr, _, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "expired-token", user.ID, []string{}, &past)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	tok, gotUser, err := auth.ValidateToken(ctx, db, tokenStr)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}

	if tok != nil {
		t.Error("expected nil token for expired token")
	}

	if gotUser != nil {
		t.Error("expected nil user for expired token")
	}
}

func TestToken_ValidateMalformed(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()

	tok, gotUser, err := auth.ValidateToken(ctx, db, "no-prefix-here")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}

	if tok != nil {
		t.Error("expected nil token for malformed string")
	}

	if gotUser != nil {
		t.Error("expected nil user for malformed string")
	}
}

func TestToken_UpdateLastUsed(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()
	user := setupTokenUser(t, db)

	_, record, err := auth.CreateToken(ctx, db, auth.TokenKindAccess, "lasused-token", user.ID, []string{}, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	auth.UpdateLastUsed(ctx, db, record.ID)

	waitUntil(t, 5*time.Second, "last_used_at update", func() bool {
		stored, err := db.GetTokenByID(ctx, record.ID)

		return err == nil && stored != nil && stored.LastUsedAt != nil
	})
}

func TestToken_InvalidKind(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()

	_, _, err := auth.CreateToken(ctx, db, "badkind", "x", "uid", []string{}, nil)
	if err == nil {
		t.Error("expected error for invalid kind")
	}
}
