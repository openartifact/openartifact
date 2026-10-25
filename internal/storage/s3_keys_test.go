// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package storage

import (
	"testing"
	"time"
)

func TestObjectKey(t *testing.T) {
	sha := "aabbccdd1234567890abcdef1234567890abcdef1234567890abcdef12345678"
	want := "blobs/aa/bb/" + sha

	got := objectKey(sha)
	if got != want {
		t.Errorf("objectKey(%q) = %q, want %q", sha, got, want)
	}
}

func TestClampTTL(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{10 * time.Second, time.Minute},
		{30 * time.Minute, 30 * time.Minute},
		{2 * time.Hour, time.Hour},
		{time.Minute, time.Minute},
		{time.Hour, time.Hour},
	}
	for _, tc := range cases {
		got := clampTTL(tc.in)
		if got != tc.want {
			t.Errorf("clampTTL(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
