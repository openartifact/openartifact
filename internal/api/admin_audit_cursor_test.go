// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/base64"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/openartifact/openartifact/internal/database/models"
)

func TestAuditCursorRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
	}{
		{name: "microsecond precision", at: time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.UTC)},
		{name: "whole second", at: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)},
		{name: "before epoch", at: time.Date(1969, 12, 31, 23, 59, 59, 500000000, time.UTC)},
		{name: "non-UTC input", at: time.Date(2026, 3, 4, 5, 6, 7, 1000, time.FixedZone("x", 3600))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := &models.AuditCursor{CreatedAt: tt.at, ID: "0b7e3a52-9c1d-4f5e-8a60-1234567890ab"}

			out, err := decodeAuditCursor(encodeAuditCursor(in))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			if !out.CreatedAt.Equal(in.CreatedAt) || out.ID != in.ID {
				t.Errorf("round trip = %+v, want %+v", out, in)
			}
		})
	}
}

func TestDecodeAuditCursorRejectsGarbage(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "not base64", in: "!!!"},
		{name: "no separator", in: enc("12345")},
		{name: "non numeric time", in: enc("abc:0b7e3a52-9c1d-4f5e-8a60-1234567890ab")},
		{name: "id is not a uuid", in: enc("1:not-a-uuid")},
		{name: "sql in id", in: enc("1:' OR 1=1 --")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if c, err := decodeAuditCursor(tt.in); err == nil {
				t.Errorf("decodeAuditCursor(%q) = %+v, want error", tt.in, c)
			}
		})
	}
}

func TestDecodeAuditCursorRejectsOutOfRangeTimestamp(t *testing.T) {
	const id = "33333333-3333-4333-8333-333333333333"

	for _, us := range []int64{maxAuditCursorMicros + 1, minAuditCursorMicros - 1, math.MaxInt64, math.MinInt64} {
		tok := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(us, 10) + ":" + id))
		if _, err := decodeAuditCursor(tok); !errors.Is(err, errInvalidAuditCursor) {
			t.Errorf("micros %d: err = %v", us, err)
		}
	}
}
