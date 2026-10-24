// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package federation

import "errors"

var (
	// ErrUnsupportedFormat means the repository format has no federation codec.
	ErrUnsupportedFormat = errors.New("federation does not support this repository format")
	// ErrNotFederated means the repository is not federated.
	ErrNotFederated = errors.New("repository is not federated")
	// ErrNotFound means a peer, token or unit does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the request conflicts with existing state (duplicate peer name).
	ErrConflict = errors.New("conflict")
	// ErrStale means an incoming change lost against a newer local stamp; the
	// engine drops it.
	ErrStale = errors.New("stale unit")
	// ErrQuarantined means the origin refuses to serve the unit (HTTP 423).
	ErrQuarantined = errors.New("unit is quarantined at the origin")
	// ErrInvalid means a request failed validation (message is client-safe).
	ErrInvalid = errors.New("invalid")

	errUnexpectedTransport = errors.New("federation: unexpected HTTP transport")
	errDockerNotIndexed    = errors.New("docker manifest is not indexed yet")
	errEngineNoLogger      = errors.New("federation: engine needs a logger")
	errDependencyDepth     = errors.New("dependency chain too deep")
	errDependencyDeleted   = errors.New("dependency was deleted locally")
	errDependencyMissing   = errors.New("dependency is not available at the peer")
	errBlobDigestMismatch  = errors.New("blob digest mismatch at the peer")
	errUnitKindMismatch    = errors.New("unit content kind differs from the log kind")
	errTableNoColumns      = errors.New("federation: table has no columns")
	errPullPanic           = errors.New("federation: pull panicked")
)
