// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"strings"
	"testing"
)

func TestValidateAIArtifactIdentity(t *testing.T) {
	long := strings.Repeat("x", aiMaxNameLen+1)

	tests := []struct {
		name, format, artName, version string
		wantErr                        bool
	}{
		{"empty is fine", "", "", "", false},
		{"go is the repository format id", "go", "example.com/m", "v1.0.0", false},
		{"gomod is not a format id", "gomod", "", "", true},
		{"unknown format", "rpmx", "", "", true},
		{"name too long", "npm", long, "", true},
		{"version too long", "npm", "", long, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidateAIArtifactIdentity(tc.format, tc.artName, tc.version); (got != "") != tc.wantErr {
				t.Errorf("ValidateAIArtifactIdentity = %q, wantErr %v", got, tc.wantErr)
			}
		})
	}
}
