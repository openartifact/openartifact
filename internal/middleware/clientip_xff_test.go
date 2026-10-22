// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"net/http"
	"testing"
)

// A proxy that appends its own X-Forwarded-For line (instead of extending the
// existing one) leaves the client-sent line first: the rightmost line is the
// one the trusted proxy wrote.
func TestClientIP_MultipleXFFLinesUsesRightmostLine(t *testing.T) {
	trusted := NewTrustedProxies([]string{"10.0.0.0/8"})

	r := newReqWithHeaders("10.0.0.1:1234", nil)
	r.Header.Add("X-Forwarded-For", "198.51.100.77") // client-forged
	r.Header.Add("X-Forwarded-For", "203.0.113.9")   // appended by the proxy

	if got := ClientIP(r, trusted); got != "203.0.113.9" {
		t.Fatalf("ClientIP = %q, want the proxy-appended hop 203.0.113.9", got)
	}
}

func TestClientIP_UnparseableHopFallsBackToPeer(t *testing.T) {
	trusted := NewTrustedProxies([]string{"10.0.0.0/8"})

	r := newReqWithHeaders("10.0.0.1:1234", nil)
	r.Header.Add("X-Forwarded-For", "198.51.100.77")
	r.Header.Add("X-Forwarded-For", "garbage")

	if got := ClientIP(r, trusted); got != "10.0.0.1" {
		t.Fatalf("ClientIP = %q, want the peer 10.0.0.1 (an unattributable hop must not defer to client-supplied hops)", got)
	}
}

func TestLimiterKeyCollapsesIPv6To64(t *testing.T) {
	a := LimiterKey("2001:db8:1:2:aaaa:bbbb:cccc:dddd")
	b := LimiterKey("2001:db8:1:2:1111:2222:3333:4444")

	if a != b {
		t.Fatalf("addresses in one /64 got different keys: %q vs %q", a, b)
	}

	if other := LimiterKey("2001:db8:1:3::1"); other == a {
		t.Fatalf("different /64s share key %q", a)
	}

	if got := LimiterKey("::ffff:192.0.2.7"); got != "192.0.2.7" {
		t.Fatalf("IPv4-mapped = %q, want 192.0.2.7", got)
	}

	if got := LimiterKey("192.0.2.7"); got != "192.0.2.7" {
		t.Fatalf("IPv4 = %q", got)
	}
}

func TestMethodLabelBoundsCardinality(t *testing.T) {
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		if got := methodLabel(m); got != m {
			t.Errorf("methodLabel(%q) = %q", m, got)
		}
	}

	for _, m := range []string{"A1", "PROPFIND", "get", ""} {
		if got := methodLabel(m); got != "OTHER" {
			t.Errorf("methodLabel(%q) = %q, want OTHER", m, got)
		}
	}
}
