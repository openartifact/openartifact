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

	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, errInvalidAuditCursor
	}

	return &models.AuditCursor{CreatedAt: time.UnixMicro(us).UTC(), ID: parsed.String()}, nil
}
