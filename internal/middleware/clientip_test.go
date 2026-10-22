// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newReqWithHeaders(remoteAddr string, headers map[string]string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	r.RemoteAddr = remoteAddr

	for k, v := range headers {
		r.Header.Set(k, v)
	}

	return r
}

func TestClientIP_NoTrustedProxies_IgnoresSpoofedHeaders(t *testing.T) {
	trusted := NewTrustedProxies(nil)

	r := newReqWithHeaders("203.0.113.5:12345", map[string]string{
		"X-Forwarded-For": "1.2.3.4",
		"X-Real-IP":       "5.6.7.8",
	})

	if got := ClientIP(r, trusted); got != "203.0.113.5" {
		t.Errorf("expected RemoteAddr host when no proxy is trusted, got %q", got)
	}
}

func TestClientIP_UntrustedPeer_IgnoresHeaders(t *testing.T) {
	trusted := NewTrustedProxies([]string{"10.0.0.0/8"})

	// Peer is NOT in the trusted range — headers must be ignored even though
	// a trusted-proxies list is configured.
	r := newReqWithHeaders("203.0.113.5:12345", map[string]string{
		"X-Forwarded-For": "1.2.3.4",
	})

	if got := ClientIP(r, trusted); got != "203.0.113.5" {
		t.Errorf("expected RemoteAddr host from untrusted peer, got %q", got)
	}
}

func TestClientIP_TrustedPeer_HonorsForwardedFor(t *testing.T) {
	trusted := NewTrustedProxies([]string{"10.0.0.0/8"})

	r := newReqWithHeaders("10.1.2.3:54321", map[string]string{
		"X-Forwarded-For": "1.2.3.4, 10.1.2.3",
	})

	if got := ClientIP(r, trusted); got != "1.2.3.4" {
		t.Errorf("expected first untrusted XFF hop from trusted peer, got %q", got)
	}
}

func TestClientIP_SpoofedLeftmostXFFIgnored(t *testing.T) {
	trusted := NewTrustedProxies([]string{"10.0.0.0/8"})

	// Attacker 198.51.100.7 sends XFF "8.8.8.8"; the proxy appends the attacker.
	r := newReqWithHeaders("10.1.2.3:54321", map[string]string{
		"X-Forwarded-For": "8.8.8.8, 198.51.100.7, 10.9.9.9",
	})

	if got := ClientIP(r, trusted); got != "198.51.100.7" {
		t.Errorf("expected rightmost untrusted hop, got %q", got)
	}
}

func TestClientIP_TrustedPeer_FallsBackToXRealIP(t *testing.T) {
	trusted := NewTrustedProxies([]string{"10.0.0.0/8"})

	r := newReqWithHeaders("10.1.2.3:54321", map[string]string{
		"X-Real-IP": "9.9.9.9",
	})

	if got := ClientIP(r, trusted); got != "9.9.9.9" {
		t.Errorf("expected X-Real-IP from trusted peer, got %q", got)
	}
}

func TestClientIP_TrustedPeer_NoHeaders_FallsBackToPeer(t *testing.T) {
	trusted := NewTrustedProxies([]string{"10.0.0.0/8"})

	r := newReqWithHeaders("10.1.2.3:54321", nil)

	if got := ClientIP(r, trusted); got != "10.1.2.3" {
		t.Errorf("expected peer address when no forwarding headers present, got %q", got)
	}
}

func TestClientIP_NilTrustedProxies(t *testing.T) {
	r := newReqWithHeaders("203.0.113.5:12345", map[string]string{
		"X-Forwarded-For": "1.2.3.4",
	})

	if got := ClientIP(r, nil); got != "203.0.113.5" {
		t.Errorf("expected RemoteAddr host with nil trusted proxies, got %q", got)
	}
}

func TestNewTrustedProxies_SkipsInvalidAndBlankEntries(t *testing.T) {
	trusted := NewTrustedProxies([]string{"", "not-a-cidr", "  ", "10.0.0.0/8"})

	if len(trusted.networks) != 1 {
		t.Fatalf("expected exactly 1 valid network, got %d", len(trusted.networks))
	}
}
