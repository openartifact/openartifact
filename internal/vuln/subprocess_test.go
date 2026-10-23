// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package vuln

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRunCappedCollectsOutputUnderLimit(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	out, stderr, err := runCapped(context.Background(), 1024, 1024, nil, "sh", "-c", "printf hello; printf oops >&2")
	if err != nil {
		t.Fatalf("runCapped: %v", err)
	}

	if string(out) != "hello" || stderr != "oops" {
		t.Errorf("out=%q stderr=%q", out, stderr)
	}
}

func TestRunCappedKillsRunawayStdout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	start := time.Now()

	// `yes` never stops on its own: the cap must kill it.
	out, _, err := runCapped(context.Background(), 4096, 1024, nil, "sh", "-c", "yes")
	if !errors.Is(err, ErrScannerOutputTooLarge) {
		t.Fatalf("err = %v, want ErrScannerOutputTooLarge", err)
	}

	if out != nil {
		t.Errorf("a truncated report must not be returned (%d bytes)", len(out))
	}

	if time.Since(start) > 10*time.Second {
		t.Errorf("runaway scanner was not stopped promptly: %v", time.Since(start))
	}
}

func TestRunCappedTruncatesStderr(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	_, stderr, err := runCapped(context.Background(), 1024, 16, nil, "sh", "-c", "printf '%0200d' 0 >&2; printf ok")
	if err != nil {
		t.Fatalf("runCapped: %v", err)
	}

	if len(stderr) != 16 || strings.Trim(stderr, "0") != "" {
		t.Errorf("stderr = %q, want 16 truncated bytes", stderr)
	}
}
