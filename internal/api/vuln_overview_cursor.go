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

var errInvalidVulnCursor = errors.New("invalid vulnerability scan cursor")

// encodeVulnCursor renders a keyset position as an opaque URL-safe token:
// base64url("<critical>:<high>:<medium>:<low>:<sort time unix microseconds>:<scan id>").
// Postgres stores timestamptz at microsecond precision, so the round trip is
// exact.
func encodeVulnCursor(c *models.VulnScanCursor) string {
	raw := strconv.Itoa(c.CriticalCount) + ":" + strconv.Itoa(c.HighCount) + ":" + strconv.Itoa(c.MediumCount) + ":" + strconv.Itoa(c.LowCount) + ":" + strconv.FormatInt(c.SortAt.UnixMicro(), 10) + ":" + c.ID

	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeVulnCursor parses a token produced by encodeVulnCursor. A forged
// value is an error here, never a failing query.
func decodeVulnCursor(s string) (*models.VulnScanCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errInvalidVulnCursor
	}

	parts := strings.Split(string(raw), ":")
	if len(parts) != 6 {
		return nil, errInvalidVulnCursor
	}

	var counts [4]int

	for i := range counts {
		v, err := strconv.ParseInt(parts[i], 10, 32)
		if err != nil || v < 0 {
			return nil, errInvalidVulnCursor
		}

		counts[i] = int(v)
	}

	// time.UnixMicro wraps outside Postgres's timestamptz range.
	us, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || us < minAuditCursorMicros || us > maxAuditCursorMicros {
		return nil, errInvalidVulnCursor
	}

	id, err := uuid.Parse(parts[5])
	if err != nil {
		return nil, errInvalidVulnCursor
	}

	return &models.VulnScanCursor{
		CriticalCount: counts[0],
		HighCount:     counts[1],
		MediumCount:   counts[2],
		LowCount:      counts[3],
		SortAt:        time.UnixMicro(us).UTC(),
		ID:            id.String(),
	}, nil
}
