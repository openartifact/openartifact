// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestTeeWritesBothAtTheLoggerLevel checks that the teed core receives the
// entries the logger writes and nothing below its level, that the output keeps
// every entry, and that the original logger is left alone.
func TestTeeWritesBothAtTheLoggerLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")

	l, err := New("info", "json", path)
	if err != nil {
		t.Fatal(err)
	}

	core, seen := observer.New(zapcore.DebugLevel)

	teed, err := l.Tee(core)
	if err != nil {
		t.Fatal(err)
	}

	teed.Debugw("below the level")
	teed.Infow("teed")
	l.Infow("original")

	if err := teed.Sync(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"teed"`) || !strings.Contains(lines[1], `"original"`) {
		t.Errorf("output = %q, want the teed and the original entry", lines)
	}

	if got := seen.All(); len(got) != 1 || got[0].Message != "teed" || got[0].Caller.File == "" {
		t.Errorf("teed core saw %v, want only the info entry, with its caller", got)
	}
}
