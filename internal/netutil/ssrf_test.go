// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import "testing"

func TestValidateOutboundURL(t *testing.T) {
	for _, bad := range []string{"http://169.254.169.254/x", "http://127.0.0.1/", "http://10.0.0.1/", "http://[::1]/", "file:///etc/passwd", "ftp://example.com", "http:///x"} {
		if err := ValidateOutboundURLContext(t.Context(), bad); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
	if err := ValidateOutboundURLContext(t.Context(), "https://1.1.1.1/x"); err != nil {
		t.Errorf("public IP rejected: %v", err)
	}
	t.Setenv(EnvAllowPrivate, "true")
	if err := ValidateOutboundURLContext(t.Context(), "http://127.0.0.1/"); err != nil {
		t.Errorf("loopback should be allowed with opt-in: %v", err)
	}
	if err := ValidateOutboundURLContext(t.Context(), "http://169.254.169.254/"); err == nil {
		t.Error("metadata must stay blocked")
	}
}
