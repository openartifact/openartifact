// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The blob upserts must refuse a malformed digest before touching the
// database: a zero-value PostgresDB has no connection, so any attempt to run
// the INSERT would panic instead of returning ErrInvalidBlobSHA256.
func TestBlobUpsertsRejectInvalidSHA256(t *testing.T) {
	valid := strings.Repeat("ab", 32)

	bad := map[string]string{
		"empty":     "",
		"prefixed":  "sha256:" + valid,
		"uppercase": strings.ToUpper(valid),
		"short":     valid[:63],
		"long":      valid + "0",
		"non-hex":   strings.Repeat("zz", 32),
	}

	p := &PostgresDB{}
	ctx := context.Background()

	for name, sha := range bad {
		t.Run(name, func(t *testing.T) {
			if id, err := p.upsertBlobWithMediaType(
				ctx,
				sha,
				sha,
				"application/zip",
				1,
			); !errors.Is(err, ErrInvalidBlobSHA256) ||
				id != "" {
				t.Errorf("upsertBlobWithMediaType(%q) = %q, %v; want ErrInvalidBlobSHA256", sha, id, err)
			}

			if id, err := p.UpsertBlob(ctx, sha, sha, 1); !errors.Is(err, ErrInvalidBlobSHA256) || id != "" {
				t.Errorf("UpsertBlob(%q) = %q, %v; want ErrInvalidBlobSHA256", sha, id, err)
			}

			if id, err := p.EnsureBlob(ctx, sha, "application/zip", 1); !errors.Is(err, ErrInvalidBlobSHA256) || id != "" {
				t.Errorf("EnsureBlob(%q) = %q, %v; want ErrInvalidBlobSHA256", sha, id, err)
			}
		})
	}

	if !IsValidBlobSHA256(valid) {
		t.Errorf("IsValidBlobSHA256(%q) = false", valid)
	}
}
