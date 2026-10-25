// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

const (
	// BootstrapUsername is the local account created on first start.
	BootstrapUsername = "admin"
	// BootstrapTokenName is the name of the access token created from
	// BootstrapOptions.AdminTokenFile.
	BootstrapTokenName = "bootstrap"

	// Bounds of a supplied token body (the part after the oa_pat_ prefix).
	// The generated ones are 64 hex characters.
	minBootstrapTokenBody = 32
	maxBootstrapTokenBody = 128
	// minBootstrapTokenDistinct rejects obviously low-entropy bodies such as
	// "aaaa…" or "0123456789" repeated.
	minBootstrapTokenDistinct = 12
	// maxBootstrapSecretFile bounds what is read from a secret file.
	maxBootstrapSecretFile = 4096
)

var (
	// ErrBootstrapSecret is wrapped by every invalid bootstrap secret error.
	ErrBootstrapSecret = errors.New("invalid bootstrap secret")
	errBootstrapTTL    = errors.New("bootstrap TTLs must not be negative")
)

// BootstrapOptions configures first-admin provisioning (config section
// "bootstrap"). The zero value generates a random password that never expires.
type BootstrapOptions struct {
	// AdminPasswordFile is read for the initial password; it wins over
	// AdminPassword.
	AdminPasswordFile string
	AdminPassword     string
	// ForcePasswordChange forces a password change at the first login of a
	// supplied password. A generated password always forces it.
	ForcePasswordChange bool
	// AdminTokenFile is read for the secret of the "bootstrap" access token.
	AdminTokenFile string
	// AdminTokenTTL is the lifetime of the bootstrap token; 0 = none.
	AdminTokenTTL time.Duration
	// GeneratedPasswordTTL is how long a generated password stays valid;
	// 0 = forever.
	GeneratedPasswordTTL time.Duration

	// Now is the clock (default time.Now); Stderr receives the banner
	// (default os.Stderr).
	Now    func() time.Time
	Stderr io.Writer
}

func (o *BootstrapOptions) suppliesAnything() bool {
	return o.AdminPasswordFile != "" || o.AdminPassword != "" || o.AdminTokenFile != ""
}

// BootstrapIfEmpty creates the first admin account on an empty instance.
//
// The user row, its admin-all assignment and the optional bootstrap token are
// written in one transaction guarded by an advisory lock (CreateBootstrapAdmin),
// so concurrent replicas cannot both create an admin, and an interrupted
// bootstrap cannot leave an admin without rights. When users already exist it
// ignores the supplied secrets and only repairs the "admin exists but nobody
// holds admin-all" state left by older versions.
func BootstrapIfEmpty(ctx context.Context, db database.Database, log *logger.Logger, opts BootstrapOptions) error {
	count, err := db.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}

	if count > 0 {
		if opts.suppliesAnything() {
			log.Infow("bootstrap: the instance already has users; bootstrap.adminPassword*, bootstrap.adminTokenFile are ignored")
		}

		repaired, err := db.RepairBootstrapAdmin(ctx, BootstrapUsername)
		if err != nil {
			return fmt.Errorf("repair bootstrap admin: %w", err)
		}

		if repaired {
			log.Warnw("bootstrap: re-assigned admin-all to the local admin user", "username", BootstrapUsername)
		}

		return nil
	}

	return createBootstrapAdmin(ctx, db, log, opts)
}

func createBootstrapAdmin(ctx context.Context, db database.Database, log *logger.Logger, opts BootstrapOptions) error {
	if opts.AdminTokenTTL < 0 || opts.GeneratedPasswordTTL < 0 {
		return errBootstrapTTL
	}

	if opts.Now == nil {
		opts.Now = time.Now
	}

	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	now := opts.Now()

	spec := database.BootstrapAdmin{Username: BootstrapUsername}

	password, supplied, err := bootstrapPassword(opts)
	if err != nil {
		return err
	}

	spec.MustChangePassword = !supplied || opts.ForcePasswordChange

	if !supplied && opts.GeneratedPasswordTTL > 0 {
		exp := now.Add(opts.GeneratedPasswordTTL)
		spec.PasswordExpiresAt = &exp
	}

	if spec.PasswordHash, err = HashPassword(password); err != nil {
		return fmt.Errorf("hash bootstrap password: %w", err)
	}

	if opts.AdminTokenFile != "" {
		secret, err := readSecretFile("bootstrap.adminTokenFile", opts.AdminTokenFile)
		if err != nil {
			return err
		}

		if spec.TokenHash, err = bootstrapTokenDigest(secret); err != nil {
			return err
		}

		spec.TokenName = BootstrapTokenName

		if opts.AdminTokenTTL > 0 {
			exp := now.Add(opts.AdminTokenTTL)
			spec.TokenExpiresAt = &exp
		}
	}

	created, err := db.CreateBootstrapAdmin(ctx, spec)
	if err != nil {
		return fmt.Errorf("create bootstrap admin: %w", err)
	}

	if !created {
		// Another replica won the first-boot race; its settings are the valid ones.
		return nil
	}

	if !supplied {
		printGeneratedPassword(opts.Stderr, password, opts.GeneratedPasswordTTL)
	}

	log.Infow("Bootstrap completed: admin user created",
		"username", BootstrapUsername,
		"password_supplied", supplied,
		"token_created", spec.TokenHash != "",
	)

	return nil
}

// bootstrapPassword returns the supplied password (file first, then the plain
// value) validated against the password policy, or a freshly generated one.
func bootstrapPassword(opts BootstrapOptions) (string, bool, error) {
	var password string

	switch {
	case opts.AdminPasswordFile != "":
		var err error

		password, err = readSecretFile("bootstrap.adminPasswordFile", opts.AdminPasswordFile)
		if err != nil {
			return "", false, err
		}
	case opts.AdminPassword != "":
		password = opts.AdminPassword
	default:
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return "", false, fmt.Errorf("generate bootstrap password: %w", err)
		}

		return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(raw), false, nil
	}

	if err := ValidatePassword(password); err != nil {
		return "", false, fmt.Errorf("%w: bootstrap admin password: %w", ErrBootstrapSecret, err)
	}

	return password, true, nil
}

// readSecretFile reads a small secret file and trims trailing newlines.
func readSecretFile(key, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrBootstrapSecret, key, err)
	}

	defer func() { _ = f.Close() }()

	b, err := io.ReadAll(io.LimitReader(f, maxBootstrapSecretFile+1))
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrBootstrapSecret, key, err)
	}

	if len(b) > maxBootstrapSecretFile {
		return "", fmt.Errorf("%w: %s: file larger than %d bytes", ErrBootstrapSecret, key, maxBootstrapSecretFile)
	}

	return strings.TrimRight(string(b), "\r\n"), nil
}

// bootstrapTokenDigest validates a supplied token secret and returns the
// lookup digest to store. The secret must be "oa_pat_" followed by 32 to 128
// characters from A-Za-z0-9_- with at least 12 distinct characters, the shape
// of a generated token (`oa_pat_$(openssl rand -hex 32)`).
func bootstrapTokenDigest(secret string) (string, error) {
	body, ok := strings.CutPrefix(secret, TokenPrefixAccess)
	if !ok {
		return "", fmt.Errorf("%w: bootstrap.adminTokenFile: token must start with %q", ErrBootstrapSecret, TokenPrefixAccess)
	}

	if len(body) < minBootstrapTokenBody || len(body) > maxBootstrapTokenBody {
		return "", fmt.Errorf("%w: bootstrap.adminTokenFile: token body must be %d to %d characters", ErrBootstrapSecret, minBootstrapTokenBody, maxBootstrapTokenBody)
	}

	distinct := map[rune]struct{}{}

	for _, c := range body {
		if !isTokenChar(c) {
			return "", fmt.Errorf("%w: bootstrap.adminTokenFile: token body may only contain A-Z a-z 0-9 _ -", ErrBootstrapSecret)
		}

		distinct[c] = struct{}{}
	}

	if len(distinct) < minBootstrapTokenDistinct {
		return "", fmt.Errorf("%w: bootstrap.adminTokenFile: token looks low-entropy (fewer than %d distinct characters); generate one with: echo oa_pat_$(openssl rand -hex 32)", ErrBootstrapSecret, minBootstrapTokenDistinct)
	}

	return lookupDigest(body), nil
}

func isTokenChar(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func printGeneratedPassword(w io.Writer, password string, ttl time.Duration) {
	expiry := "never expires"
	if ttl > 0 {
		expiry = "valid for " + ttl.String() + " (after that, reset it with cmd/resetpw)"
	}

	fmt.Fprintf(w,
		"\n===== OPENARTIFACT BOOTSTRAP =====\n admin password: %s\n %s\n Change immediately at /api/v1/auth/change-password\n To choose the password instead, set OPENARTIFACT_BOOTSTRAP_ADMIN_PASSWORD_FILE\n (bootstrap.adminPasswordFile) before the first start.\n===================================\n",
		password, expiry,
	)
}
