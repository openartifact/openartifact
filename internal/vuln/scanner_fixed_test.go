// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"encoding/json"
	"testing"
)

func TestExtractFixedVersionFromOSV(t *testing.T) {
	const advisory = `[
		{"package":{"name":"other","ecosystem":"npm"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"0.0.1"}]}]},
		{"package":{"name":"lib","ecosystem":"npm"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"0"},{"fixed":"1.2.5"}]}]},
		{"package":{"name":"lib","ecosystem":"npm"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"2.0.0"},{"fixed":"2.3.1"}]}]},
		{"package":{"name":"lib","ecosystem":"npm"},"ranges":[{"type":"ECOSYSTEM","events":[{"introduced":"2.0.0"},{"fixed":"2.4.0"}]}]}
	]`

	var affected []osvAffected
	if err := json.Unmarshal([]byte(advisory), &affected); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, pkg, version, want string
	}{
		{"old branch", "lib", "1.0.0", "1.2.5"},
		{"new branch skips older fixes", "lib", "2.1.0", "2.3.1"},
		{"already fixed", "lib", "3.0.0", ""},
		{"other package ignored", "unknown", "1.0.0", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractFixedVersionFromOSV(affected, "npm", tt.pkg, tt.version); got != tt.want {
				t.Errorf("fixed = %q, want %q", got, tt.want)
			}
		})
	}
}
