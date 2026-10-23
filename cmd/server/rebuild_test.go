// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openartifact/openartifact/internal/config"
	"github.com/openartifact/openartifact/internal/recovery"
	"github.com/openartifact/openartifact/internal/storage"
)

func TestParseRebuildFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(*rebuildFlags) bool
	}{
		{"defaults", nil, false, func(f *rebuildFlags) bool { return f.source == "primary" && f.concurrency == 8 && !f.dryRun }},
		{"all flags", []string{"--dry-run", "--deep", "--prune-superseded", "--json", "--source", "replica", "--concurrency", "3", "--report", "r.json"}, false,
			func(f *rebuildFlags) bool {
				return f.dryRun && f.deep && f.pruneSuperseded && f.json && f.source == "replica" && f.concurrency == 3 && f.report == "r.json"
			}},
		{"concurrency is clamped", []string{"--concurrency", "0"}, false, func(f *rebuildFlags) bool { return f.concurrency == 1 }},
		{"positional argument", []string{"extra"}, true, nil},
		{"unknown flag", []string{"--nope"}, true, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, err := parseRebuildFlags(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%t", err, tc.wantErr)
			}

			if err == nil && !tc.check(f) {
				t.Fatalf("unexpected flags: %+v", f)
			}
		})
	}
}

func TestRebuildBlobStore(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Storage: storage.StorageConfig{Type: "filesystem", Path: t.TempDir()}}

	tests := []struct {
		source  string
		wantErr error
	}{
		{"primary", nil},
		{"replica", errNoReplica},
		{"bogus", errRebuildSource},
	}

	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			t.Parallel()

			store, err := rebuildBlobStore(cfg, tc.source)
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil) != (store != nil) {
				t.Fatalf("store=%v err=%v want %v", store, err, tc.wantErr)
			}
		})
	}

	if !strings.Contains(errNoReplica.Error(), "storage.replica is not configured") {
		t.Fatalf("unexpected message %q", errNoReplica)
	}
}

func TestEmitReport(t *testing.T) {
	t.Parallel()

	rep := &recovery.Report{Source: "primary", DryRun: true}

	tests := []struct {
		name       string
		json       bool
		wantStdout func(string) bool
		wantStderr func(string) bool
	}{
		{"text summary on stdout", false,
			func(s string) bool { return strings.Contains(s, "dry run") },
			func(s string) bool { return s == "" }},
		{"json keeps stdout machine readable", true,
			func(s string) bool { var v map[string]any; return json.Unmarshal([]byte(s), &v) == nil },
			func(s string) bool { return strings.Contains(s, "dry run") }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "report.json")

			var stdout, stderr bytes.Buffer
			if err := emitReport(&rebuildFlags{json: tc.json, report: path}, rep, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}

			if !tc.wantStdout(stdout.String()) || !tc.wantStderr(stderr.String()) {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}

			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			var v map[string]any
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatalf("report file is not JSON: %v", err)
			}
		})
	}
}
