// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckBuildFeatures(t *testing.T) {
	tests := []struct {
		name         string
		aiOn, mcpOn  bool // configured
		aiBuilt      bool
		mcpBuilt     bool
		want         error
		wantContains string
	}{
		{name: "everything compiled in", aiOn: true, mcpOn: true, aiBuilt: true, mcpBuilt: true},
		{name: "features off in a slim build", aiBuilt: false, mcpBuilt: false},
		{name: "ai on without ai", aiOn: true, aiBuilt: false, mcpBuilt: true, want: errAINotCompiled, wantContains: "-tags noai"},
		{name: "mcp on without mcp", mcpOn: true, aiBuilt: true, mcpBuilt: false, want: errMCPNotCompiled, wantContains: "-tags nomcp"},
		{name: "mcp on, ai on, only mcp missing", aiOn: true, mcpOn: true, aiBuilt: true, mcpBuilt: false, want: errMCPNotCompiled},
		{name: "ai off without ai is fine", mcpOn: true, aiBuilt: false, mcpBuilt: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{}
			c.AI.Enabled = tc.aiOn
			c.MCP.Enabled = tc.mcpOn

			err := checkBuildFeatures(c, tc.aiBuilt, tc.mcpBuilt)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}

			if tc.wantContains != "" && !strings.Contains(err.Error(), tc.wantContains) {
				t.Errorf("err = %q, want it to mention %q", err, tc.wantContains)
			}
		})
	}
}
