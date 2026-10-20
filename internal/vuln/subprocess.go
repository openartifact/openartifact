// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

const (
	// maxScannerStdout caps the JSON a scanner subprocess may print. Trivy and
	// Grype reports for a single package are far below this; the cap exists so
	// a misbehaving scanner (or a hostile package description) cannot exhaust
	// server memory.
	maxScannerStdout = 32 << 20
	// maxScannerStderr caps the captured stderr, which is only logged.
	maxScannerStderr = 64 << 10
)

// ErrScannerOutputTooLarge is returned when a scanner subprocess prints more
// than maxScannerStdout bytes. The subprocess is killed and the scan fails: a
// truncated report must never be mistaken for "no vulnerabilities".
var ErrScannerOutputTooLarge = errors.New("scanner output exceeds size limit")

// cappedBuffer collects up to limit bytes. Beyond the limit it either fails the
// write (failOnOverflow, used for stdout) or silently drops the excess (stderr).
type cappedBuffer struct {
	buf            bytes.Buffer
	limit          int
	failOnOverflow bool
	overflowed     bool
	onOverflow     func()
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()

	if len(p) <= room {
		return c.buf.Write(p)
	}

	if room > 0 {
		c.buf.Write(p[:room])
	}

	if !c.failOnOverflow {
		return len(p), nil
	}

	if !c.overflowed {
		c.overflowed = true

		if c.onOverflow != nil {
			c.onOverflow()
		}
	}

	return room, ErrScannerOutputTooLarge
}

// runScanner runs a scanner binary and returns its bounded stdout and stderr.
// Stdout beyond maxScannerStdout kills the process and yields
// ErrScannerOutputTooLarge; stderr beyond maxScannerStderr is truncated.
func runScanner(ctx context.Context, name string, args ...string) ([]byte, string, error) {
	return runCapped(ctx, maxScannerStdout, maxScannerStderr, name, args...)
}

func runCapped(ctx context.Context, stdoutLimit, stderrLimit int, name string, args ...string) ([]byte, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := &cappedBuffer{limit: stdoutLimit, failOnOverflow: true, onOverflow: cancel}
	errOut := &cappedBuffer{limit: stderrLimit}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = out
	cmd.Stderr = errOut

	runErr := cmd.Run()
	if out.overflowed {
		return nil, errOut.buf.String(), fmt.Errorf("%s: %w (limit %d bytes)", name, ErrScannerOutputTooLarge, stdoutLimit)
	}

	if runErr != nil {
		return nil, errOut.buf.String(), fmt.Errorf("run %s: %w", name, runErr)
	}

	return out.buf.Bytes(), errOut.buf.String(), nil
}
