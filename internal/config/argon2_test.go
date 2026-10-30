// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"testing"
)

func TestArgon2MaxConcurrent(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    int
		wantErr error
	}{
		{"unset is automatic", "", 0, nil},
		{"explicit", "3", 3, nil},
		{"negative is refused", "-1", -1, errNegativeArgon2Concurrent},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("OPENARTIFACT_ARGON2_MAX_CONCURRENT", tc.env)
			}

			cfg, err := Load("")
			if err != nil {
				t.Fatal(err)
			}

			if cfg.Auth.Argon2MaxConcurrent != tc.want {
				t.Errorf("Argon2MaxConcurrent = %d, want %d", cfg.Auth.Argon2MaxConcurrent, tc.want)
			}

			if err := cfg.Validate(); !errors.Is(err, tc.wantErr) {
				t.Errorf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
