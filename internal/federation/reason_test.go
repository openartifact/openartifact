// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/quota"
)

func TestDescribeUsesTheFixedVocabulary(t *testing.T) {
	t.Parallel()

	secret := "internal.example.corp password=hunter2"

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"peer 500", &HTTPError{Status: 500, Body: secret}, "upstream_error (HTTP 500)"},
		{"peer 401", &HTTPError{Status: 401, Body: secret}, "unauthorized (HTTP 401)"},
		{"peer 403", &HTTPError{Status: 403, Body: secret}, "unauthorized (HTTP 403)"},
		{"peer 404", &HTTPError{Status: 404, Body: secret}, "not_found (HTTP 404)"},
		{"peer 429", &HTTPError{Status: 429, Body: secret}, "rate_limited (HTTP 429)"},
		{"not found sentinel", fmt.Errorf("%w: %s", ErrNotFound, secret), "not_found (HTTP 404)"},
		{"wrapped status", fmt.Errorf("pull: %w", &HTTPError{Status: 502, Body: secret}), "upstream_error (HTTP 502)"},
		{"deadline", fmt.Errorf("get %s: %w", secret, context.DeadlineExceeded), "timeout"},
		{"net timeout", &net.OpError{Op: "dial", Err: timeoutError{}}, "timeout"},
		{"refused", &net.OpError{Op: "dial", Err: errors.New(secret)}, "unreachable"},
		{"eof", fmt.Errorf("read %s: %w", secret, io.ErrUnexpectedEOF), "unreachable"},
		{"unknown authority", x509.UnknownAuthorityError{}, "tls"},
		{"hostname", x509.HostnameError{Host: secret}, "tls"},
		{"alert", tls.AlertError(42), "tls"},
		{"blocked", fmt.Errorf("%w: %s", netutil.ErrBlockedAddress, secret), "blocked_address"},
		{"bad config", fmt.Errorf("%w: %w: %s", errPeerConfig, ErrInvalid, secret), "invalid_config"},
		{"mismatch", fmt.Errorf("%w: %s", errPeerMismatch, secret), "protocol_mismatch"},
		{"bad json", fmt.Errorf("%w: decode %s: %w", errPeerResponse, secret, io.EOF), "invalid_response"},
		{"clock", deferred("clock_skew", fmt.Errorf("%w: %s", errStampInvalid, secret)), "clock_skew"},
		{"quota", deferred("quota_exceeded", fmt.Errorf("%w: %s", quota.ErrExceeded, secret)), "quota_exceeded"},
		{"digest", deferred("digest_mismatch", fmt.Errorf("%w: %s", errBlobDigestMismatch, secret)), "blob_rejected"},
		{"too long", deferred("digest_mismatch", fmt.Errorf("%w: %s", errBlobTooLong, secret)), "blob_rejected"},
		{"unit invalid", fmt.Errorf("%w: %s", ErrInvalid, secret), "invalid_unit"},
		{"quarantined", deferred("quarantined", ErrQuarantined), "quarantined"},
		{"anything else", errors.New(secret), "internal_error"},
		{"panic", fmt.Errorf("%w in codec: %s", errPullPanic, secret), "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Describe(tt.err)
			if got != tt.want {
				t.Fatalf("Describe = %q, want %q", got, tt.want)
			}

			if strings.Contains(got, "hunter2") || strings.Contains(got, "corp") {
				t.Fatalf("Describe leaked the cause: %q", got)
			}

			if StoredReason(got) != got {
				t.Fatalf("StoredReason(%q) = %q, want it unchanged", got, StoredReason(got))
			}
		})
	}
}

func TestStoredReasonHidesTextWrittenBeforeTheVocabulary(t *testing.T) {
	t.Parallel()

	tests := []struct{ stored, want string }{
		{"", ""},
		{"unreachable", "unreachable"},
		{"upstream_error (HTTP 500)", "upstream_error (HTTP 500)"},
		{`peer answered 500: {"error":"db password=hunter2"}`, ReasonUpstreamError},
		{"dial tcp 10.1.2.3:5432: connect: connection refused", ReasonUpstreamError},
		{"upstream_error (HTTP 500) and more", ReasonUpstreamError},
		{"UPSTREAM", ReasonUpstreamError},
	}

	for _, tt := range tests {
		if got := StoredReason(tt.stored); got != tt.want {
			t.Errorf("StoredReason(%q) = %q, want %q", tt.stored, got, tt.want)
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return false }
