// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/openartifact/openartifact/internal/database"
	"github.com/openartifact/openartifact/pkg/logger"
)

// BootstrapUsername is the local account created on first start.
const BootstrapUsername = "admin"

// BootstrapIfEmpty creates the first admin account on an empty instance.
//
// The user row and its admin-all assignment are written in one transaction
// guarded by an advisory lock (CreateBootstrapAdmin), so concurrent replicas
// cannot both create an admin, and an interrupted bootstrap cannot leave an
// admin without rights. When users already exist it only repairs the
// "admin exists but nobody holds admin-all" state left by older versions.
func BootstrapIfEmpty(ctx context.Context, db database.Database, log *logger.Logger) error {
	count, err := db.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}

	if count > 0 {
		repaired, err := db.RepairBootstrapAdmin(ctx, BootstrapUsername)
		if err != nil {
			return fmt.Errorf("repair bootstrap admin: %w", err)
		}

		if repaired {
			log.Warnw("bootstrap: re-assigned admin-all to the local admin user", "username", BootstrapUsername)
		}

		return nil
	}

	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate bootstrap password: %w", err)
	}

	password := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(raw)

	hash, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("hash bootstrap password: %w", err)
	}

	created, err := db.CreateBootstrapAdmin(ctx, BootstrapUsername, hash)
	if err != nil {
		return fmt.Errorf("create bootstrap admin: %w", err)
	}

	if !created {
		// Another replica won the first-boot race; its password is the valid one.
		return nil
	}

	fmt.Fprintf(os.Stderr,
		"\n===== OPENARTIFACT BOOTSTRAP =====\n admin password: %s\n Change immediately at /api/v1/auth/change-password\n===================================\n",
		password,
	)

	log.Infow("Bootstrap completed: admin user created", "username", BootstrapUsername)

	return nil
}
