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

func TestBlockedIPRanges(t *testing.T) {
	blocked := []string{
		// always-blocked IPv4
		"0.0.0.0", "0.1.2.3", "192.0.0.1", "198.18.0.1", "198.19.255.255", "240.0.0.1", "255.255.255.255",
		"169.254.169.254", "168.63.129.16", "100.100.100.200", "224.0.0.1",
		// IPv6 basics
		"::", "ff02::1", "fe80::1", "100::1", "2001::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2", "fd00:ec2::254",
		// NAT64 / local-use NAT64 / 6to4 / IPv4-compatible / mapped carrying forbidden IPv4
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", "64:ff9b::a00:1",
		"64:ff9b:1:0:0:7f:0:1", "64:ff9b:1:0:0:a9:fe00:a9fe",
		"2002:7f00:1::", "2002:a9fe:a9fe::1", "2002:c0a8:101::",
		"::7f00:1", "::a9fe:a9fe", "::ffff:169.254.169.254", "::ffff:127.0.0.1",
	}
	for _, a := range blocked {
		if !blockedIP(netip.MustParseAddr(a)) {
			t.Errorf("%s must be blocked", a)
		}
	}

	allowed := []string{
		"1.1.1.1", "93.184.216.34", "192.0.2.1", "198.51.100.1", "203.0.113.1", "2001:db8::1",
		"2606:4700:4700::1111", "64:ff9b::101:101", "2002:101:101::1",
	}
	for _, a := range allowed {
		if blockedIP(netip.MustParseAddr(a)) {
			t.Errorf("%s must be allowed", a)
		}
	}
}

func TestBlockedIPRangesWithAllowPrivate(t *testing.T) {
	t.Setenv(EnvAllowPrivate, "true")

	for _, a := range []string{"0.1.2.3", "198.18.0.1", "240.0.0.1", "2001::1", "100::1", "64:ff9b::a9fe:a9fe", "2002:a9fe:a9fe::1"} {
		if !blockedIP(netip.MustParseAddr(a)) {
			t.Errorf("%s must stay blocked with private upstreams allowed", a)
		}
	}

	for _, a := range []string{"10.1.2.3", "127.0.0.1", "::1", "fc00::1", "100.64.0.1", "64:ff9b::a00:1", "2002:a00:1::"} {
		if blockedIP(netip.MustParseAddr(a)) {
			t.Errorf("%s must be allowed with private upstreams allowed", a)
		}
	}
}

func TestValidateOutboundURLLiteralRanges(t *testing.T) {
	for _, bad := range []string{
		"http://192.0.0.8/", "http://198.18.0.1/", "http://240.0.0.1/", "http://255.255.255.255/",
		"http://[64:ff9b::7f00:1]/", "http://[64:ff9b::a9fe:a9fe]/", "http://[2002:7f00:1::]/",
		"http://[2001::1]/", "http://[::7f00:1]/", "http://[fe80::1]/", "http://[fc00::1]/", "http://[100::1]/", "http://[ff02::1]/",
	} {
		if err := ValidateOutboundURL(bad); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}

	for _, ok := range []string{"http://192.0.2.1/", "http://198.51.100.1/", "http://203.0.113.1/", "http://[2001:db8::1]/"} {
		if err := ValidateOutboundURL(ok); err != nil {
			t.Errorf("%s should be allowed: %v", ok, err)
		}
	}
}
