// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/internal/database/models"
	"github.com/openartifact/openartifact/pkg/logger"
)

type SSOClaims struct {
	ExternalID   string
	ProviderSlug string
	Username     string
	Email        string
	// EmailVerified is true only when the IdP asserted that Email is verified
	// (OIDC email_verified == true). Unverified emails are never used to link
	// to, or overwrite the email of, an existing account.
	EmailVerified bool
	FullName      string
	Groups        []string
	// GroupsPresent distinguishes "the IdP did not tell us the user's groups"
	// (claim or attribute absent, Entra overage not resolved) from "the IdP
	// says the user has no groups". Only a present set, even an empty one,
	// replaces the user's SSO-managed memberships; an absent one keeps them,
	// so a misconfigured IdP cannot silently revoke group access.
	GroupsPresent bool
}

type Provisioner struct {
	db  database.Database
	log *logger.Logger
}

func NewProvisioner(db database.Database, log *logger.Logger) *Provisioner {
	return &Provisioner{db: db, log: log}
}

var ErrUserNotProvisioned = errors.New("user not provisioned")
var ErrAccountDisabled = errors.New("account disabled")
var ErrEmailAlreadyInUse = errors.New("email already in use")

// ErrUsernameUnavailable is returned when no free username could be derived.
var ErrUsernameUnavailable = errors.New("no free username available")
var ErrEmailLinkAmbiguous = errors.New("multiple users share email")

// ErrIdentityConflict is returned when an SSO login would re-bind a local user
// that is already bound to a different external identity or provider.
var ErrIdentityConflict = errors.New("account is already linked to a different sso identity")

//nolint:gocyclo,cyclop // protocol handler: branching mirrors spec state machine
func (p *Provisioner) Provision(ctx context.Context, provider *models.SSOProvider, claims SSOClaims) (*models.User, error) {
	claims.Email = sanitizeSSOEmail(claims.Email)

	user, err := p.db.GetUserByExternalID(ctx, claims.ExternalID, claims.ProviderSlug)
	if err != nil {
		return nil, fmt.Errorf("sso provision get by external id: %w", err)
	}

	if user == nil && provider.LinkByEmail && claims.EmailVerified && claims.Email != "" {
		existing, err := p.db.GetUserByEmail(ctx, claims.Email)
		if err != nil {
			return nil, fmt.Errorf("sso provision get by email: %w", err)
		}

		if existing != nil {
			if existing.Disabled {
				return nil, ErrAccountDisabled
			}

			// Never re-bind an account that already belongs to another SSO
			// identity (different subject or provider).
			if hasSSOIdentity(existing) {
				p.log.Warnw("sso link by email refused: account already linked to another identity",
					"user_id", existing.ID, "provider_slug", claims.ProviderSlug)

				return nil, ErrIdentityConflict
			}

			if err := p.db.UpdateUserSSO(ctx, existing.ID, claims.ExternalID, claims.ProviderSlug); err != nil {
				return nil, fmt.Errorf("sso provision link by email: %w", err)
			}

			existing.ExternalID = &claims.ExternalID
			existing.ProviderSlug = &claims.ProviderSlug
			user = existing
		}
	}

	// Pre-check: when the email cannot be used to link (link_by_email off, or
	// the IdP did not verify it), surface a clean error rather than hitting
	// UNIQUE(email) at INSERT.
	if user == nil && claims.Email != "" && (!provider.LinkByEmail || !claims.EmailVerified) {
		existing, err := p.db.GetUserByEmail(ctx, claims.Email)
		if err != nil {
			return nil, fmt.Errorf("sso provision email conflict check: %w", err)
		}

		if existing != nil {
			return nil, ErrEmailAlreadyInUse
		}
	}

	if user == nil {
		if !provider.AutoProvisionUsers {
			return nil, ErrUserNotProvisioned
		}

		username := ssoUsername(claims)

		newUser := &models.User{
			Username:           username,
			Email:              ptrIfNonEmpty(claims.Email),
			FullName:           ptrIfNonEmpty(claims.FullName),
			Disabled:           false,
			MustChangePassword: false,
			ExternalID:         &claims.ExternalID,
			ProviderSlug:       &claims.ProviderSlug,
			SSOManagedGroups:   true,
		}

		created, err := p.createSSOUser(ctx, newUser, claims)
		if err != nil {
			return nil, err
		}

		user = created
	}

	if user == nil {
		return nil, ErrUserNotProvisioned
	}

	if user.Disabled {
		return nil, ErrAccountDisabled
	}

	emailUpdate, err := p.verifiedEmailUpdate(ctx, user, claims)
	if err != nil {
		return nil, err
	}

	var fullNameUpdate *string
	if claims.FullName != "" {
		fullNameUpdate = &claims.FullName
	}

	// Narrow write: a full-row UPDATE from this snapshot could resurrect a
	// user an admin disabled (or restore an old password hash) mid-login.
	now := time.Now()
	if err := p.db.UpdateUserSSOProfile(ctx, user.ID, emailUpdate, fullNameUpdate, now); err != nil {
		return nil, fmt.Errorf("sso provision update user: %w", err)
	}

	user.LastLoginAt = &now
	if emailUpdate != nil {
		user.Email = emailUpdate
	}

	if fullNameUpdate != nil {
		user.FullName = fullNameUpdate
	}

	p.syncGroups(ctx, user, provider, claims)

	return user, nil
}

// syncGroups applies the IdP group set. Failures are non-fatal: a transient
// error must not block an otherwise valid login.
func (p *Provisioner) syncGroups(ctx context.Context, user *models.User, provider *models.SSOProvider, claims SSOClaims) {
	if !claims.GroupsPresent {
		// Keep whatever the previous login granted; an absent claim is not
		// evidence that the user left every group.
		if len(provider.GroupMapping) > 0 {
			p.log.Warnw("sso login carried no groups claim; keeping existing SSO group memberships",
				"user_id", user.ID, "provider_slug", provider.Slug)
		}

		return
	}

	if err := p.SyncSSOGroupsFromClaims(ctx, user.ID, provider, claims.Groups); err != nil {
		p.log.Warnw("sso group sync failed; login continues", "user_id", user.ID, "error", err)
	}

	if err := p.SyncSSOPermissionsFromGroups(ctx, provider, claims.Groups); err != nil {
		p.log.Warnw("sso permission sync failed; login continues", "user_id", user.ID, "error", err)
	}
}

// maxUsernameAttempts bounds the suffix search on username collisions.
const maxUsernameAttempts = 5

// createSSOUser inserts newUser. A username that already exists (local user,
// other provider) gets a random suffix instead of failing the login for good.
// A unique violation on the SSO identity means a concurrent callback for the
// same subject won the race: the existing row is reloaded and used.
func (p *Provisioner) createSSOUser(ctx context.Context, newUser *models.User, claims SSOClaims) (*models.User, error) {
	base := newUser.Username

	for attempt := range maxUsernameAttempts {
		if attempt > 0 {
			var b [3]byte
			if _, err := rand.Read(b[:]); err != nil {
				return nil, fmt.Errorf("sso provision username suffix: %w", err)
			}

			newUser.Username = base + "-" + hex.EncodeToString(b[:])
		}

		taken, err := p.db.GetUserByUsername(ctx, newUser.Username)
		if err != nil {
			return nil, fmt.Errorf("sso provision username check: %w", err)
		}

		if taken != nil {
			continue
		}

		created, err := p.db.CreateUser(ctx, newUser)
		if err == nil {
			return created, nil
		}

		if !database.IsUniqueViolation(err) {
			return nil, fmt.Errorf("sso provision create user: %w", err)
		}

		// Lost a race: same identity created concurrently, or the username
		// was taken between the check and the insert.
		if existing, gerr := p.db.GetUserByExternalID(ctx, claims.ExternalID, claims.ProviderSlug); gerr == nil && existing != nil {
			return existing, nil
		}
	}

	return nil, fmt.Errorf("sso provision create user: %w", ErrUsernameUnavailable)
}

// hasSSOIdentity reports whether u is already bound to an SSO identity.
func hasSSOIdentity(u *models.User) bool {
	return (u.ExternalID != nil && *u.ExternalID != "") || (u.ProviderSlug != nil && *u.ProviderSlug != "")
}

// verifiedEmailUpdate returns the IdP email when it must be written onto user:
// the IdP verified it, it differs from the current one and no other account
// already owns that address. It returns nil otherwise.
func (p *Provisioner) verifiedEmailUpdate(ctx context.Context, user *models.User, claims SSOClaims) (*string, error) {
	if claims.Email == "" || !claims.EmailVerified {
		return nil, nil //nolint:nilnil // nil means "no update"
	}

	if user.Email != nil && *user.Email == claims.Email {
		return nil, nil //nolint:nilnil // nil means "no update"
	}

	other, err := p.db.GetUserByEmail(ctx, claims.Email)
	if err != nil {
		return nil, fmt.Errorf("sso provision email ownership check: %w", err)
	}

	if other != nil && other.ID != user.ID {
		p.log.Warnw("sso email update skipped: address belongs to another account",
			"user_id", user.ID, "provider_slug", claims.ProviderSlug)

		return nil, nil //nolint:nilnil // nil means "no update"
	}

	return &claims.Email, nil
}

func (p *Provisioner) SyncSSOGroupsFromClaims(ctx context.Context, userID string, provider *models.SSOProvider, idpGroups []string) error {
	groupIDs := make([]string, 0, len(idpGroups))
	for _, idpGroup := range idpGroups {
		localName, ok := provider.GroupMapping[idpGroup]
		if !ok {
			continue
		}

		grp, err := p.db.GetOrCreateGroup(ctx, localName)
		if err != nil {
			return fmt.Errorf("sso group get or create: %w", err)
		}
		if grp == nil {
			continue
		}

		groupIDs = append(groupIDs, grp.ID)
	}

	return p.db.SyncSSOGroups(ctx, userID, groupIDs)
}

// SyncSSOPermissionsFromGroups grants the mapped permission target to every
// local group the logging-in user holds through the IdP group mapping.
//
// It is deliberately add-only. Permission assignments on a group are shared by
// all of its members, so removing one based on a single user's claims would
// strip access from every other member (and flap with login order). Revoking a
// mapping is done by an admin editing the assignment, not by a login.
func (p *Provisioner) SyncSSOPermissionsFromGroups(ctx context.Context, provider *models.SSOProvider, idpGroups []string) error {
	if len(provider.PermissionMapping) == 0 {
		return nil
	}

	for _, idpGroup := range idpGroups {
		localGroupName, ok := provider.GroupMapping[idpGroup]
		if !ok {
			continue
		}

		permTargetName, ok := provider.PermissionMapping[localGroupName]
		if !ok {
			continue
		}

		grp, err := p.db.GetGroupByName(ctx, localGroupName)
		if err != nil || grp == nil {
			continue // group doesn't exist yet, skip
		}

		target, err := p.db.GetPermissionTargetByName(ctx, permTargetName)
		if err != nil || target == nil {
			p.log.Warnw("sso permission mapping references unknown target",
				"provider_slug", provider.Slug,
				"group", localGroupName,
				"target", permTargetName)

			continue
		}

		if err := p.db.GetOrCreatePermissionAssignment(ctx, target.ID, "group", grp.ID); err != nil {
			return fmt.Errorf("sso permission assign: %w", err)
		}
	}

	return nil
}

const (
	minSSOUsernameLen = 3
	maxSSOUsernameLen = 64
	maxSSOEmailLen    = 254
)

var ssoUsernameInvalidRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// sanitizeSSOEmail drops values that are not plausibly an email address
// (no '@', control characters, oversized): they would otherwise reach the
// database verbatim.
func sanitizeSSOEmail(email string) string {
	email = strings.TrimSpace(email)
	if len(email) > maxSSOEmailLen || !strings.Contains(email, "@") {
		return ""
	}

	for _, c := range email {
		if c < 0x21 || c == 0x7f {
			return ""
		}
	}

	return email
}

// sanitizeSSOUsername maps an IdP-supplied name onto the local username
// alphabet ([a-zA-Z0-9_-], 3-64 chars) so IdP users can neither carry control
// characters nor impersonate system principals such as "system/legacy-api-key"
// in audit rows. It returns "" when nothing usable remains.
func sanitizeSSOUsername(name string) string {
	name = strings.Trim(ssoUsernameInvalidRe.ReplaceAllString(strings.TrimSpace(name), "-"), "-")
	if len(name) > maxSSOUsernameLen {
		name = strings.Trim(name[:maxSSOUsernameLen], "-")
	}

	if len(name) < minSSOUsernameLen {
		return ""
	}

	return name
}

// ssoUsername derives the local username for a new SSO user: the IdP username,
// else the email local part, else the subject, else a hash of the subject.
func ssoUsername(claims SSOClaims) string {
	candidates := []string{claims.Username}
	if local, _, ok := strings.Cut(claims.Email, "@"); ok {
		candidates = append(candidates, local)
	}

	candidates = append(candidates, claims.ExternalID)

	for _, c := range candidates {
		if u := sanitizeSSOUsername(c); u != "" {
			return u
		}
	}

	sum := sha256.Sum256([]byte(claims.ProviderSlug + "|" + claims.ExternalID))

	return "sso-" + hex.EncodeToString(sum[:6])
}

func ptrIfNonEmpty(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}
