// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import (
	"net/netip"
	"testing"
)

func TestBlockedIPCloudMetadata(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")

	for _, a := range []string{"168.63.129.16", "100.100.100.200", "0.1.2.3", "169.254.169.254"} {
		if !blockedIP(netip.MustParseAddr(a)) {
			t.Errorf("%s must stay blocked even with private upstreams allowed", a)
		}
	}

	if blockedIP(netip.MustParseAddr("10.1.2.3")) {
		t.Error("10.1.2.3 should be allowed when private upstreams are allowed")
	}
}

func TestParseNetworks(t *testing.T) {
	nets, err := ParseNetworks([]string{"203.0.113.5", " 10.0.0.0/8 ", "", "2001:db8::1"})
	if err != nil || len(nets) != 3 {
		t.Fatalf("got %v, %v", nets, err)
	}

	if !nets[0].Contains(netip.MustParseAddr("203.0.113.5").AsSlice()) {
		t.Error("bare IP must match itself")
	}

	if _, err := ParseNetworks([]string{"10.0.0.0/8", "bogus"}); err == nil {
		t.Error("unparsable entry must be an error")
	}
}
