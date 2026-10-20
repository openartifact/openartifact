// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package cron

import (
	"testing"
	"time"
)

func TestParseInvalid(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "a * * * *", "5-1 * * * *", "* * 0 * *", "* * * 13 *", "* * * * 8"} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) expected error", expr)
		}
	}
}

func TestNext(t *testing.T) {
	base := time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC) // Tuesday

	cases := []struct {
		expr string
		want time.Time
	}{
		{"0 2 * * *", time.Date(2026, 3, 11, 2, 0, 0, 0, time.UTC)},
		{"*/15 * * * *", time.Date(2026, 3, 10, 14, 45, 0, 0, time.UTC)},
		{"@hourly", time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC)},
		{"0 0 1 * *", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{"0 9 * * 1", time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)},
		{"0 9 * * 7", time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)},
		{"30 14 * * *", time.Date(2026, 3, 11, 14, 30, 0, 0, time.UTC)},
		{"0 0 29 2 *", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)},
		{"0 0 13 * 5", time.Date(2026, 3, 13, 0, 0, 0, 0, time.UTC)}, // dom OR dow
	}

	for _, c := range cases {
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.expr, err)
		}

		if got := s.Next(base); !got.Equal(c.want) {
			t.Errorf("Next(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}
