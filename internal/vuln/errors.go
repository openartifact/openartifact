// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
)

// errUnsupportedEcosystem marks an ecosystem a scanner has no way to scan.
var errScannerPanic = errors.New("scanner panic")

var errUnsupportedEcosystem = errors.New("unsupported ecosystem")

// FailureKind classifies why a scanner could not produce a result. The kind is
// what reaches API clients (through PublicMessage); the wrapped error stays in
// the server log.
type FailureKind string

const (
	// FailureUnavailable: the scanner binary is missing or cannot be started.
	FailureUnavailable FailureKind = "unavailable"
	// FailureTimeout: the scan hit its deadline.
	FailureTimeout FailureKind = "timeout"
	// FailureCanceled: the scan was canceled before it finished.
	FailureCanceled FailureKind = "canceled"
	// FailureExecution: the scanner ran and exited with an error.
	FailureExecution FailureKind = "execution_failed"
	// FailureInvalidOutput: the scanner printed output that could not be parsed.
	FailureInvalidOutput FailureKind = "invalid_output"
	// FailureOutputTooLarge: the scanner printed more than the size limit.
	FailureOutputTooLarge FailureKind = "output_too_large"
	// FailureUnsupported: the scanner cannot scan this ecosystem.
	FailureUnsupported FailureKind = "unsupported_ecosystem"
	// FailureUpstream: the vulnerability database could not be reached or
	// answered with an error.
	FailureUpstream FailureKind = "upstream_error"
	// FailureDatabase: the scanner's vulnerability database is missing, not yet
	// downloaded, unreadable, or cannot be written.
	FailureDatabase FailureKind = "database_unavailable"
	// FailureInternal: anything else.
	FailureInternal FailureKind = "internal"
)

// ScanError is a scanner failure. Error() carries the full detail for server
// logs; PublicMessage() is the only text safe to hand to API clients.
type ScanError struct {
	Scanner string
	Kind    FailureKind
	// Err is the underlying cause (command line, stderr, upstream status, ...).
	// It must never be shown to clients.
	Err error
}

func (e *ScanError) Error() string {
	msg := e.Scanner + " scan failed: " + string(e.Kind)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}

	return msg
}

func (e *ScanError) Unwrap() error { return e.Err }

// PublicMessage returns a fixed, client-safe description of the failure.
func (e *ScanError) PublicMessage() string {
	switch e.Kind {
	case FailureUnavailable:
		return "scanner is not available on the server (" + CodeScannerUnavailable + "), setup guide: " + DocsURL
	case FailureDatabase:
		return "scanner vulnerability database is not ready (" + CodeScannerDatabase + "), setup guide: " + DocsURL
	case FailureTimeout:
		return "scan timed out"
	case FailureCanceled:
		return "scan was canceled"
	case FailureExecution:
		return "scanner exited with an error"
	case FailureInvalidOutput:
		return "scanner output could not be parsed"
	case FailureOutputTooLarge:
		return "scanner output exceeded the size limit"
	case FailureUnsupported:
		return "ecosystem is not supported by this scanner"
	case FailureUpstream:
		return "vulnerability database request failed"
	case FailureInternal:
		return "scan failed"
	}

	return "scan failed"
}

func newScanError(scanner string, kind FailureKind, err error) *ScanError {
	return &ScanError{Scanner: scanner, Kind: kind, Err: err}
}

// classify turns any error returned by a backend into a *ScanError. A backend
// that already returned one keeps its kind; context errors and a missing
// binary are recognised; everything else is FailureInternal.
func classify(scanner string, err error) *ScanError {
	if se, ok := errors.AsType[*ScanError](err); ok {
		return se
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return newScanError(scanner, FailureTimeout, err)
	case errors.Is(err, context.Canceled):
		return newScanError(scanner, FailureCanceled, err)
	case errors.Is(err, exec.ErrNotFound):
		return newScanError(scanner, FailureUnavailable, err)
	case errors.Is(err, ErrScannerOutputTooLarge):
		return newScanError(scanner, FailureOutputTooLarge, err)
	}

	return newScanError(scanner, FailureInternal, err)
}

// ctxFailure maps an expired scan context onto its failure kind.
func ctxFailure(scanner string, ctxErr error) *ScanError {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return newScanError(scanner, FailureTimeout, ctxErr)
	}

	return newScanError(scanner, FailureCanceled, ctxErr)
}

// stderrMentionsDatabase reports whether a scanner's error output blames its
// vulnerability database (corrupt, unreadable, unwritable cache).
func stderrMentionsDatabase(stderr string) bool {
	s := strings.ToLower(stderr)

	for _, marker := range []string{"vulnerability db", "vulnerability database", "db error", "failed to open db", "trivy.db"} {
		if strings.Contains(s, marker) {
			return true
		}
	}

	return false
}

// runFailure classifies an error from runScanner for the named scanner.
func runFailure(ctx context.Context, scanner string, err error, stderr string) *ScanError {
	if errors.Is(err, ErrScannerOutputTooLarge) {
		return newScanError(scanner, FailureOutputTooLarge, err)
	}

	if ctx.Err() != nil {
		return ctxFailure(scanner, ctx.Err())
	}

	if errors.Is(err, exec.ErrNotFound) {
		return newScanError(scanner, FailureUnavailable, err)
	}

	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		if stderrMentionsDatabase(stderr) {
			return newScanError(scanner, FailureDatabase, err)
		}

		return newScanError(scanner, FailureExecution, err)
	}

	// Start failures other than a missing binary (permissions, exec format).
	var (
		execErr *exec.Error
		pathErr *fs.PathError
	)

	if errors.As(err, &execErr) || errors.As(err, &pathErr) {
		return newScanError(scanner, FailureUnavailable, err)
	}

	return newScanError(scanner, FailureExecution, err)
}
