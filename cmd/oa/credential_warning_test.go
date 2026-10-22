// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWarnCredentialExposure(t *testing.T) {
	tests := []struct {
		name, server, token string
		flagSet             bool
		want                []string
	}{
		{"no token", "http://example.com", "", true, nil},
		{"env token https", "https://example.com", "t", false, nil},
		{"loopback http", "http://localhost:8080", "t", false, nil},
		{"remote http", "http://example.com", "t", false, []string{"plain http"}},
		{"flag token", "https://example.com", "t", true, []string{"--token"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer

			warnCredentialExposure(&buf, tt.server, tt.token, tt.flagSet)

			if len(tt.want) == 0 && buf.Len() != 0 {
				t.Fatalf("unexpected output %q", buf.String())
			}

			for _, w := range tt.want {
				if !strings.Contains(buf.String(), w) {
					t.Fatalf("output %q missing %q", buf.String(), w)
				}
			}
		})
	}
}
