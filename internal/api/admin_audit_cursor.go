// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openartifact/openartifact/internal/database/models"
)

// Cursor timestamps must fall in years 0001..9999, well inside timestamptz.
const (
	minAuditCursorMicros = -62135596800 * 1_000_000
	maxAuditCursorMicros = 253402300799 * 1_000_000
)

var errInvalidAuditCursor = errors.New("invalid audit cursor")

// encodeAuditCursor renders a keyset position as an opaque URL-safe token:
// base64url("<created_at unix microseconds>:<entry id>"). Postgres stores
// timestamptz at microsecond precision, so the round trip is exact.
func encodeAuditCursor(c *models.AuditCursor) string {
	raw := strconv.FormatInt(c.CreatedAt.UnixMicro(), 10) + ":" + c.ID

	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeAuditCursor parses a token produced by encodeAuditCursor.
func decodeAuditCursor(s string) (*models.AuditCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errInvalidAuditCursor
	}

	micros, id, ok := strings.Cut(string(raw), ":")
	if !ok {
		return nil, errInvalidAuditCursor
	}

	us, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return nil, errInvalidAuditCursor
	}

	// time.UnixMicro silently wraps outside Postgres's timestamptz range; a
	// forged cursor must be a 400, not a 500 from the query.
	if us < minAuditCursorMicros || us > maxAuditCursorMicros {
		return nil, errInvalidAuditCursor
	}

	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, errInvalidAuditCursor
	}

	return &models.AuditCursor{CreatedAt: time.UnixMicro(us).UTC(), ID: parsed.String()}, nil
}
