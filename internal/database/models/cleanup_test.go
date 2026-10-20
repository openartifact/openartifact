// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package models

import (
	"errors"
	"testing"
)

func TestCleanupPolicyValidate(t *testing.T) {
	cases := []struct {
		name string
		p    CleanupPolicy
		ok   bool
	}{
		{"keep last n", CleanupPolicy{Name: "p", KeepLastN: new(3)}, true},
		{"keep since", CleanupPolicy{Name: "p", KeepSinceDays: new(30), ScheduleCron: "0 3 * * 1"}, true},
		{"both + pattern", CleanupPolicy{Name: "p", KeepLastN: new(1), KeepSinceDays: new(1), NamePattern: `^foo-.*$`}, true},
		{"no name", CleanupPolicy{KeepLastN: new(3)}, false},
		{"no criteria", CleanupPolicy{Name: "p", NamePattern: ".*"}, false},
		{"zero keep_last_n", CleanupPolicy{Name: "p", KeepLastN: new(0)}, false},
		{"negative keep_last_n", CleanupPolicy{Name: "p", KeepLastN: new(-1)}, false},
		{"zero keep_since_days", CleanupPolicy{Name: "p", KeepSinceDays: new(0)}, false},
		{"negative keep_since_days", CleanupPolicy{Name: "p", KeepSinceDays: new(-5)}, false},
		{"bad regex", CleanupPolicy{Name: "p", KeepLastN: new(1), NamePattern: "("}, false},
		{"bad cron", CleanupPolicy{Name: "p", KeepLastN: new(1), ScheduleCron: "every day"}, false},
		{"downloaded unsupported", CleanupPolicy{Name: "p", KeepLastN: new(1), KeepDownloadedSinceDays: new(7)}, false},
	}

	for _, c := range cases {
		err := c.p.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
		}

		if !c.ok && !errors.Is(err, ErrInvalidCleanupPolicy) {
			t.Errorf("%s: expected ErrInvalidCleanupPolicy, got %v", c.name, err)
		}
	}
}
