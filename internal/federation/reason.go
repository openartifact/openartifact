// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"

	"github.com/openartifact/openartifact/internal/netutil"
	"github.com/openartifact/openartifact/internal/quota"
	"github.com/openartifact/openartifact/internal/storage"
)

// The reasons stored in federation_peers.last_error and
// federation_deferred.last_error, and so returned by the API. The vocabulary is
// fixed: a failure is reduced to one of these (plus the HTTP status of the
// peer's answer when it gave one), never to text of the peer, the network stack
// or the database. The cause is logged instead.
const (
	ReasonUnreachable      = "unreachable"
	ReasonTimeout          = "timeout"
	ReasonTLS              = "tls"
	ReasonUnauthorized     = "unauthorized"
	ReasonNotFound         = "not_found"
	ReasonRateLimited      = "rate_limited"
	ReasonUpstreamError    = "upstream_error"
	ReasonProtocolMismatch = "protocol_mismatch"
	ReasonBlockedAddress   = "blocked_address"
	ReasonInvalidConfig    = "invalid_config"
	ReasonInvalidResponse  = "invalid_response"
	ReasonInvalidUnit      = "invalid_unit"
	ReasonQuarantined      = "quarantined"
	ReasonDependency       = "dependency_unavailable"
	ReasonBlobRejected     = "blob_rejected"
	ReasonBlobGone         = "blob_gone"
	ReasonClockSkew        = "clock_skew"
	ReasonQuotaExceeded    = "quota_exceeded"
	ReasonCanceled         = "canceled"
	ReasonInternal         = "internal_error"
)

// storedReasonRe is the shape Describe produces.
var storedReasonRe = regexp.MustCompile(`^[a-z_]+( \(HTTP [1-5][0-9]{2}\))?$`)

// Describe classifies a failure for storage and display: a reason of the fixed
// vocabulary, followed by "(HTTP <status>)" when a peer answered with a status.
func Describe(err error) string {
	if he, ok := errors.AsType[*HTTPError](err); ok {
		return withStatus(httpReason(he.Status), he.Status)
	}

	if errors.Is(err, ErrNotFound) {
		return withStatus(ReasonNotFound, http.StatusNotFound)
	}

	return classify(err)
}

func withStatus(reason string, status int) string {
	return reason + " (HTTP " + strconv.Itoa(status) + ")"
}

func httpReason(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ReasonUnauthorized
	case http.StatusNotFound:
		return ReasonNotFound
	case http.StatusTooManyRequests:
		return ReasonRateLimited
	}

	return ReasonUpstreamError
}

// StoredReason makes a stored last_error safe to return: values written by
// Describe pass through, anything else (rows written before the vocabulary
// existed hold raw upstream text) becomes the generic upstream_error.
func StoredReason(stored string) string {
	if stored == "" || storedReasonRe.MatchString(stored) {
		return stored
	}

	return ReasonUpstreamError
}

type classifier struct {
	reason string
	match  func(error) bool
}

//nolint:gochecknoglobals // read-only table, ordered by precedence
var classifiers = []classifier{
	{ReasonBlockedAddress, func(err error) bool { return errors.Is(err, netutil.ErrBlockedAddress) }},
	{ReasonCanceled, func(err error) bool { return errors.Is(err, context.Canceled) }},
	{ReasonTimeout, func(err error) bool { return errors.Is(err, context.DeadlineExceeded) || isTimeout(err) }},
	{ReasonUnreachable, func(err error) bool { return errors.Is(err, netutil.ErrCannotResolve) }},
	{ReasonInvalidConfig, func(err error) bool { return errors.Is(err, errPeerConfig) }},
	{ReasonProtocolMismatch, func(err error) bool { return errors.Is(err, errPeerMismatch) }},
	{ReasonTLS, isTLSError},
	{ReasonClockSkew, func(err error) bool { return errors.Is(err, errStampInvalid) }},
	{ReasonQuotaExceeded, func(err error) bool { return errors.Is(err, quota.ErrExceeded) }},
	{ReasonQuarantined, func(err error) bool { return errors.Is(err, ErrQuarantined) }},
	{ReasonBlobGone, func(err error) bool { return errors.Is(err, storage.ErrBlobGone) }},
	{ReasonBlobRejected, isBlobRejected},
	{ReasonDependency, isDependency},
	{ReasonUnreachable, isNetworkError},
	{ReasonInvalidResponse, isBadResponse},
	{ReasonInvalidUnit, func(err error) bool { return errors.Is(err, ErrInvalid) || errors.Is(err, errUnitKindMismatch) }},
	{ReasonUnreachable, func(err error) bool { return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) }},
}

func classify(err error) string {
	for _, c := range classifiers {
		if c.match(err) {
			return c.reason
		}
	}

	return ReasonInternal
}

func isBlobRejected(err error) bool {
	return errors.Is(err, errBlobDigestMismatch) || errors.Is(err, errBlobTooShort) || errors.Is(err, errBlobTooLong) ||
		errors.Is(err, errBlobSize) || errors.Is(err, errBlobTooLarge)
}

func isDependency(err error) bool {
	return errors.Is(err, errDependencyDepth) || errors.Is(err, errDependencyDeleted) || errors.Is(err, errDependencyMissing)
}

func isTimeout(err error) bool {
	ne, ok := errors.AsType[net.Error](err)

	return ok && ne.Timeout()
}

func isTLSError(err error) bool {
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return true
	}

	if _, ok := errors.AsType[tls.AlertError](err); ok {
		return true
	}

	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return true
	}

	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return true
	}

	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return true
	}

	_, ok := errors.AsType[x509.CertificateInvalidError](err)

	return ok
}

func isBadResponse(err error) bool {
	if errors.Is(err, errPeerResponse) || errors.Is(err, errRangeMismatch) || errors.Is(err, errBadContentRange) {
		return true
	}

	if _, ok := errors.AsType[*json.SyntaxError](err); ok {
		return true
	}

	_, ok := errors.AsType[*json.UnmarshalTypeError](err)

	return ok
}

func isNetworkError(err error) bool {
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}

	_, ok := errors.AsType[*streamError](err)

	return ok
}
