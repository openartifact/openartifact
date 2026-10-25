// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package delta provides binary diff generation and application for package files.
// It wraps the bsdiff algorithm (github.com/totallygamerjet/bsdiff) with
// repository-aware helpers for generating and serving package deltas.
//
// A delta is a compact binary patch that transforms one version of a package
// into another. Clients that already have version N can download a delta
// instead of the full package for version N+1, saving significant bandwidth.
//
// Example savings: a 50MB browser package update may produce a 2-5MB delta.
package delta

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/totallygamerjet/bsdiff"
)

// Sentinel errors for delta operations.
var (
	ErrInputTooLarge = errors.New("input too large for delta generation")
)

// MaxInputSize is the largest old/new input (bytes) Generate accepts. bsdiff
// needs several times the input size in memory, so this bounds worst-case use.
const MaxInputSize = 64 << 20

// Generate computes a binary diff patch from oldData to newData using bsdiff.
// The returned patch can be applied to oldData to reconstruct newData exactly.
// Returns the patch bytes and its SHA-256 hex digest.
func Generate(oldData, newData []byte) ([]byte, string, error) {
	if len(oldData) > MaxInputSize || len(newData) > MaxInputSize {
		return nil, "", ErrInputTooLarge
	}

	patch, err := bsdiff.Diff(oldData, newData)
	if err != nil {
		return nil, "", fmt.Errorf("delta: generate: %w", err)
	}

	sum := sha256.Sum256(patch)

	return patch, hex.EncodeToString(sum[:]), nil
}
