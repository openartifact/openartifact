// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/auth"
)

func TestSession_CreateAndValidate(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()

	userID := "user-session-test"

	cookieValue, session, err := auth.CreateSession(ctx, db, userID, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if cookieValue == "" {
		t.Fatal("expected non-empty cookie value")
	}

	if session == nil {
		t.Fatal("expected non-nil session")
	}

	if session.UserID != userID {
		t.Errorf("session.UserID = %q, want %q", session.UserID, userID)
	}

	got, err := auth.ValidateSession(ctx, db, cookieValue)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}

	if got == nil {
		t.Fatal("expected non-nil session from ValidateSession")
	}

	if got.UserID != userID {
		t.Errorf("validated session UserID = %q, want %q", got.UserID, userID)
	}
}

func TestSession_Validate_NonExistent(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()

	fakeCookie := "0000000000000000000000000000000000000000000000000000000000000000"

	got, err := auth.ValidateSession(ctx, db, fakeCookie)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != nil {
		t.Fatal("expected nil for non-existent session")
	}
}

func TestSession_Validate_MalformedCookie(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()

	got, err := auth.ValidateSession(ctx, db, "not-a-valid-hex-string!!")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got != nil {
		t.Fatal("expected nil for malformed cookie")
	}
}

func TestSession_TouchSlidesExpiry(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()

	cookieValue, session, err := auth.CreateSession(ctx, db, "user-extend", "127.0.0.1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Last seen long enough ago for the throttled write to happen.
	originalExpiry := time.Now().Add(time.Minute)
	session.ExpiresAt = originalExpiry
	session.LastSeenAt = time.Now().Add(-2 * time.Minute)

	if err := auth.TouchSession(ctx, db, session); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}

	got, err := auth.ValidateSession(ctx, db, cookieValue)
	if err != nil {
		t.Fatalf("ValidateSession after touch: %v", err)
	}

	if got == nil {
		t.Fatal("expected non-nil session after touch")
	}

	if !got.ExpiresAt.After(originalExpiry) {
		t.Errorf("ExpiresAt was not extended: original=%v, got=%v", originalExpiry, got.ExpiresAt)
	}
}

func TestSession_Delete(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()

	cookieValue, _, err := auth.CreateSession(ctx, db, "user-delete", "127.0.0.1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := auth.DeleteSession(ctx, db, cookieValue); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	got, err := auth.ValidateSession(ctx, db, cookieValue)
	if err != nil {
		t.Fatalf("ValidateSession after delete: %v", err)
	}

	if got != nil {
		t.Fatal("expected nil session after deletion")
	}
}

func TestSession_Expired(t *testing.T) {
	ctx := context.Background()
	db := newMockDatabase()

	cookieValue, session, err := auth.CreateSession(ctx, db, "user-expired", "127.0.0.1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	pastTime := time.Now().Add(-2 * time.Hour)
	session.ExpiresAt = pastTime

	got, err := auth.ValidateSession(ctx, db, cookieValue)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}

	if got != nil {
		t.Fatal("expected nil for expired session")
	}
}
