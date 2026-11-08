// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"testing"
)

func TestQuayAPIConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		orgs    []string
		wantErr bool
	}{
		{"none allows every organization", nil, false},
		{"plain names", []string{"konflux", "team-a_1"}, false},
		{"slash", []string{"a/b"}, true},
		{"empty", []string{""}, true},
		{"leading dash", []string{"-a"}, true},
		{"space", []string{"a b"}, true},
		{"one bad among good", []string{"konflux", "bad name"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := QuayAPIConfig{Enabled: true, Organizations: tc.orgs}.validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate() = %v, wantErr %v", err, tc.wantErr)
			}

			if tc.wantErr && !errors.Is(err, errQuayOrgInvalid) {
				t.Errorf("validate() = %v, want errQuayOrgInvalid", err)
			}
		})
	}
}

func TestQuayAPIDisabledByDefault(t *testing.T) {
	if defaultQuayAPIConfig().Enabled {
		t.Error("the Quay-compatible API must be off by default")
	}
}
