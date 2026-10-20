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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/totallygamerjet/bsdiff"
)

// Sentinel errors for delta operations.
var (
	ErrSHA256Mismatch = errors.New("sha256 mismatch")
	ErrInputTooLarge  = errors.New("input too large for delta generation")
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

// Apply reconstructs newData by applying patch to oldData.
// Returns an error if the patch is invalid or incompatible with oldData.
func Apply(oldData, patch []byte) ([]byte, error) {
	newData, err := bsdiff.Patch(oldData, patch)
	if err != nil {
		return nil, fmt.Errorf("delta: apply: %w", err)
	}

	return newData, nil
}

// GenerateFromReaders is a streaming variant of Generate that reads old and new
// package data from io.Reader instances. Both readers are fully consumed.
func GenerateFromReaders(ctx context.Context, oldReader, newReader io.Reader) ([]byte, string, error) {
	oldData, err := readAll(ctx, oldReader)
	if err != nil {
		return nil, "", fmt.Errorf("delta: read old: %w", err)
	}

	newData, err := readAll(ctx, newReader)
	if err != nil {
		return nil, "", fmt.Errorf("delta: read new: %w", err)
	}

	return Generate(oldData, newData)
}

// Verify checks that applying patch to oldData produces data with the expected
// SHA-256 digest. Use this to validate a delta before serving it to clients.
func Verify(oldData, patch []byte, expectedSHA256 string) error {
	newData, err := Apply(oldData, patch)
	if err != nil {
		return fmt.Errorf("delta: verify apply: %w", err)
	}

	sum := sha256.Sum256(newData)

	got := hex.EncodeToString(sum[:])
	if got != expectedSHA256 {
		return fmt.Errorf("delta: verify: %w: got %s, want %s", ErrSHA256Mismatch, got, expectedSHA256)
	}

	return nil
}

// readAll reads all bytes from r, respecting context cancellation.
func readAll(ctx context.Context, r io.Reader) ([]byte, error) {
	var buf bytes.Buffer

	done := make(chan error, 1)

	go func() {
		n, err := io.Copy(&buf, io.LimitReader(r, MaxInputSize+1))
		if err == nil && n > MaxInputSize {
			err = ErrInputTooLarge
		}

		done <- err
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-done:
		if err != nil {
			return nil, err
		}

		return buf.Bytes(), nil
	}
}
