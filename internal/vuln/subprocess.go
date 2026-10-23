// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
//
// The subprocess does not inherit the server's whole environment (database,
// storage and signing secrets stay out of it): see scannerEnv. env lists extra
// KEY=VALUE variables added to what it inherits (a later entry wins over an
// earlier one with the same key).
func runScanner(ctx context.Context, env []string, name string, args ...string) ([]byte, string, error) {
	return runCapped(ctx, maxScannerStdout, maxScannerStderr, env, name, args...)
}

func runCapped(ctx context.Context, stdoutLimit, stderrLimit int, env []string, name string, args ...string) ([]byte, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := &cappedBuffer{limit: stdoutLimit, failOnOverflow: true, onOverflow: cancel}
	errOut := &cappedBuffer{limit: stderrLimit}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = scannerEnv(os.Environ(), env)

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

// scannerEnvNames are the variables a scanner subprocess inherits from the
// server: where to find programs, a home and temp directory, locale, proxy and
// CA settings, and registry credentials (Docker's config location).
//
//nolint:gochecknoglobals // read-only allowlist
var scannerEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "TMP": true, "TEMP": true, "USER": true, "LOGNAME": true,
	"LANG": true, "LANGUAGE": true, "TZ": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true, "all_proxy": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	"XDG_CACHE_HOME": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
	"DOCKER_CONFIG": true,
}

// scannerEnv builds a scanner subprocess's environment from the server's
// (parent): only the allowlisted names, any LC_* locale variable and the
// scanners' own TRIVY_* and GRYPE_* settings, then extra on top.
func scannerEnv(parent, extra []string) []string {
	env := make([]string, 0, len(parent)+len(extra))

	for _, kv := range parent {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}

		if scannerEnvNames[name] || strings.HasPrefix(name, "LC_") ||
			strings.HasPrefix(name, "TRIVY_") || strings.HasPrefix(name, "GRYPE_") {
			env = append(env, kv)
		}
	}

	return append(env, extra...)
}
