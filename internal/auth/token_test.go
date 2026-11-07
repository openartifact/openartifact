// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestCreateToken_AccessPrefix(t *testing.T) {
	db := newTokenMockDB()
	ctx := context.Background()

	tokenStr, record, err := CreateToken(ctx, db, TokenKindAccess, "ci", "user-1", nil, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if !strings.HasPrefix(tokenStr, TokenPrefixAccess) {
		t.Errorf("expected prefix %s, got %s", TokenPrefixAccess, tokenStr)
	}

	if record.Kind != TokenKindAccess {
		t.Errorf("expected kind access, got %s", record.Kind)
	}
}

func TestCreateToken_DeployPrefix(t *testing.T) {
	db := newTokenMockDB()
	ctx := context.Background()

	tokenStr, _, err := CreateToken(ctx, db, TokenKindDeploy, "deploy", "user-1", nil, nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if !strings.HasPrefix(tokenStr, TokenPrefixDeploy) {
		t.Errorf("expected prefix %s, got %s", TokenPrefixDeploy, tokenStr)
	}
}

func TestValidateToken_Valid(t *testing.T) {
	db := newTokenMockDB()
	ctx := context.Background()

	hash := "abc123"
	user := &models.User{ID: "user-1", Username: "alice", Disabled: false}
	token := &models.Token{ID: "tok-1", UserID: "user-1", Kind: TokenKindAccess, TokenHash: hash}
	db.tokens[hash] = token
	db.tokensByID["tok-1"] = token
	db.users["user-1"] = user

	tokenStr := TokenPrefixAccess + "dummybody"
	hashBytes := sha256ForString("dummybody")
	db.tokens[hashBytes] = token

	got, gotUser, err := ValidateToken(ctx, db, TokenPrefixAccess+"dummybody")
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}

	if got == nil {
		t.Fatal("expected non-nil token")
	}

	if gotUser == nil {
		t.Fatal("expected non-nil user")
	}

	_ = tokenStr
}

func TestValidateToken_Expired(t *testing.T) {
	db := newTokenMockDB()
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	user := &models.User{ID: "user-1", Disabled: false}
	token := &models.Token{ID: "tok-2", UserID: "user-1", ExpiresAt: &past}
	body := "expiredbody"
	h := sha256ForString(body)
	token.TokenHash = h
	db.tokens[h] = token
	db.tokensByID["tok-2"] = token
	db.users["user-1"] = user

	got, _, err := ValidateToken(ctx, db, TokenPrefixAccess+body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != nil {
		t.Error("expected nil for expired token")
	}
}

func TestValidateToken_Revoked(t *testing.T) {
	db := newTokenMockDB()
	ctx := context.Background()

	now := time.Now()
	user := &models.User{ID: "user-1", Disabled: false}
	token := &models.Token{ID: "tok-3", UserID: "user-1", RevokedAt: &now}
	body := "revokedbody"
	h := sha256ForString(body)
	token.TokenHash = h
	db.tokens[h] = token
	db.tokensByID["tok-3"] = token
	db.users["user-1"] = user

	got, _, err := ValidateToken(ctx, db, TokenPrefixAccess+body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != nil {
		t.Error("expected nil for revoked token")
	}
}

func TestUpdateLastUsed_WritesTokenID(t *testing.T) {
	db := &touchCountingDB{}

	UpdateLastUsed(context.Background(), db, "tok-1")

	if len(db.writes) != 1 || db.writes[0] != "tok-1" {
		t.Fatalf("writes = %v, want [tok-1]", db.writes)
	}
}

func TestValidateToken_UnknownPrefix(t *testing.T) {
	db := newTokenMockDB()
	ctx := context.Background()

	got, _, err := ValidateToken(ctx, db, "unknown_prefix_abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != nil {
		t.Error("expected nil for unknown prefix")
	}
}

// A token of a disabled account (a service account's exchanged token included)
// authenticates nothing, even though the token itself is valid.
func TestValidateToken_DisabledAccount(t *testing.T) {
	db := newTokenMockDB()
	body := "disabledbody"
	h := sha256ForString(body)
	token := &models.Token{ID: "tok-4", UserID: "sa-1", TokenHash: h}
	db.tokens[h] = token
	db.tokensByID["tok-4"] = token
	db.users["sa-1"] = &models.User{ID: "sa-1", Disabled: true}

	got, user, err := ValidateToken(context.Background(), db, TokenPrefixAccess+body)
	if err != nil || got != nil || user != nil {
		t.Fatalf("ValidateToken = %v, %v, %v; want nothing", got, user, err)
	}
}
