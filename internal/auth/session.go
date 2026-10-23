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
	"net"
	"time"

	"github.com/openartifact/openartifact/internal/crypto"
	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
)

const (
	SessionCookieName           = "oa_session"
	defaultSessionDurationHours = 24
	// defaultSessionAbsoluteDays caps how long a session may live no matter how
	// often it is used; the sliding idle expiry can never extend past it.
	defaultSessionAbsoluteDays = 30
	// sessionTouchInterval throttles the sliding-expiry write to once per
	// interval per session instead of once per request.
	sessionTouchInterval = time.Minute
)

//nolint:gochecknoglobals // exported session duration, configurable at startup
var SessionDuration = defaultSessionDurationHours * time.Hour

//nolint:gochecknoglobals // absolute session lifetime, configurable at startup
var sessionAbsoluteLifetime = defaultSessionAbsoluteDays * 24 * time.Hour

func ConfigureSessionDuration(d time.Duration) {
	SessionDuration = d
}

// ConfigureSessionAbsoluteLifetime sets the hard cap on a session's total life.
func ConfigureSessionAbsoluteLifetime(d time.Duration) {
	sessionAbsoluteLifetime = d
}

// SessionAbsoluteLifetime is the hard cap on a session's total lifetime. It is
// never shorter than the idle duration.
func SessionAbsoluteLifetime() time.Duration {
	return max(sessionAbsoluteLifetime, SessionDuration)
}

func CreateSession(ctx context.Context, db database.Database, userID, ip, userAgent string) (string, *models.Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate session token: %w", err)
	}

	cookieValue := hex.EncodeToString(raw)
	hashBytes := sha256.Sum256(raw)
	tokenHash := hex.EncodeToString(hashBytes[:])

	now := time.Now()
	expiresAt := now.Add(SessionDuration)

	session := &models.Session{
		UserID:     userID,
		TokenHash:  tokenHash,
		ExpiresAt:  expiresAt,
		CreatedAt:  now,
		LastSeenAt: now,
	}

	if ip != "" {
		// Strip optional port (RemoteAddr is "host:port") so the value is a
		// valid Postgres inet literal.
		if host, _, splitErr := net.SplitHostPort(ip); splitErr == nil {
			ip = host
		}

		session.IPAddress = &ip
	}

	if userAgent != "" {
		session.UserAgent = &userAgent
	}

	saved, err := db.CreateSession(ctx, session)
	if err != nil {
		return "", nil, fmt.Errorf("create session: %w", err)
	}

	return cookieValue, saved, nil
}

// decodeSessionCookie decodes a hex session cookie and reports whether it has
// the expected 32-byte length.
func decodeSessionCookie(cookieValue string) ([]byte, bool) {
	raw, err := hex.DecodeString(cookieValue)

	return raw, err == nil && len(raw) == 32
}

func ValidateSession(ctx context.Context, db database.Database, cookieValue string) (*models.Session, error) {
	raw, ok := decodeSessionCookie(cookieValue)
	if !ok {
		// A malformed cookie is simply an invalid session, not a request error.
		return nil, nil //nolint:nilnil // no valid session
	}

	hashBytes := sha256.Sum256(raw)
	tokenHash := hex.EncodeToString(hashBytes[:])

	session, err := db.GetSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, err
	}

	if session == nil {
		return nil, nil //nolint:nilnil // no valid session found is not an error
	}

	now := time.Now()
	// created_at is NOT NULL in the schema; a zero value only comes from a
	// hand-built record and carries no age information.
	if now.After(session.ExpiresAt) ||
		(!session.CreatedAt.IsZero() && now.After(session.CreatedAt.Add(SessionAbsoluteLifetime()))) {
		return nil, nil //nolint:nilnil // expired session is not an error
	}

	return session, nil
}

// TouchSession slides the idle expiry of a session forward, at most once per
// sessionTouchInterval and never beyond the absolute session lifetime.
func TouchSession(ctx context.Context, db database.Database, session *models.Session) error {
	now := time.Now()
	if now.Sub(session.LastSeenAt) < sessionTouchInterval {
		return nil
	}

	expiresAt := now.Add(SessionDuration)
	if !session.CreatedAt.IsZero() {
		if limit := session.CreatedAt.Add(SessionAbsoluteLifetime()); expiresAt.After(limit) {
			expiresAt = limit
		}
	}

	return db.UpdateSessionLastSeen(ctx, session.ID, expiresAt)
}

// RevokeAllSessions deletes every session of a user (password change/reset,
// account disable).
func RevokeAllSessions(ctx context.Context, db database.Database, userID string) error {
	return db.DeleteSessionsByUser(ctx, userID)
}

// RevokeOtherSessions deletes every session of a user except the one carried
// by keepCookie. An unusable cookie keeps nothing.
func RevokeOtherSessions(ctx context.Context, db database.Database, userID, keepCookie string) error {
	raw, ok := decodeSessionCookie(keepCookie)
	if !ok {
		return db.DeleteSessionsByUser(ctx, userID)
	}

	hashBytes := sha256.Sum256(raw)

	return db.DeleteOtherSessionsByUser(ctx, userID, hex.EncodeToString(hashBytes[:]))
}

func ExtendSession(ctx context.Context, db database.Database, sessionID string) error {
	return db.UpdateSessionLastSeen(ctx, sessionID, time.Now().Add(SessionDuration))
}

func DeleteSession(ctx context.Context, db database.Database, cookieValue string) error {
	raw, ok := decodeSessionCookie(cookieValue)
	if !ok {
		// Nothing to delete for a malformed cookie.
		return nil
	}

	hashBytes := sha256.Sum256(raw)
	tokenHash := hex.EncodeToString(hashBytes[:])

	return db.DeleteSession(ctx, tokenHash)
}

// AttachSSOSession records the SSO provider that created the session
// identified by sessionID together with the (encrypted) ID token, so logout
// can run RP-initiated logout.
func AttachSSOSession(ctx context.Context, db database.Database, sessionID, providerSlug, rawIDToken string) error {
	enc, err := crypto.Encrypt(rawIDToken)
	if err != nil {
		return fmt.Errorf("encrypt id token: %w", err)
	}

	return db.SetSessionSSO(ctx, sessionID, providerSlug, enc)
}

// SessionSSOInfo is the decrypted SSO record of a session.
type SessionSSOInfo struct {
	ProviderSlug string
	IDToken      string
}

// LookupSessionSSO returns the SSO provider and ID token behind a session
// cookie, or nil when the session is not an SSO session (or is unknown).
func LookupSessionSSO(ctx context.Context, db database.Database, cookieValue string) (*SessionSSOInfo, error) {
	raw, ok := decodeSessionCookie(cookieValue)
	if !ok {
		return nil, nil //nolint:nilnil // nil means "not an SSO session"
	}

	hashBytes := sha256.Sum256(raw)

	rec, err := db.GetSessionSSO(ctx, hex.EncodeToString(hashBytes[:]))
	if err != nil {
		return nil, fmt.Errorf("get session sso: %w", err)
	}

	if rec == nil {
		return nil, nil //nolint:nilnil // nil means "not an SSO session"
	}

	idToken, err := crypto.Decrypt(rec.IDToken)
	if err != nil {
		return nil, fmt.Errorf("decrypt id token: %w", err)
	}

	return &SessionSSOInfo{ProviderSlug: rec.ProviderSlug, IDToken: idToken}, nil
}
